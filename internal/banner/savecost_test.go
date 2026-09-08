package banner

// How much does persisting a ban immediately actually cost?
//
// The obvious fix for "a ban is lost if the machine dies before the next
// five-minute flush" is to save on every change. The obvious objection is
// that a scan can ban hundreds of addresses at once, and hundreds of fsyncs
// would be a disaster.
//
// Both need numbers rather than opinions, so this measures them. The design
// that came out of it: ONE save per scan (not one per address) plus an
// immediate save for each manual action, which are rare by definition.

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"shahrag/internal/config"
)

func benchEngine(t *testing.T, n int) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, _ = mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = config.DefaultAutoBan()
		c.AutoBan.Enabled = true
		return nil
	})
	old := StatePath
	StatePath = filepath.Join(dir, "bans.json")
	t.Cleanup(func() { StatePath = old })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)
	now := time.Now()
	e.mu.Lock()
	for i := 0; i < n; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", i/65536%256, i/256%256, i%256)
		e.bans[ip] = Ban{IP: ip, Reason: ReasonHoneypot, Hits: 3,
			BannedAt: now, ExpiresAt: now.Add(4 * time.Hour)}
	}
	e.mu.Unlock()
	return e, StatePath
}

// What one save costs at realistic and at extreme ban counts.
func TestSaveCostIsBounded(t *testing.T) {
	for _, n := range []int{10, 100, 1000, 5000} {
		e, path := benchEngine(t, n)

		// Best of five: the first write to a fresh temp dir pays for
		// directory metadata that later ones do not.
		best := time.Hour
		for i := 0; i < 5; i++ {
			start := time.Now()
			if err := e.Save(); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		t.Logf("%5d bans: one atomic save = %v", n, best)

		// 5000 bans is already an extreme case — a server under a
		// sustained botnet scan. Even there a save has to stay in the
		// low milliseconds, or saving per scan would be visible.
		if best > 100*time.Millisecond {
			t.Errorf("%d bans: a save takes %v, which is too slow to do per scan", n, best)
		}
		_ = path
	}
}

// The important one: a scan that bans many addresses must produce ONE save,
// not one per address. Counted by instrumenting the file's modification
// count through a save counter on the engine.
func TestAScanSavesOnceNotPerAddress(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 60}
	_, _ = mgr.Mutate(func(c *config.Config) error { c.AutoBan = ab; return nil })

	old := StatePath
	StatePath = filepath.Join(dir, "bans.json")
	t.Cleanup(func() { StatePath = old })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)

	now := time.Now()
	for i := 0; i < 300; i++ {
		e.record(fmt.Sprintf("203.0.%d.%d", i/256, i%256), ReasonHoneypot, now)
	}

	before := e.saveCount()
	e.evaluate(ab, now)
	e.waitForSave()
	after := e.saveCount()

	if got := len(e.ActiveBans()); got != 300 {
		t.Fatalf("expected 300 bans, got %d", got)
	}
	if n := after - before; n > 1 {
		t.Fatalf("a scan that banned 300 addresses performed %d saves, want 1", n)
	}
	t.Logf("300 addresses banned in one scan = %d save(s)", after-before)
}

// A manual ban must be on disk the instant it returns. The operator who
// clicked the button has every right to assume it stuck.
func TestAManualBanIsOnDiskImmediately(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, _ = mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = config.DefaultAutoBan()
		return nil
	})
	old := StatePath
	StatePath = filepath.Join(dir, "bans.json")
	t.Cleanup(func() { StatePath = old })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)

	start := time.Now()
	if err := e.Ban("203.0.113.90", ReasonManual, time.Hour, false); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// Read it back with a completely separate engine — no shared memory.
	e2 := &Engine{bans: map[string]Ban{}, events: map[string]*counter{},
		cursors: map[string]*logCursor{}, statePath: StatePath}
	if err := e2.Load(); err != nil {
		t.Fatal(err)
	}
	if len(e2.ActiveBans()) != 1 {
		t.Fatal("a manual ban was not on disk when Ban() returned")
	}
	t.Logf("a manual ban reached disk in %v", elapsed)

	// It must not be slow enough to be felt in the UI.
	if elapsed > 200*time.Millisecond {
		t.Errorf("Ban() took %v, which the operator would feel", elapsed)
	}
}

// An unban must also be durable, or a released address comes back banned
// after a reboot — which is worse than the original problem because nobody
// looks for it.
func TestAnUnbanIsDurable(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, _ = mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = config.DefaultAutoBan()
		return nil
	})
	old := StatePath
	StatePath = filepath.Join(dir, "bans.json")
	t.Cleanup(func() { StatePath = old })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)
	_ = e.Ban("203.0.113.91", ReasonManual, time.Hour, false)
	if !e.Unban("203.0.113.91") {
		t.Fatal("unban reported nothing to remove")
	}

	e2 := &Engine{bans: map[string]Ban{}, events: map[string]*counter{},
		cursors: map[string]*logCursor{}, statePath: StatePath}
	_ = e2.Load()
	if len(e2.ActiveBans()) != 0 {
		t.Fatal("a released address came back banned after a restart")
	}
}

func TestUnbanAllIsDurable(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, _ = mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = config.DefaultAutoBan()
		return nil
	})
	old := StatePath
	StatePath = filepath.Join(dir, "bans.json")
	t.Cleanup(func() { StatePath = old })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)
	_ = e.Ban("203.0.113.92", ReasonManual, time.Hour, false)
	_ = e.Ban("203.0.113.93", ReasonManual, time.Hour, false)
	if n := e.UnbanAll(); n != 2 {
		t.Fatalf("UnbanAll removed %d", n)
	}

	e2 := &Engine{bans: map[string]Ban{}, events: map[string]*counter{},
		cursors: map[string]*logCursor{}, statePath: StatePath}
	_ = e2.Load()
	if len(e2.ActiveBans()) != 0 {
		t.Fatal("cleared bans came back after a restart")
	}
}

// Saving must not be done while holding the lock that every recorded request
// contends on: a disk write inside that mutex would stall the scanner under
// load. This asserts it by hammering record() from several goroutines while
// saves are happening and requiring the whole thing to stay fast.
func TestSavingDoesNotBlockRecording(t *testing.T) {
	e, _ := benchEngine(t, 2000)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			_ = e.Save()
		}
		close(done)
	}()

	now := time.Now()
	start := time.Now()
	for i := 0; i < 20000; i++ {
		e.record(fmt.Sprintf("198.51.%d.%d", i/256%256, i%256), ReasonNotFound, now)
	}
	elapsed := time.Since(start)
	<-done

	t.Logf("20,000 records while 20 saves ran concurrently: %v (%v each)",
		elapsed, elapsed/20000)
	// Records cost ~323 ns each when nothing else is happening, and about
	// 520 ns while saves run concurrently. The budget must survive the
	// race detector, which instruments every lock and multiplies these
	// numbers by roughly ten — measured at 5-7 us under -race, so a 5 us
	// ceiling failed there while the code was perfectly correct. That was
	// a test mistake, not a regression.
	budget := 5 * time.Microsecond
	if raceEnabled {
		budget = 40 * time.Microsecond
	}
	if per := elapsed / 20000; per > budget {
		t.Errorf("recording cost %v per event while saving (budget %v) — "+
			"the save is holding the lock", per, budget)
	}
}
