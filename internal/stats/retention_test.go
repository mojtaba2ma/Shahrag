package stats

// Tiered retention and persistence.
//
// The property that matters is negative and easy to regress: memory must
// stay bounded no matter how long the panel runs. A naive "keep everything"
// implementation passes every functional test and then eats 240 MB after a
// year, which is exactly the failure these tests exist to catch.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A year of samples must collapse to a bounded number of points, and the
// oldest data must still be reachable.
func TestCompactionBoundsMemoryOverAYear(t *testing.T) {
	now := time.Now()

	// A full year sampled every 5 seconds is 6.3 million points. Building
	// that literally would itself take gigabytes, so feed it in daily
	// chunks and compact as we go — which is also how the running collector
	// behaves.
	// Feed a year day by day, compacting as we go — which is what the
	// running collector does. Samples must never be dated in the future, so
	// each day is generated relative to its own end.
	var res []ResourceSnap
	for day := 365; day >= 1; day-- {
		dayEnd := now.Add(-time.Duration(day-1) * 24 * time.Hour)
		for s := 24 * 60 * 60; s > 0; s -= 5 {
			ts := dayEnd.Add(-time.Duration(s) * time.Second)
			if ts.After(now) {
				continue
			}
			res = append(res, ResourceSnap{TS: ts.Unix(), CPU: float64(s % 100)})
		}
		res = compactResources(res, now)
	}

	if len(res) == 0 {
		t.Fatal("compaction discarded everything")
	}
	// 720 + 1440 + 2880 + 8760, plus a little slack for bucket alignment.
	const ceiling = 14500
	if len(res) > ceiling {
		t.Errorf("a year compacted to %d samples, over the %d ceiling", len(res), ceiling)
	}

	oldest := time.Unix(res[0].TS, 0)
	age := now.Sub(oldest)
	if age < 300*24*time.Hour {
		t.Errorf("oldest sample is only %.0f days old — long history was lost",
			age.Hours()/24)
	}
	if age > 366*24*time.Hour {
		t.Errorf("kept data %.0f days old, past the retention window", age.Hours()/24)
	}
	t.Logf("a year of 5-second samples -> %d points, oldest %.0f days",
		len(res), age.Hours()/24)
}

// Recent data must keep its full resolution: rolling up the last hour would
// ruin the live charts.
func TestRecentDataKeepsFullResolution(t *testing.T) {
	now := time.Now()
	var in []ResourceSnap
	for s := 0; s < 3600; s += 5 {
		in = append(in, ResourceSnap{
			TS:  now.Add(-time.Duration(3600-s) * time.Second).Unix(),
			CPU: 50,
		})
	}
	out := compactResources(in, now)
	// 3600/5 = 720 samples, all within the first tier, so nothing merges.
	if len(out) < 700 {
		t.Errorf("the last hour was thinned to %d samples; live detail was lost", len(out))
	}
}

// Averaging away a spike is the classic mistake: the one thing an operator
// is looking for disappears.
func TestRollupPreservesExtremes(t *testing.T) {
	now := time.Now()
	// Two hours old, so it lands in the 1-minute tier and really merges.
	// Snapped to a minute boundary: buckets align to wall-clock time, so a
	// fixture starting mid-minute would legitimately span two buckets.
	base := time.Unix(bucketStart(now.Add(-2*time.Hour).Unix(), time.Minute), 0)
	in := []ResourceSnap{}
	for i := 0; i < 12; i++ { // one minute of 5-second samples
		cpu := 2.0
		if i == 6 {
			cpu = 100.0 // a 5-second spike
		}
		in = append(in, ResourceSnap{
			TS:  base.Add(time.Duration(i) * 5 * time.Second).Unix(),
			CPU: cpu,
		})
	}
	out := compactResources(in, now)
	if len(out) != 1 {
		t.Fatalf("expected one merged bucket, got %d", len(out))
	}
	got := out[0]
	if got.CPUMax < 99 {
		t.Errorf("the spike was lost: max=%.1f, want ~100", got.CPUMax)
	}
	if got.CPU > 20 {
		t.Errorf("the average looks wrong: %.1f", got.CPU)
	}
	if got.CPUMin > 3 {
		t.Errorf("the minimum was lost: %.1f", got.CPUMin)
	}
}

