package health

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// noDeps is a Deps with everything wired to something harmless, so a test
// exercises the /proc reading without forking anything.
func noDeps() Deps {
	return Deps{
		Build:          "rTEST",
		NginxActive:    func() bool { return true },
		NginxEnabled:   func() bool { return true },
		NginxVersion:   func() string { return "nginx/1.26.3" },
		NginxWorkerCon: func() int { return 768 },
		NginxFailure:   func() string { return "" },
		NginxConfTest:  func() (bool, string) { return true, "" },
		HoneypotOn:     func() (bool, string) { return true, "throttle" },
		AutoBanOn:      func() (bool, string) { return true, "notfound" },
		BanCounts:      func() (int, int, bool) { return 3, 1, true },
		CertExpiry:     func() Certs { return Certs{Total: 2, SoonestDays: 60, SoonestName: "a.example"} },
		TrafficNow:     func() Traffic { return Traffic{RequestsHour: 100, ErrorRatePct: 2} },
	}
}

func TestCollectReadsRealNumbers(t *testing.T) {
	c := NewCollector(noDeps())
	r := c.Collect()

	if r.Memory.TotalBytes <= 0 {
		t.Fatalf("no total memory read from /proc/meminfo")
	}
	if r.Memory.AvailableBytes <= 0 {
		t.Fatalf("no available memory read")
	}
	if r.Memory.UsedBytes >= r.Memory.TotalBytes {
		t.Fatalf("used (%d) should be below total (%d)", r.Memory.UsedBytes, r.Memory.TotalBytes)
	}
	if r.Disk.TotalBytes <= 0 {
		t.Fatalf("no disk size from statfs")
	}
	if r.Disk.InodesTotal <= 0 {
		t.Fatalf("no inode count from statfs")
	}
	if r.CPU.Cores <= 0 {
		t.Fatalf("no cores")
	}
	if r.Panel.RssAnonBytes <= 0 {
		t.Fatalf("RssAnon not read from /proc/self/status")
	}
	if r.Panel.Goroutines <= 0 {
		t.Fatalf("goroutine count is nonsense")
	}
	if r.Panel.OpenFDs <= 0 {
		t.Fatalf("fd count is nonsense")
	}
	if r.UptimeSec <= 0 {
		t.Fatalf("uptime not read")
	}
}

// The central promise of this package: swap is judged by the RATE, not the
// gauge. A machine whose swap is completely full but where nothing is moving
// is healthy, and the report must say so.
func TestFullSwapWithNoPagingIsHealthy(t *testing.T) {
	s := Swap{
		TotalBytes: 400 << 20,
		UsedBytes:  400 << 20,
		UsedPct:    100,
		Measured:   true,
		// No pages moving.
		InPagesPerSec:  0,
		OutPagesPerSec: 0,
	}
	got := gradeSwap(s)
	if got.Level != LevelOK {
		t.Fatalf("100%% swap with zero paging graded %q, want ok", got.Level)
	}
	if got.Hint != "swap_idle" {
		t.Fatalf("hint = %q, want swap_idle", got.Hint)
	}
	// The gauge must still be visible — hiding it would be its own kind of
	// dishonesty.
	if !strings.Contains(got.Detail, "100%") {
		t.Fatalf("detail %q does not carry the gauge", got.Detail)
	}
}

// And the opposite: barely any swap used, but pages flying in and out. That
// is the machine that is actually in trouble.
func TestQuietGaugeWithHeavyPagingIsBad(t *testing.T) {
	s := Swap{
		TotalBytes:     400 << 20,
		UsedBytes:      8 << 20, // 2%
		UsedPct:        2,
		Measured:       true,
		InPagesPerSec:  400,
		OutPagesPerSec: 300,
	}
	got := gradeSwap(s)
	if got.Level != LevelBad {
		t.Fatalf("700 pages/s graded %q, want bad", got.Level)
	}
	if got.Hint != "swap_thrashing" {
		t.Fatalf("hint = %q", got.Hint)
	}
}

func TestSwapRateBands(t *testing.T) {
	for _, tc := range []struct {
		rate float64
		want Level
	}{
		{0, LevelOK}, {10, LevelOK}, {49, LevelOK},
		{50, LevelWarn}, {200, LevelWarn}, {499, LevelWarn},
		{500, LevelBad}, {5000, LevelBad},
	} {
		got := gradeSwap(Swap{TotalBytes: 1 << 30, Measured: true, OutPagesPerSec: tc.rate})
		if got.Level != tc.want {
			t.Errorf("rate %.0f: got %q want %q", tc.rate, got.Level, tc.want)
		}
	}
}

