package stats

// Ban counts over time.
//
// Two different numbers, and conflating them is the mistake to avoid:
//
//	ACTIVE   how many addresses are blocked at this instant — a gauge,
//	         which goes up and down as bans expire
//	TOTAL    how many bans have ever been applied — a counter, which only
//	         ever rises
//
// An operator asking "am I under attack?" wants the second one's SLOPE. An
// operator asking "is anything blocked right now?" wants the first. A chart
// showing only the gauge makes a four-hour ban wave look like nothing an
// hour after it ended.
//
// Cost. This is a sample every 30 seconds of two integers, folded into the
// same tiered retention as everything else, so a year of history is a few
// hundred kilobytes rather than the megabytes a per-event log would be. The
// sample itself is two map lookups: the numbers are supplied by the ban
// engine, which already has them.

import (
	"sync"
	"time"
)

// BanSnap is one sample.
type BanSnap struct {
	TS int64 `json:"ts"`
	// Active is how many addresses were blocked at that moment.
	Active int `json:"active"`
	// Total is the cumulative count of bans ever applied. Monotonic, so
	// the difference between two samples is how many were applied in
	// between — which is the number that answers "am I under attack?".
	Total int `json:"total"`
	// Pending is how many addresses are accumulating offences without
	// having crossed a threshold. An early warning: it rises before the
	// bans do.
	Pending int `json:"pending"`

	// Extremes, filled once a sample has been rolled up into a coarser
	// tier. A ban wave that lasted two minutes vanishes from an hourly
	// mean, and that wave is usually why the page was opened.
	ActiveMax int `json:"active_max,omitempty"`
}

// BanCounter is the source of the numbers.
//
// An interface so this package does not import banner, which would be a
// cycle: the ban engine already reads config, and the web server wires the
// two together.
type BanCounter interface {
	// BanCounts returns the active, total-ever and pending counts.
	BanCounts() (active, total, pending int)
}

var (
	banSrcMu sync.RWMutex
	banSrc   BanCounter
)

// SetBanCounter wires the running engine in. Safe to call with nil.
func SetBanCounter(c BanCounter) {
	banSrcMu.Lock()
	banSrc = c
	banSrcMu.Unlock()
}

// sampleBans records one point. Called from the collector's loop.
func (c *Collector) sampleBans() {
	banSrcMu.RLock()
	src := banSrc
	banSrcMu.RUnlock()
	if src == nil {
		// No engine running: record nothing rather than a row of zeros,
		// which would draw a flat line implying "measured, and none"
		// when the truth is "not measured".
		return
	}
	active, total, pending := src.BanCounts()
	c.mu.Lock()
	c.bans = append(c.bans, BanSnap{
		TS: time.Now().Unix(), Active: active, Total: total, Pending: pending,
	})
	c.mu.Unlock()
}

// BanTimeseries returns ban samples for the last `minutes`, downsampled to
// at most maxChartPoints so a year-long range is still one small response.
func (c *Collector) BanTimeseries(minutes int) []BanSnap {
	c.mu.RLock()
	defer c.mu.RUnlock()
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	out := []BanSnap{}
	for _, s := range c.bans {
		if s.TS >= since {
			out = append(out, s)
		}
	}
	return downsampleBans(out)
}

// BanSummary is the headline figures for the stats page.
type BanSummary struct {
	Active  int `json:"active"`
	Pending int `json:"pending"`
	Total   int `json:"total"`
	// NewLastHour / NewLast24h are the counter's rise over those windows —
	// the "am I under attack?" numbers.
	NewLastHour int `json:"new_last_hour"`
	NewLast24h  int `json:"new_last_24h"`
	// PeakActive24h is the highest simultaneous ban count in a day.
	PeakActive24h int `json:"peak_active_24h"`
	// Running says whether the engine is actually going, so the page can
	// say "not measured" rather than showing a confident zero.
	Running bool `json:"running"`
}