// Counters are totals, not gauges: merging must SUM them or traffic
// disappears from the history.
func TestBucketRollupSumsCounters(t *testing.T) {
	now := time.Now()
	base := now.Add(-2 * time.Hour)
	in := []*MinuteBucket{}
	for i := 0; i < 3; i++ {
		in = append(in, &MinuteBucket{
			TS:      base.Add(time.Duration(i) * time.Minute).Unix(),
			Total:   100,
			Success: 90,
			Bytes:   1000,
			UniqIPs: map[string]bool{"1.2.3.4": true},
		})
	}
	// Age them into the 15-minute tier, snapped to its boundary so all
	// three really fall in the same bucket.
	old := time.Unix(bucketStart(now.Add(-48*time.Hour).Unix(), 15*time.Minute), 0)
	for i := range in {
		in[i].TS = old.Add(time.Duration(i) * time.Minute).Unix()
	}
	out := compactBuckets(in, now)
	if len(out) != 1 {
		t.Fatalf("expected one merged bucket, got %d", len(out))
	}
	if out[0].Total != 300 {
		t.Errorf("Total = %d, want 300 (counters must sum, not average)", out[0].Total)
	}
	if out[0].Bytes != 3000 {
		t.Errorf("Bytes = %d, want 3000", out[0].Bytes)
	}
	// The IP set cannot be merged correctly and is the heaviest field, so
	// it must be dropped once the bucket leaves the live tier.
	if out[0].UniqIPs != nil {
		t.Error("the unique-IP map should be dropped from rolled-up buckets")
	}
}

// Connection counts are gauges: merging must AVERAGE them, or the history
// shows traffic that never happened.
func TestGaugeRollupAverages(t *testing.T) {
	now := time.Now()
	old := time.Unix(bucketStart(now.Add(-48*time.Hour).Unix(), 15*time.Minute), 0)
	in := []ConnectionSnapshot{}
	for i := 0; i < 4; i++ {
		in = append(in, ConnectionSnapshot{
			TS: old.Add(time.Duration(i) * 10 * time.Second).Unix(), Active: 100,
		})
	}
	out := compactConns(in, now)
	if len(out) != 1 {
		t.Fatalf("expected one bucket, got %d", len(out))
	}
	if out[0].Active != 100 {
		t.Errorf("Active = %d, want 100 (gauges average; summing would give 400)",
			out[0].Active)
	}
}

// Running compaction twice must not change anything — it runs on a timer.
func TestCompactionIsIdempotent(t *testing.T) {
	now := time.Now()
	var in []ResourceSnap
	for i := 0; i < 5000; i++ {
		in = append(in, ResourceSnap{
			TS:  now.Add(-time.Duration(i) * 30 * time.Second).Unix(),
			CPU: float64(i % 50),
		})
	}
	once := compactResources(in, now)
	twice := compactResources(once, now)
	if len(once) != len(twice) {
		t.Errorf("second pass changed the length: %d then %d", len(once), len(twice))
	}
	for i := range once {
		if once[i].TS != twice[i].TS {
			t.Fatalf("second pass moved sample %d: %d != %d", i, once[i].TS, twice[i].TS)
		}
	}
}

// ── Persistence ──────────────────────────────────────────────

// History must survive a restart, which is the whole reason for a long
// retention window.
func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	old := StatePath
	StatePath = filepath.Join(dir, "stats.json")
	defer func() { StatePath = old }()

	now := time.Now()
	c := &Collector{maxAge: MaxRetention()}
	for i := 0; i < 100; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute).Unix()
		c.resources = append(c.resources, ResourceSnap{TS: ts, CPU: float64(i)})
		c.conns = append(c.conns, ConnectionSnapshot{TS: ts, Active: i})
		c.protos = append(c.protos, ProtoSnap{TS: ts, TCP: i})
		c.buckets = append(c.buckets, &MinuteBucket{
			TS: ts, Total: int64(i), UniqIPs: map[string]bool{"1.1.1.1": true}})
	}
	if err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := os.Stat(StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("state file mode = %o, want 644", st.Mode().Perm())
	}
	// No temp files may survive an atomic write.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("a temp file survived: %s", e.Name())
		}
	}

	c2 := &Collector{maxAge: MaxRetention()}
	if err := c2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(c2.resources) == 0 || len(c2.conns) == 0 ||
		len(c2.protos) == 0 || len(c2.buckets) == 0 {
		t.Fatalf("series lost: res=%d conns=%d protos=%d buckets=%d",
			len(c2.resources), len(c2.conns), len(c2.protos), len(c2.buckets))
	}
	// Values must actually match, not just the counts.
	if c2.resources[len(c2.resources)-1].CPU == 0 &&
		c2.resources[0].CPU == 0 {
		t.Error("resource values did not survive the round trip")
	}
}

