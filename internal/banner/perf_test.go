package banner

// Cost of running permanently.
//
// The honeypot and the ban engine are always on, so their cost has to be
// bounded by the number of things being TRACKED, never by how much traffic
// arrives. That is the same lesson the statistics collector taught: a
// design that stores one record per event looks fine in a functional test
// and then eats hundreds of megabytes under a real scan.
//
// These tests pin the bound. They are written as assertions on measured
// behaviour rather than as benchmarks, so a regression fails the suite
// instead of quietly making the panel heavier.

import (
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"shahrag/internal/config"
)

func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / 1024 / 1024
}

// Memory must not grow with the number of REQUESTS, only with the number of
// addresses. An address that sends one probe and one that sends ten
// thousand must cost the same.
func TestMemoryDoesNotGrowWithTraffic(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	// A threshold nothing will reach, so every event accumulates instead
	// of turning into a ban — the worst case for the counters.
	ab.NotFound = config.AutoBanRule{Enabled: true, Hits: 1 << 30, WindowMinutes: 60, BanMinutes: 60}
	e, _, _ := newEngine(t, ab)

	const addrs = 2000
	ips := make([]string, addrs)
	for i := range ips {
		ips[i] = fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256)
	}

	now := time.Now()
	for _, ip := range ips {
		e.record(ip, ReasonNotFound, now)
	}
	one := heapMB()

	// The same addresses, a hundred times more traffic.
	for r := 0; r < 100; r++ {
		for _, ip := range ips {
			e.record(ip, ReasonNotFound, now)
		}
	}
	hundred := heapMB()

	growth := hundred - one
	t.Logf("%d addresses × 1 event   : %.2f MB", addrs, one)
	t.Logf("%d addresses × 101 events: %.2f MB  (growth %.2f MB)", addrs, hundred, growth)
	// A per-event design would have grown by roughly 100×; a counter
	// design grows by nothing measurable.
	if growth > 1.0 {
		t.Errorf("100× the traffic added %.2f MB — memory is growing with "+
			"requests rather than with addresses", growth)
	}
}

// The number of tracked addresses is capped, so even a huge botnet cannot
// exhaust memory.
func TestTrackedAddressesAreCapped(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.NotFound = config.AutoBanRule{Enabled: true, Hits: 1 << 30, WindowMinutes: 60, BanMinutes: 60}
	e, _, _ := newEngine(t, ab)

	now := time.Now()
	for i := 0; i < maxTrackedIPs+5000; i++ {
		e.record(fmt.Sprintf("%d.%d.%d.%d",
			11+(i/16777216)%200, (i/65536)%256, (i/256)%256, i%256),
			ReasonNotFound, now)
	}
	e.mu.RLock()
	n := len(e.events)
	e.mu.RUnlock()
	if n > maxTrackedIPs {
		t.Errorf("tracking %d addresses, over the %d cap", n, maxTrackedIPs)
	}
	t.Logf("offered %d addresses, tracking %d", maxTrackedIPs+5000, n)
}

// One counter must stay small. This is the number that decides the worst
// case, so it is asserted directly.
func TestCounterIsSmall(t *testing.T) {
	var c counter
	size := int(unsafeSizeof(c))
	t.Logf("counter is %d bytes; %d of them is %.1f MB",
		size, maxTrackedIPs, float64(size*maxTrackedIPs)/1024/1024)
	// An earlier attempt used 60 per-minute buckets per address and came
	// to 968 bytes, which measured 217 MB for 200,000 addresses — worse
	// than the naive design it replaced.
	if size > 96 {
		t.Errorf("a counter is %d bytes; the whole point is that it is tiny", size)
	}
}

// Recording an offence must not read the configuration file. It used to,
// and a 200,000 event scan took 28 seconds almost entirely in config reads.
func TestRecordingDoesNotReadTheConfigFile(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.NotFound = config.AutoBanRule{Enabled: true, Hits: 1 << 30, WindowMinutes: 60, BanMinutes: 60}
	e, _, _ := newEngine(t, ab)
	e.refreshWindows(ab)

	now := time.Now()
	start := time.Now()
	const n = 200000
	for i := 0; i < n; i++ {
		e.record(fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256),
			ReasonNotFound, now)
	}
	elapsed := time.Since(start)
	perEvent := elapsed / n
	t.Logf("%d records in %s (%s each)", n, elapsed.Round(time.Millisecond), perEvent)
	// Reading the config per event measured about 140µs each. A cached
	// window is well under a microsecond; 20µs leaves ample margin for a
	// slow CI machine while still catching a reintroduced file read.
	if perEvent > 20*time.Microsecond {
		t.Errorf("%s per recorded offence — something expensive is happening "+
			"on the hot path", perEvent)
	}
}

// A scan with nothing new must be cheap: it runs every 15 seconds forever.
func TestIdleScanIsCheap(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	e, _, _ := newEngine(t, ab)

	start := time.Now()
	for i := 0; i < 50; i++ {
		e.Scan()
	}
	per := time.Since(start) / 50
	t.Logf("idle scan: %s each", per)
	if per > 5*time.Millisecond {
		t.Errorf("an idle scan costs %s; it runs every %s forever", per, ScanInterval)
	}
}

// Addresses are forgotten once their counters decay, so a scan that has
// gone quiet releases its memory instead of holding it for a day.
func TestQuietAddressesAreForgotten(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.NotFound = config.AutoBanRule{Enabled: true, Hits: 1 << 30, WindowMinutes: 5, BanMinutes: 60}
	e, _, _ := newEngine(t, ab)
	e.refreshWindows(ab)

	old := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 1000; i++ {
		e.record(fmt.Sprintf("10.0.%d.%d", i/256, i%256), ReasonNotFound, old)
	}
	e.mu.RLock()
	before := len(e.events)
	e.mu.RUnlock()

	now := time.Now()
	e.mu.Lock()
	e.pruneLocked(now, ab)
	e.mu.Unlock()

	e.mu.RLock()
	after := len(e.events)
	e.mu.RUnlock()
	t.Logf("%d addresses tracked, %d after pruning half an hour later", before, after)
	if after != 0 {
		t.Errorf("%d addresses survived long past their 5-minute window — "+
			"memory is held far longer than any rule can use", after)
	}
}

// A burst is counted; a trickle decays. This is the property that makes the
// decay approximation safe: scanners burst, real visitors do not.
func TestBurstCountsButTrickleDecays(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.NotFound = config.AutoBanRule{Enabled: true, Hits: 10, WindowMinutes: 10, BanMinutes: 60}
	e, _, _ := newEngine(t, ab)
	e.refreshWindows(ab)

	// A burst of 10 inside a second: must be banned.
	now := time.Now()
	for i := 0; i < 10; i++ {
		e.record("203.0.113.1", ReasonNotFound, now)
	}
	e.evaluate(ab, now)
	if !banned(e, "203.0.113.1") {
		t.Error("a burst of 10 in one second was not banned")
	}

	// A trickle: one every 9 minutes, ten times. Each has almost entirely
	// decayed before the next arrives, so it must NOT be banned.
	tm := time.Now()
	for i := 0; i < 10; i++ {
		tm = tm.Add(9 * time.Minute)
		e.record("203.0.113.2", ReasonNotFound, tm)
		e.evaluate(ab, tm)
	}
	if banned(e, "203.0.113.2") {
		t.Error("a slow trickle far below the rate was banned — this is the " +
			"false positive that costs real users")
	}
}

// unsafeSizeof reports a value's in-memory size.
func unsafeSizeof(c counter) uintptr {
	return unsafe.Sizeof(c)
}