// The first sample cannot produce a rate, and the report must SAY that
// rather than claiming a comfortable zero.
func TestFirstSampleAdmitsItHasNotMeasuredYet(t *testing.T) {
	c := NewCollector(noDeps())
	r := c.Collect()
	if r.Swap.TotalBytes > 0 && r.Swap.Measured {
		t.Fatalf("first Collect claims to have measured a rate")
	}
	var swap Check
	for _, ch := range r.Checks {
		if ch.ID == "swap" {
			swap = ch
		}
	}
	if r.Swap.TotalBytes > 0 && swap.Hint != "swap_measuring" {
		t.Fatalf("first report hint = %q, want swap_measuring", swap.Hint)
	}
}

// A second Collect after a real interval must produce a measured rate.
func TestSecondSampleMeasuresARate(t *testing.T) {
	c := NewCollector(noDeps())
	c.Collect()
	time.Sleep(600 * time.Millisecond)
	r := c.Collect()
	if r.Swap.TotalBytes == 0 {
		t.Skip("no swap configured on this machine")
	}
	if !r.Swap.Measured {
		t.Fatalf("second Collect still reports no measurement")
	}
	if r.Swap.InPagesPerSec < 0 || r.Swap.OutPagesPerSec < 0 {
		t.Fatalf("negative rate: %+v", r.Swap)
	}
}

// A counter that goes backwards (a reboot between samples) must not produce
// a negative rate.
func TestCounterResetDoesNotProduceANegativeRate(t *testing.T) {
	if got := perSec(-5000, 10); got != 0 {
		t.Fatalf("negative delta gave %v, want 0", got)
	}
	if got := perSec(100, 0); got != 0 {
		t.Fatalf("zero elapsed gave %v, want 0", got)
	}
}

// Two Collects in the same instant would divide by ~0. Reject rather than
// print a fictional rate.
func TestTwoCollectsInTheSameInstantDoNotMeasure(t *testing.T) {
	c := NewCollector(noDeps())
	c.Collect()
	r := c.Collect() // immediately, no sleep
	if r.Swap.Measured {
		t.Fatalf("a rate was claimed across a ~0 s interval")
	}
}

// Memory must be judged on MemAvailable, not MemFree. This is the classic
// mistake and it makes every healthy Linux box look full.
func TestMemoryUsesAvailableNotFree(t *testing.T) {
	c := NewCollector(noDeps())
	r := c.Collect()
	// Cache is normally hundreds of MB on any box that has done work. If
	// used were computed from MemFree, used would include the cache and
	// therefore exceed total-minus-available by roughly the cache size.
	want := r.Memory.TotalBytes - r.Memory.AvailableBytes
	if r.Memory.UsedBytes != want {
		t.Fatalf("used = %d, want total-available = %d", r.Memory.UsedBytes, want)
	}
}

// The overall level is the WORST check, not an average.
func TestOverallLevelIsTheWorstCheck(t *testing.T) {
	d := noDeps()
	d.NginxActive = func() bool { return false }
	c := NewCollector(d)
	r := c.Collect()
	if r.Level != LevelBad {
		t.Fatalf("nginx down but overall level is %q", r.Level)
	}
}

// Settings on with no engine running is the state that must never be
// reported as protected.
func TestAutoBanWithoutAnEngineWarns(t *testing.T) {
	got := gradeSecurity(Security{AutoBanOn: true, Engine: false})
	if got.Level != LevelWarn {
		t.Fatalf("graded %q, want warn", got.Level)
	}
	if got.Hint != "protection_engine" {
		t.Fatalf("hint = %q", got.Hint)
	}
}