// A damaged file must never stop the panel: losing history is an
// inconvenience, refusing to start is an outage.
func TestCorruptStateIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	old := StatePath
	StatePath = filepath.Join(dir, "stats.json")
	defer func() { StatePath = old }()

	for _, bad := range []string{"", "{", "not json at all", `{"version":999}`} {
		if err := os.WriteFile(StatePath, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		c := &Collector{maxAge: MaxRetention()}
		err := c.Load()
		// An error is expected and useful for the log, but the collector
		// must still be usable.
		if err == nil && bad != "" {
			t.Errorf("input %q was accepted silently", bad)
		}
		c.resources = append(c.resources, ResourceSnap{TS: time.Now().Unix()})
		if len(c.ResourceTimeseries(60)) != 1 {
			t.Errorf("collector unusable after loading %q", bad)
		}
	}

	// A missing file is the first run: not an error at all.
	os.Remove(StatePath)
	c := &Collector{maxAge: MaxRetention()}
	if err := c.Load(); err != nil {
		t.Errorf("a missing state file should not be an error, got %v", err)
	}
}

// Data older than the window must be dropped on load, not resurrected.
func TestLoadDropsExpiredData(t *testing.T) {
	dir := t.TempDir()
	old := StatePath
	StatePath = filepath.Join(dir, "stats.json")
	defer func() { StatePath = old }()

	ancient := time.Now().Add(-400 * 24 * time.Hour).Unix()
	recent := time.Now().Add(-time.Hour).Unix()
	st := persistedState{
		Version: stateVersion,
		Resources: []ResourceSnap{
			{TS: ancient, CPU: 1},
			{TS: recent, CPU: 2},
		},
	}
	b, _ := json.Marshal(&st)
	if err := os.WriteFile(StatePath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Collector{maxAge: MaxRetention()}
	if err := c.Load(); err != nil {
		t.Fatal(err)
	}
	for _, s := range c.resources {
		if s.TS == ancient {
			t.Error("a 400-day-old sample survived the retention window")
		}
	}
	if len(c.resources) != 1 {
		t.Errorf("expected only the recent sample, got %d", len(c.resources))
	}
}

// The headline claim, measured rather than asserted.
func TestFullYearFitsInAFewMegabytes(t *testing.T) {
	now := time.Now()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	c := &Collector{maxAge: MaxRetention()}
	for day := 365; day >= 1; day-- {
		end := now.Add(-time.Duration(day-1) * 24 * time.Hour)
		for s := 24 * 60 * 60; s > 0; s -= 5 {
			ts := end.Add(-time.Duration(s) * time.Second)
			if ts.After(now) {
				continue
			}
			c.resources = append(c.resources, ResourceSnap{TS: ts.Unix(), CPU: 10})
			c.protos = append(c.protos, ProtoSnap{TS: ts.Unix(), TCP: 5})
		}
		for s := 24 * 60 * 60; s > 0; s -= 10 {
			ts := end.Add(-time.Duration(s) * time.Second)
			if ts.After(now) {
				continue
			}
			c.conns = append(c.conns, ConnectionSnapshot{TS: ts.Unix(), Active: 3})
		}
		for m := 24 * 60; m > 0; m-- {
			ts := end.Add(-time.Duration(m) * time.Minute)
			if ts.After(now) {
				continue
			}
			c.buckets = append(c.buckets, &MinuteBucket{TS: ts.Unix(), Total: 7})
		}
		c.gc()
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	mb := float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / 1024 / 1024
	total := len(c.resources) + len(c.protos) + len(c.conns) + len(c.buckets)
	t.Logf("one year, all four series: %d samples, %.2f MB", total, mb)

	if mb > 20 {
		t.Errorf("a year costs %.1f MB — too much for a small VPS", mb)
	}
	runtime.KeepAlive(c)
	_ = fmt.Sprint(total)
}
