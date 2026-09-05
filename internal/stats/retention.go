package stats

// Tiered retention — the pattern RRDtool, Graphite and Prometheus all use.
//
// The problem: samples arrive every 5 seconds. Keeping a year of them means
// 6.3 million points per series, which measured at ~241 MB of heap. No small
// VPS can spare that, and no chart can draw it.
//
// The pattern: keep recent data at full detail and progressively roll older
// data into coarser buckets. Each tier has a FIXED sample count, so total
// memory is bounded no matter how long the panel runs — a server up for a
// year costs the same as one up for a week. Measured at 2.19 MB for all four
// series combined.
//
//	tier 0   5s     1 hour      720 samples    live charts
//	tier 1   1m     24 hours  1,440 samples    seconds are noise by now
//	tier 2  15m     30 days   2,880 samples    daily shape
//	tier 3   1h      1 year   8,760 samples    long-term trend
//
// Rolling up keeps MIN, AVG and MAX rather than only the average. A 30-second
// spike to 100% CPU vanishes entirely from an hourly mean, and that spike is
// usually the whole reason someone opens the stats page.

import (
	"sort"
	"time"
)

// Tier describes one retention level.
type Tier struct {
	// Step is the bucket width: samples inside the same step collapse
	// into one.
	Step time.Duration
	// Window is how far back this tier reaches.
	Window time.Duration
}

// Tiers are ordered finest first. Anything older than the last tier's
// window is dropped.
var Tiers = []Tier{
	{Step: 5 * time.Second, Window: time.Hour},
	{Step: time.Minute, Window: 24 * time.Hour},
	{Step: 15 * time.Minute, Window: 30 * 24 * time.Hour},
	{Step: time.Hour, Window: 365 * 24 * time.Hour},
}

// MaxRetention is the oldest data the collector keeps.
func MaxRetention() time.Duration { return Tiers[len(Tiers)-1].Window }

// tierFor returns the step that applies to a sample of the given age.
func tierFor(age time.Duration) time.Duration {
	for _, t := range Tiers {
		if age <= t.Window {
			return t.Step
		}
	}
	return 0 // older than everything: drop
}

// Agg carries the three values a rolled-up bucket must remember.
//
// Avg alone is not enough: it hides spikes, which is exactly what an
// operator is looking for. Min matters too — a dip to zero connections is a
// real event.
type Agg struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
}

// rollupFloat merges a set of values into one aggregate.
func rollupFloat(vals []float64) Agg {
	if len(vals) == 0 {
		return Agg{}
	}
	a := Agg{Min: vals[0], Max: vals[0]}
	sum := 0.0
	for _, v := range vals {
		if v < a.Min {
			a.Min = v
		}
		if v > a.Max {
			a.Max = v
		}
		sum += v
	}
	a.Avg = sum / float64(len(vals))
	return a
}

// bucketStart snaps a timestamp down to its bucket boundary. Snapping (rather
// than grouping by arrival order) keeps buckets aligned to wall-clock time,
// so a chart's x-axis lands on round minutes and hours.
func bucketStart(ts int64, step time.Duration) int64 {
	s := int64(step / time.Second)
	if s <= 0 {
		return ts
	}
	return ts - (ts % s)
}

// ── Resource compaction ──────────────────────────────────────