// A tiny amount of traffic must not produce an error-rate verdict: 1 of 2
// requests failing is not "50% errors".
func TestErrorRateIsIgnoredBelowAMeaningfulSample(t *testing.T) {
	d := noDeps()
	d.TrafficNow = func() Traffic { return Traffic{RequestsHour: 2, ErrorRatePct: 50} }
	r := NewCollector(d).Collect()
	for _, ch := range r.Checks {
		if ch.ID == "errors" {
			t.Fatalf("error check emitted for a 2-request sample")
		}
	}

	d.TrafficNow = func() Traffic { return Traffic{RequestsHour: 500, ErrorRatePct: 50} }
	r = NewCollector(d).Collect()
	found := false
	for _, ch := range r.Checks {
		if ch.ID == "errors" {
			found = true
			if ch.Level != LevelBad {
				t.Fatalf("50%% of 500 graded %q", ch.Level)
			}
		}
	}
	if !found {
		t.Fatalf("no error check for 500 requests at 50%%")
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {512, "512 B"}, {1024, "1.0 KB"},
		{1536, "1.5 KB"}, {1 << 20, "1.0 MB"},
		{4_000_000, "3.8 MB"}, // the panel's own footprint
		{200 << 20, "200 MB"},
		{1 << 30, "1.0 GB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSortChecksPutsBadFirst(t *testing.T) {
	in := []Check{
		{ID: "a", Level: LevelOK},
		{ID: "b", Level: LevelBad},
		{ID: "c", Level: LevelWarn},
		{ID: "d", Level: LevelOK},
	}
	out := SortChecks(in)
	if out[0].ID != "b" || out[1].ID != "c" {
		t.Fatalf("order = %v", []string{out[0].ID, out[1].ID, out[2].ID, out[3].ID})
	}
	// Stable: the two OKs keep their original relative order.
	if out[2].ID != "a" || out[3].ID != "d" {
		t.Fatalf("not stable: %v", []string{out[2].ID, out[3].ID})
	}
	// The input must not be mutated — the caller may still be using it.
	if in[0].ID != "a" {
		t.Fatalf("SortChecks mutated its input")
	}
}

// Every check must carry a machine-readable ID, because the UI translates by
// ID and a check with an empty one would render as a blank row.
func TestEveryCheckHasAnID(t *testing.T) {
	r := NewCollector(noDeps()).Collect()
	if len(r.Checks) == 0 {
		t.Fatal("no checks produced")
	}
	seen := map[string]bool{}
	for _, ch := range r.Checks {
		if ch.ID == "" {
			t.Fatalf("a check has no ID: %+v", ch)
		}
		if seen[ch.ID] {
			t.Fatalf("duplicate check ID %q — the UI would render two rows with one key", ch.ID)
		}
		seen[ch.ID] = true
		if ch.Level != LevelOK && ch.Level != LevelWarn && ch.Level != LevelBad {
			t.Fatalf("check %q has an unknown level %q", ch.ID, ch.Level)
		}
	}
	// The checks a report must always contain, whatever the machine's
	// state. A missing one means a blank card in the UI.
	for _, must := range []string{"nginx", "memory", "cpu", "disk", "certs", "protection", "panel"} {
		if !seen[must] {
			t.Errorf("report is missing the %q check", must)
		}
	}
}

// Nil dependencies must not panic. The CLI builds a collector with none of
// them wired.
func TestNilDepsAreSafe(t *testing.T) {
	r := NewCollector(Deps{Build: "rX"}).Collect()
	if r.Memory.TotalBytes <= 0 {
		t.Fatal("still expected real memory numbers with no deps")
	}
}

func TestConcurrentCollectIsSafe(t *testing.T) {
	c := NewCollector(noDeps())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = c.Collect()
			}
		}()
	}
	wg.Wait()
}

// ── Cost, locked as behaviour ────────────────────────────────

// The expensive probes fork a process. They must be called at most once per
// TTL, no matter how often the page is refreshed. Without the cache, 50
// reports would be 50 forks of `nginx -t`.
func TestExpensiveProbesAreCached(t *testing.T) {
	var active, confTest int32
	d := noDeps()
	d.NginxActive = func() bool { atomic.AddInt32(&active, 1); return true }
	d.NginxConfTest = func() (bool, string) { atomic.AddInt32(&confTest, 1); return true, "" }

	c := NewCollector(d)
	for i := 0; i < 50; i++ {
		c.Collect()
	}
	// Let any background refresh finish before counting.
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&active); got > 1 {
		t.Fatalf("systemctl probe ran %d times for 50 reports (TTL is %v)", got, stateTTL)
	}
	if got := atomic.LoadInt32(&confTest); got > 1 {
		t.Fatalf("nginx -t ran %d times for 50 reports (TTL is %v)", got, confTestTTL)
	}
}

