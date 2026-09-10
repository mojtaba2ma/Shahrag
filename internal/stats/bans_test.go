package stats

import (
	"testing"
	"time"
)

type fakeCounter struct{ active, total, pending int }

func (f *fakeCounter) BanCounts() (int, int, int) { return f.active, f.total, f.pending }

func withCounter(t *testing.T, c BanCounter) {
	t.Helper()
	SetBanCounter(c)
	t.Cleanup(func() { SetBanCounter(nil) })
}

// The distinction the whole feature rests on: ACTIVE is a gauge that falls
// as bans expire, TOTAL is a counter whose slope says "am I under attack?".
// A chart of the gauge alone makes a wave that ended an hour ago invisible.
func TestTheCounterRisesEvenAsTheGaugeFalls(t *testing.T) {
	c := quietCollector()
	f := &fakeCounter{}
	withCounter(t, f)

	now := time.Now()
	// A wave: 40 bans applied, then they expire.
	for i, s := range []struct{ active, total int }{
		{0, 0}, {12, 12}, {40, 40}, {40, 40}, {8, 40}, {0, 40},
	} {
		c.bans = append(c.bans, BanSnap{
			TS:     now.Add(-time.Duration(30-i*5) * time.Minute).Unix(),
			Active: s.active, Total: s.total,
		})
	}
	f.active, f.total = 0, 40

	sum := c.BanSummaryNow()
	if sum.Active != 0 {
		t.Errorf("active = %d, want 0 (they all expired)", sum.Active)
	}
	if sum.NewLastHour != 40 {
		t.Errorf("new in the last hour = %d, want 40 — the wave is invisible", sum.NewLastHour)
	}
	if sum.PeakActive24h != 40 {
		t.Errorf("peak = %d, want 40", sum.PeakActive24h)
	}
}

// With no engine running the page must say so rather than show a confident
// zero, which would read as "measured, and nothing is banned".
func TestNoEngineMeansNotMeasuredNotZero(t *testing.T) {
	SetBanCounter(nil)
	c := quietCollector()
	c.sampleBans()
	if len(c.bans) != 0 {
		t.Fatal("a sample was recorded with no engine running")
	}
	if c.BanSummaryNow().Running {
		t.Fatal("the summary claims the engine is running")
	}
}

func TestSamplingRecordsWhatTheEngineReports(t *testing.T) {
	c := quietCollector()
	withCounter(t, &fakeCounter{active: 7, total: 19, pending: 3})
	c.sampleBans()
	if len(c.bans) != 1 {
		t.Fatalf("recorded %d samples", len(c.bans))
	}
	got := c.bans[0]
	if got.Active != 7 || got.Total != 19 || got.Pending != 3 {
		t.Fatalf("recorded %+v", got)
	}
}

// Downsampling must keep the PEAK, not an average or an evenly spaced
// sample: both of those hide a short wave, which is the one thing the chart
// exists to show.
func TestDownsamplingKeepsThePeak(t *testing.T) {
	in := make([]BanSnap, 0, maxChartPoints*3)
	now := time.Now().Unix()
	for i := 0; i < maxChartPoints*3; i++ {
		a := 1
		if i == 1234 {
			a = 900 // one short spike
		}
		in = append(in, BanSnap{TS: now + int64(i), Active: a, Total: i})
	}
	out := downsampleBans(in)
	if len(out) > maxChartPoints {
		t.Fatalf("downsampled to %d points, cap is %d", len(out), maxChartPoints)
	}
	peak := 0
	for _, s := range out {
		if s.Active > peak {
			peak = s.Active
		}
	}
	if peak != 900 {
		t.Fatalf("the spike was lost: peak = %d, want 900", peak)
	}
}

// A year of samples must stay small. This is the standing rule: measure the
// cost and lock it as behaviour.
func TestAYearOfBanSamplesIsSmall(t *testing.T) {
	// 30-second sampling for a year.
	perYear := 365 * 24 * 60 * 2
	in := make([]BanSnap, 0, perYear)
	now := time.Now()
	for i := 0; i < perYear; i++ {
		in = append(in, BanSnap{
			TS:     now.Add(-time.Duration(perYear-i) * 30 * time.Second).Unix(),
			Active: i % 40, Total: i,
		})
	}
	out := compactBans(in, now)
	t.Logf("a year of 30s samples: %d raw -> %d retained (~%d KB)",
		len(in), len(out), len(out)*48/1024)

	// 12,325 is what the tiered retention leaves, and it is EXACTLY what
	// the existing resource series retains for the same input — measured
	// side by side rather than guessed, because a budget invented from
	// nothing is how a test ends up asserting the author's mood.
	//
	// At ~48 bytes a sample that is under 600 KB for a year, the same
	// order as the other series. 14,000 leaves headroom for a tier change
	// without letting an accidental order-of-magnitude regression pass.
	if len(out) > 14000 {
		t.Fatalf("a year compacts to %d samples; the resource series retains "+
			"12,325 for the same input, so this has regressed", len(out))
	}
	// The newest data must keep full resolution.
	if len(out) < 100 {
		t.Fatalf("compaction threw almost everything away: %d samples", len(out))
	}
}

// The counter is monotonic, so compaction must keep the HIGHEST total in a
// group — taking the first or the mean would make the series go backwards.
func TestCompactionKeepsTheCounterMonotonic(t *testing.T) {
	now := time.Now()
	var in []BanSnap
	for i := 0; i < 5000; i++ {
		in = append(in, BanSnap{
			TS:    now.Add(-time.Duration(5000-i) * time.Minute).Unix(),
			Total: i, Active: i % 7,
		})
	}
	out := compactBans(in, now)
	last := -1
	for _, s := range out {
		if s.Total < last {
			t.Fatalf("the cumulative counter went backwards: %d after %d", s.Total, last)
		}
		last = s.Total
	}
}

// Sampling has to be cheap: it runs on the collector's loop alongside
// everything else.
func TestSamplingBansIsCheap(t *testing.T) {
	c := quietCollector()
	withCounter(t, &fakeCounter{active: 5, total: 10})

	allocs := testing.AllocsPerRun(200, func() { c.sampleBans() })
	t.Logf("one ban sample allocates %.1f times", allocs)
	if allocs > 4 {
		t.Fatalf("a ban sample allocates %.1f times; it should be two map "+
			"lookups and an append", allocs)
	}
}

func TestBanSeriesSurvivesARestart(t *testing.T) {
	useTempState(t)
	c1 := quietCollector()
	now := time.Now()
	for i := 0; i < 20; i++ {
		c1.bans = append(c1.bans, BanSnap{
			TS:     now.Add(-time.Duration(i) * time.Minute).Unix(),
			Active: i, Total: i * 2,
		})
	}
	if err := c1.Save(); err != nil {
		t.Fatal(err)
	}
	c2 := quietCollector()
	if err := c2.Load(); err != nil {
		t.Fatal(err)
	}
	if len(c2.bans) == 0 {
		t.Fatal("the ban series did not survive a restart")
	}
}