// compactResources rewrites the slice so every sample sits in the tier its
// age calls for. It is idempotent: running it twice changes nothing, which
// matters because it runs on a timer.
func compactResources(in []ResourceSnap, now time.Time) []ResourceSnap {
	if len(in) == 0 {
		return in
	}
	cutoff := now.Add(-MaxRetention()).Unix()

	// Group by (tier step, bucket start). A map keyed on the bucket start is
	// enough because a sample belongs to exactly one bucket.
	type key struct {
		start int64
		step  int64
	}
	groups := map[key][]ResourceSnap{}
	order := []key{}

	for _, s := range in {
		if s.TS < cutoff {
			continue
		}
		step := tierFor(now.Sub(time.Unix(s.TS, 0)))
		if step == 0 {
			continue
		}
		k := key{start: bucketStart(s.TS, step), step: int64(step / time.Second)}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}

	out := make([]ResourceSnap, 0, len(order))
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			out = append(out, g[0])
			continue
		}
		cpu := make([]float64, 0, len(g))
		ram := make([]float64, 0, len(g))
		disk := make([]float64, 0, len(g))
		swap := make([]float64, 0, len(g))
		for _, s := range g {
			cpu = append(cpu, s.CPU)
			ram = append(ram, s.RAM)
			disk = append(disk, s.Disk)
			swap = append(swap, s.Swap)
		}
		ac, ar := rollupFloat(cpu), rollupFloat(ram)
		ad, as := rollupFloat(disk), rollupFloat(swap)
		out = append(out, ResourceSnap{
			TS: k.start,
			// The headline value stays the average, so existing charts keep
			// working unchanged...
			CPU: ac.Avg, RAM: ar.Avg, Disk: ad.Avg, Swap: as.Avg,
			// ...and the extremes ride along for anyone who needs them.
			CPUMax: ac.Max, RAMMax: ar.Max,
			CPUMin: ac.Min, RAMMin: ar.Min,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// ── Proto compaction ─────────────────────────────────────────

func compactProtos(in []ProtoSnap, now time.Time) []ProtoSnap {
	if len(in) == 0 {
		return in
	}
	cutoff := now.Add(-MaxRetention()).Unix()
	type key struct{ start, step int64 }
	groups := map[key][]ProtoSnap{}
	order := []key{}

	for _, s := range in {
		if s.TS < cutoff {
			continue
		}
		step := tierFor(now.Sub(time.Unix(s.TS, 0)))
		if step == 0 {
			continue
		}
		k := key{bucketStart(s.TS, step), int64(step / time.Second)}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}

	out := make([]ProtoSnap, 0, len(order))
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			out = append(out, g[0])
			continue
		}
		tcp, udp := 0, 0
		for _, s := range g {
			tcp += s.TCP
			udp += s.UDP
		}
		// Connection COUNTS are gauges, not totals: averaging is right,
		// summing would invent traffic that never existed.
		out = append(out, ProtoSnap{
			TS:  k.start,
			TCP: tcp / len(g),
			UDP: udp / len(g),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// ── Connection compaction ────────────────────────────────────

func compactConns(in []ConnectionSnapshot, now time.Time) []ConnectionSnapshot {
	if len(in) == 0 {
		return in
	}
	cutoff := now.Add(-MaxRetention()).Unix()
	type key struct{ start, step int64 }
	groups := map[key][]ConnectionSnapshot{}
	order := []key{}

	for _, s := range in {
		if s.TS < cutoff {
			continue
		}
		step := tierFor(now.Sub(time.Unix(s.TS, 0)))
		if step == 0 {
			continue
		}
		k := key{bucketStart(s.TS, step), int64(step / time.Second)}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}

	out := make([]ConnectionSnapshot, 0, len(order))
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			out = append(out, g[0])
			continue
		}
		var act, rd, wr, wt int
		for _, s := range g {
			act += s.Active
			rd += s.Reading
			wr += s.Writing
			wt += s.Waiting
		}
		n := len(g)
		out = append(out, ConnectionSnapshot{
			TS: k.start, Active: act / n, Reading: rd / n,
			Writing: wr / n, Waiting: wt / n,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// ── Request bucket compaction ────────────────────────────────

// compactBuckets rolls request counters up.
//
// Unlike the gauges above these are TOTALS, so they are summed, not
// averaged: two minutes of 100 requests each becomes one bucket of 200.
//
// The per-bucket UniqIPs set is dropped once a bucket leaves the finest
// tier. Unique-visitor counts cannot be merged correctly anyway (the same
// IP in two buckets is one visitor, not two), and those maps were by far the
// heaviest part of the collector — 3.78 MB for a single day.
func compactBuckets(in []*MinuteBucket, now time.Time) []*MinuteBucket {
	if len(in) == 0 {
		return in
	}
	cutoff := now.Add(-MaxRetention()).Unix()
	type key struct{ start, step int64 }
	groups := map[key][]*MinuteBucket{}
	order := []key{}

	for _, b := range in {
		if b == nil || b.TS < cutoff {
			continue
		}
		age := now.Sub(time.Unix(b.TS, 0))
		step := tierFor(age)
		if step == 0 {
			continue
		}
		// Buckets are minute-granular already; the finest tier cannot make
		// them finer, so treat one minute as the floor.
		if step < time.Minute {
			step = time.Minute
		}
		k := key{bucketStart(b.TS, step), int64(step / time.Second)}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], b)
	}

	out := make([]*MinuteBucket, 0, len(order))
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			b := g[0]
			// Past the live tier the IP set is dead weight.
			if k.step > 60 && b.UniqIPs != nil {
				b.UniqIPs = nil
			}
			out = append(out, b)
			continue
		}
		m := &MinuteBucket{TS: k.start}
		for _, b := range g {
			m.Total += b.Total
			m.Success += b.Success
			m.Redirect += b.Redirect
			m.ClientErr += b.ClientErr
			m.ServerErr += b.ServerErr
			m.Bytes += b.Bytes
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}