// ...but the cache must actually expire, or the page would show a stopped
// nginx as running forever.
func TestTheCacheExpires(t *testing.T) {
	old := stateTTL
	stateTTL = 50 * time.Millisecond
	defer func() { stateTTL = old }()

	var n int32
	d := noDeps()
	d.NginxActive = func() bool { atomic.AddInt32(&n, 1); return true }

	c := NewCollector(d)
	c.Collect()
	time.Sleep(120 * time.Millisecond)
	c.Collect()
	time.Sleep(100 * time.Millisecond) // the refresh is in the background

	if got := atomic.LoadInt32(&n); got < 2 {
		t.Fatalf("probe ran %d times across two expired windows, want >= 2", got)
	}
}

// Only the FIRST report may wait on a fork. Every later one is served from
// cache while the refresh happens behind it — otherwise a hung systemctl on
// a loaded box hangs the health page.
func TestOnlyTheFirstReportWaitsOnAFork(t *testing.T) {
	old := stateTTL
	stateTTL = 10 * time.Millisecond
	defer func() { stateTTL = old }()

	slow := func() bool { time.Sleep(150 * time.Millisecond); return true }
	d := noDeps()
	d.NginxActive = slow

	c := NewCollector(d)
	c.Collect() // pays the cost
	time.Sleep(30 * time.Millisecond)

	start := time.Now()
	c.Collect()
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("a stale report blocked for %v on a slow probe", elapsed)
	}
}

// A regenerate must drop the cached `nginx -t`, or the page reports the
// previous config's verdict for the new file.
func TestInvalidateConfigForcesAFreshTest(t *testing.T) {
	var n int32
	d := noDeps()
	d.NginxConfTest = func() (bool, string) { atomic.AddInt32(&n, 1); return true, "" }

	c := NewCollector(d)
	c.Collect()
	c.Collect()
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("probe ran %d times before invalidation", got)
	}
	c.InvalidateConfig()
	c.Collect()
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("probe ran %d times after invalidation, want 2", got)
	}
}

// The cost that is NOT cacheable — reading /proc — has to stay small enough
// that a 5-second refresh is free. This asserts a real budget rather than a
// benchmark, so it fails the build if a future change starts walking
// something expensive.
func TestCollectStaysCheap(t *testing.T) {
	c := NewCollector(noDeps())
	c.Collect() // warm the probe cache so we time only the /proc work

	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		c.Collect()
	}
	per := time.Since(start) / n

	// The dominant cost is countNginxWorkers, which reads one small file
	// per process in /proc. On the 2-core test box a report measures
	// ~1.5 ms; 15 ms is a 10x ceiling that will not flake on a loaded CI
	// box but will still catch anything genuinely expensive being added.
	if per > 15*time.Millisecond {
		t.Fatalf("one report costs %v, budget is 15ms", per)
	}
	t.Logf("one report = %v (%d samples)", per, n)
}

// Allocation budget. A report is a fixed-size struct plus a dozen checks; it
// must not allocate in proportion to how many processes or domains exist.
func TestCollectAllocationBudget(t *testing.T) {
	c := NewCollector(noDeps())
	c.Collect()

	allocs := testingAllocsPerRun(50, func() { c.Collect() })
	// Measured at ~900 on the test box, dominated by the /proc/<pid>/comm
	// reads. 3000 is a generous ceiling that still catches a regression
	// like re-parsing config.json per report (which was ~267 allocs a
	// call on its own, and would be called several times).
	if allocs > 3000 {
		t.Fatalf("a report allocates %.0f times, budget is 3000", allocs)
	}
	t.Logf("allocations per report: %.0f", allocs)
}

// testingAllocsPerRun wraps testing.AllocsPerRun so the intent is named.
func testingAllocsPerRun(runs int, f func()) float64 {
	return allocsPerRun(runs, f)
}

func TestReportIsSelfConsistent(t *testing.T) {
	r := NewCollector(noDeps()).Collect()
	if r.Memory.UsedPct > 100 || r.Memory.UsedPct < 0 {
		t.Fatalf("memory pct out of range: %v", r.Memory.UsedPct)
	}
	if r.Disk.UsedPct > 100 || r.Disk.UsedPct < 0 {
		t.Fatalf("disk pct out of range: %v", r.Disk.UsedPct)
	}
	if r.CPU.UsedPct > 100 || r.CPU.UsedPct < 0 {
		t.Fatalf("cpu pct out of range: %v", r.CPU.UsedPct)
	}
	if r.Swap.UsedPct > 100 || r.Swap.UsedPct < 0 {
		t.Fatalf("swap pct out of range: %v", r.Swap.UsedPct)
	}
	if r.TS == 0 {
		t.Fatal("no timestamp")
	}
	_ = fmt.Sprintf("%+v", r) // must not panic on any field
}