// BanSummaryNow computes the headline figures.
func (c *Collector) BanSummaryNow() BanSummary {
	banSrcMu.RLock()
	src := banSrc
	banSrcMu.RUnlock()

	var out BanSummary
	if src != nil {
		out.Running = true
		out.Active, out.Total, out.Pending = src.BanCounts()
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	hourAgo := now.Add(-time.Hour).Unix()
	dayAgo := now.Add(-24 * time.Hour).Unix()

	// The rise of a monotonic counter over a window is (latest - earliest
	// within the window). Reading the earliest sample INSIDE the window
	// rather than the one before it slightly understates a spike that
	// started before the window opened, which is the safe direction.
	var firstHour, firstDay = -1, -1
	for _, s := range c.bans {
		if s.TS >= dayAgo && firstDay < 0 {
			firstDay = s.Total
		}
		if s.TS >= hourAgo && firstHour < 0 {
			firstHour = s.Total
		}
		if s.TS >= dayAgo {
			a := s.Active
			if s.ActiveMax > a {
				a = s.ActiveMax
			}
			if a > out.PeakActive24h {
				out.PeakActive24h = a
			}
		}
	}
	if firstHour >= 0 && out.Total >= firstHour {
		out.NewLastHour = out.Total - firstHour
	}
	if firstDay >= 0 && out.Total >= firstDay {
		out.NewLast24h = out.Total - firstDay
	}
	if out.Active > out.PeakActive24h {
		out.PeakActive24h = out.Active
	}
	return out
}

// downsampleBans thins a series, keeping the PEAK of each group.
//
// Not an average and not an evenly-spaced sample: the question a ban chart
// answers is "how bad did it get?", and both of those hide a short wave.
// The first and last points are kept so the axis still spans the window.
func downsampleBans(in []BanSnap) []BanSnap {
	n := len(in)
	if n <= maxChartPoints {
		return in
	}
	out := make([]BanSnap, 0, maxChartPoints)
	step := float64(n) / float64(maxChartPoints)
	for i := 0; i < maxChartPoints; i++ {
		lo := int(float64(i) * step)
		hi := int(float64(i+1) * step)
		if hi > n {
			hi = n
		}
		if lo >= hi {
			continue
		}
		best := in[lo]
		for _, s := range in[lo:hi] {
			if s.Active > best.Active {
				best = s
			}
			if s.Total > best.Total {
				best.Total = s.Total
			}
		}
		out = append(out, best)
	}
	return out
}

// compactBans folds old ban samples into coarser tiers, exactly as the
// resource series does, so a year of history stays small.
func compactBans(in []BanSnap, now time.Time) []BanSnap {
	if len(in) == 0 {
		return in
	}
	maxAge := MaxRetention()
	cutoff := now.Add(-maxAge).Unix()

	type group struct {
		snap BanSnap
		max  int
	}
	groups := map[int64]*group{}
	var order []int64

	for _, s := range in {
		if s.TS < cutoff {
			continue
		}
		age := now.Sub(time.Unix(s.TS, 0))
		step := tierFor(age)
		bucket := bucketStart(s.TS, step)
		g := groups[bucket]
		if g == nil {
			g = &group{snap: s}
			g.snap.TS = bucket
			g.max = s.Active
			groups[bucket] = g
			order = append(order, bucket)
			continue
		}
		// Keep the PEAK active count and the HIGHEST total: the total is
		// monotonic, so the highest is the latest.
		if s.Active > g.max {
			g.max = s.Active
		}
		if s.Total > g.snap.Total {
			g.snap.Total = s.Total
		}
		if s.Pending > g.snap.Pending {
			g.snap.Pending = s.Pending
		}
		g.snap.Active = s.Active
	}

	out := make([]BanSnap, 0, len(order))
	sortInt64s(order)
	for _, b := range order {
		g := groups[b]
		s := g.snap
		if g.max > s.Active {
			s.ActiveMax = g.max
		}
		out = append(out, s)
	}
	return out
}

func sortInt64s(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
