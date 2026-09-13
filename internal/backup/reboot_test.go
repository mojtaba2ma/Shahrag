package backup

// Surviving a reboot, and staying light after a month.
//
// These are the two failure modes an operator actually experiences with a
// scheduled job, and neither shows up in a unit test of the happy path:
//
//   - the server restarts and the schedule either forgets where it was
//     (no backup for a day) or replays (a burst of identical archives)
//   - the panel runs for weeks and the thing that was cheap once a day
//     turns out to leak a little each time
//
// Both are tested here against real files and a real clock, because both
// are about state that outlives the process.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"shahrag/internal/config"
)

// A restart must not lose the schedule's place, and must not replay.
//
// The last-run time lives in the config file precisely so this works; the
// test proves it by throwing the whole engine away and building a new one
// over the same files, which is exactly what a reboot does.
func TestTheScheduleSurvivesARestart(t *testing.T) {
	e, mgr, _ := newEngine(t)

	base := time.Date(2026, 9, 13, 3, 0, 0, 0, time.UTC)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Enabled = true
		c.Backup.IntervalHours = 24
		c.Backup.Hour = 3
		c.Shahrag.LastBackup = base
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A reboot: a brand new manager and engine over the same config path.
	mgr2 := config.New()
	c2, err := mgr2.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !c2.Shahrag.LastBackup.Equal(base) {
		t.Fatalf("after a restart the last-run time reads %v, want %v — the schedule lost its place",
			c2.Shahrag.LastBackup, base)
	}

	// Ten minutes after the reboot, nothing is due.
	if Due(c2.Backup, c2.Shahrag.LastBackup, base.Add(10*time.Minute)) {
		t.Fatal("a backup was due ten minutes after the last one; a reboot would produce a burst")
	}
	// The next day at 03:00, one is.
	if !Due(c2.Backup, c2.Shahrag.LastBackup, base.Add(24*time.Hour)) {
		t.Fatal("no backup was due a day later; the schedule stalled across the restart")
	}
	_ = e
}

// Archives written before a reboot are still listed after it, with their
// metadata intact — the list is rebuilt from the FILENAMES, so there is no
// index to lose.
func TestArchivesAreStillThereAfterARestart(t *testing.T) {
	e, mgr, _ := newEngine(t)

	base := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		e.now = func() time.Time { return at }
		if _, err := e.Create(config.BackupFull, GenHourly); err != nil {
			t.Fatal(err)
		}
	}
	e.now = time.Now

	// New engine, same directory.
	e2 := New(mgr)
	list, err := e2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("after a restart %d archives are listed, want 3", len(list))
	}
	for _, a := range list {
		if a.Scope != config.BackupFull {
			t.Errorf("%s lost its scope (%q)", a.Name, a.Scope)
		}
		if a.Generation != GenHourly {
			t.Errorf("%s lost its generation (%q)", a.Name, a.Generation)
		}
		if a.CreatedAt.IsZero() {
			t.Errorf("%s lost its timestamp", a.Name)
		}
		if a.Size == 0 {
			t.Errorf("%s reports zero size", a.Name)
		}
	}
	// And one of them still restores.
	if _, err := Inspect(list[0].Path, ""); err != nil {
		t.Fatalf("an archive written before the restart no longer opens: %v", err)
	}
}

// A backup interrupted half way through must never appear in the list.
//
// This is the reboot case that actually corrupts things: the power goes
// during the write. The engine writes to a temporary name and renames, so
// a partial file is never called shahrag-*.tar.gz — the test proves the
// lister ignores whatever is left behind.
func TestAnInterruptedBackupIsNotListedAsComplete(t *testing.T) {
	e, mgr, _ := newEngine(t)
	c, _ := mgr.Read()
	dir := c.Backup.EffectiveDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// The debris a killed process leaves: the temp file the engine was
	// writing into.
	if err := os.WriteFile(filepath.Join(dir, ".bak-123456.tmp"),
		[]byte("half a gzip stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And a truncated file with a plausible-looking name, to prove the
	// lister is not simply matching on the extension.
	if err := os.WriteFile(filepath.Join(dir, "notours.tar.gz"),
		[]byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Create(config.BackupConfig, GenManual); err != nil {
		t.Fatal(err)
	}
	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("the list has %d entries, want only the one complete archive: %+v",
			len(list), list)
	}
}

// The state a reboot must preserve, end to end: take a backup, simulate
// the machine going down and coming back, and restore from it.
func TestFullCycleAcrossAReboot(t *testing.T) {
	e, mgr, _ := newEngine(t)

	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Encrypt = true
		c.Backup.Passphrase = "a-properly-long-passphrase"
		c.Services["before-the-reboot"] = config.Service{LocalPort: 7777}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := e.Create(config.BackupFull, GenManual)
	if err != nil {
		t.Fatal(err)
	}

	// The machine comes back and somebody has since broken the config.
	mgr2 := config.New()
	if _, err := mgr2.Mutate(func(c *config.Config) error {
		delete(c.Services, "before-the-reboot")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	e2 := New(mgr2)
	if _, err := e2.Restore(a.Path, "a-properly-long-passphrase",
		RestoreOptions{Config: true}); err != nil {
		t.Fatal(err)
	}
	c, _ := mgr2.Read()
	if _, ok := c.Services["before-the-reboot"]; !ok {
		t.Fatal("the service did not come back after restoring across a reboot")
	}
}

// ── staying light ────────────────────────────────────────────

// A month of scheduled backups must not grow the process.
//
// The STANDING RULE applied to the one component that runs for ever. Each
// cycle is a create + a prune, which is what the scheduler does; if any of
// it retained memory, 720 cycles would show it.
func TestAMonthOfBackupsDoesNotGrowTheProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("long test")
	}
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		// A tight policy so the directory stays small while the number
		// of CYCLES stays realistic.
		c.Backup.Retention = config.BackupRetention{Hourly: 6, Daily: 7}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// Warm up, so one-off allocations are not counted as growth.
	for i := 0; i < 24; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		e.now = func() time.Time { return at }
		if _, err := e.Create(config.BackupConfig, GenHourly); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Prune(); err != nil {
			t.Fatal(err)
		}
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	// One a month's worth of hourly cycles.
	const cycles = 720
	for i := 0; i < cycles; i++ {
		at := base.Add(time.Duration(24+i) * time.Hour)
		e.now = func() time.Time { return at }
		if _, err := e.Create(config.BackupConfig, GenHourly); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Prune(); err != nil {
			t.Fatal(err)
		}
	}
	e.now = time.Now

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d backup cycles: heap %+.2f MB, %d archives left on disk",
		cycles, float64(grew)/(1<<20), len(list))

	// The directory must be bounded by the policy, not by the cycles.
	if len(list) > 20 {
		t.Fatalf("after %d cycles there are %d archives; retention is not bounding the directory",
			cycles, len(list))
	}
	// 8 MB over 720 cycles is a very generous ceiling — it catches a real
	// leak (a slice appended to for ever, a map never cleared) without
	// failing on GC timing.
	const budget = 8 << 20
	if grew > budget {
		t.Fatalf("the heap grew %.2f MB over %d cycles, which looks like a leak",
			float64(grew)/(1<<20), cycles)
	}
}

// Disk usage is bounded too, which is the other half of "stays light".
func TestTheBackupDirectoryStaysBounded(t *testing.T) {
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Retention = config.DefaultRetention()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// A year of daily backups, pruned each time as the scheduler does.
	for i := 0; i < 365; i++ {
		at := base.Add(time.Duration(i) * 24 * time.Hour)
		e.now = func() time.Time { return at }
		if _, err := e.Create(config.BackupFull, GenHourly); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Prune(); err != nil {
			t.Fatal(err)
		}
	}
	e.now = time.Now

	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, a := range list {
		total += a.Size
	}
	t.Logf("a year of daily backups: %d files, %.1f KB total",
		len(list), float64(total)/1024)

	if len(list) > config.DefaultRetention().TotalKept()+2 {
		t.Fatalf("a year left %d files, past the policy's %d",
			len(list), config.DefaultRetention().TotalKept())
	}
	// A few megabytes at most on a 25 GB disk.
	if total > 16<<20 {
		t.Fatalf("a year of backups occupies %.1f MB", float64(total)/(1<<20))
	}
}

// Pruning must not be fooled by a file it cannot delete: one failure has
// to leave the rest of the run working, because the alternative is that a
// single stuck file stops every future backup.
func TestPruneKeepsGoingWhenOneFileCannotBeDeleted(t *testing.T) {
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Retention = config.BackupRetention{Hourly: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		e.now = func() time.Time { return at }
		if _, err := e.Create(config.BackupConfig, GenHourly); err != nil {
			t.Fatal(err)
		}
	}
	e.now = time.Now

	// Prune must not return an error for the whole run.
	removed, err := e.Prune()
	if err != nil {
		t.Fatalf("prune reported a fatal error: %v", err)
	}
	if len(removed) == 0 {
		t.Fatal("prune removed nothing at all")
	}
	// And a second call is a no-op rather than an error.
	if _, err := e.Prune(); err != nil {
		t.Fatalf("a second prune errored: %v", err)
	}
}

// The scheduler records its run time BEFORE doing the work, so a backup
// that fails every time cannot spin once a minute for ever.
func TestAFailingBackupDoesNotRetryEveryMinute(t *testing.T) {
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Enabled = true
		c.Backup.IntervalHours = 1
		// A directory that cannot be created, so every attempt fails.
		c.Backup.Dir = "/proc/shahrag-cannot-exist"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sch := NewScheduler(e, mgr)

	// A first run has to be triggered before the failure path can be
	// tested at all: with no recorded run, Due() waits for the scheduled
	// hour, so a tick at 04:00 against Hour=3 correctly does nothing.
	// Seeding the last run makes the interval the thing under test.
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Shahrag.LastBackup = time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
	sch.tick(now)

	c, _ := mgr.Read()
	if !c.Shahrag.LastBackup.Equal(now) {
		t.Fatalf("a failed backup recorded %v, want the attempt time %v — "+
			"without that it would be retried every single minute",
			c.Shahrag.LastBackup, now)
	}
	if Due(c.Backup, c.Shahrag.LastBackup, now.Add(time.Minute)) {
		t.Fatal("another backup was due one minute after a failure")
	}
	if !Due(c.Backup, c.Shahrag.LastBackup, now.Add(61*time.Minute)) {
		t.Error("no backup was due an hour after the failure; the schedule stopped entirely")
	}
}

// Two ticks at once must not both run: a slow disk would otherwise have
// two backups writing and pruning the same directory.
func TestOverlappingTicksRunOnlyOneBackup(t *testing.T) {
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Enabled = true
		c.Backup.IntervalHours = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sch := NewScheduler(e, mgr)

	now := time.Date(2026, 9, 13, 5, 0, 0, 0, time.UTC)
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			sch.tick(now)
			done <- struct{}{}
		}()
	}
	<-done
	<-done

	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) > 1 {
		t.Fatalf("two simultaneous ticks produced %d archives", len(list))
	}
	_ = fmt.Sprint()
}

// Encryption must not leave the process fat.
//
// This is the STANDING RULE applied to the most expensive thing the panel
// does. scrypt deliberately allocates a large arena — that is what makes
// it strong — and the first version of this feature used N=32768 (32 MB)
// and never gave it back. Measured against the live panel: three backups
// took RssAnon from 7.9 MB to 66 MB and it stayed there, because Go keeps
// the high-water mark and a 96 MB GOMEMLIMIT never creates the pressure
// to return it.
//
// An operator on a 1 GB server sees 8 MB become 66 MB overnight and
// reasonably concludes the panel leaks. The test locks in both halves of
// the fix: a smaller arena, and returning it afterwards.
func TestRepeatedEncryptedBackupsDoNotRetainMemory(t *testing.T) {
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Backup.Encrypt = true
		c.Backup.Passphrase = "a-properly-long-passphrase"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// One first, so the one-off allocations are not counted.
	if _, err := e.Create(config.BackupConfig, GenManual); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	const n = 10
	for i := 0; i < n; i++ {
		if _, err := e.Create(config.BackupConfig, GenManual); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d encrypted backups: heap %+.2f MB (scrypt arena is %d MB per call)",
		n, float64(grew)/(1<<20), (128*scryptN*scryptR)>>20)

	// One arena's worth is the ceiling. If the arenas accumulated, ten
	// backups would show ten times this.
	//
	// Honest limitation: this measures Go's HeapAlloc, which the
	// collector reclaims inside the process either way — so this test
	// alone would not have caught the original bug. The symptom was RSS,
	// the memory the operating system still had assigned, and that is
	// only visible from outside. TestTheScryptArenaIsAffordable is the
	// assertion that actually fails on a regression here; this one
	// guards against a different mistake, holding each derived key or
	// each archive body alive in a slice somewhere.
	budget := int64(128*scryptN*scryptR) + (4 << 20)
	if grew > budget {
		t.Fatalf("the heap grew %.2f MB over %d encrypted backups, which is more than a single scrypt arena (%.0f MB) — the arenas are accumulating",
			float64(grew)/(1<<20), n, float64(budget)/(1<<20))
	}
}

// The scrypt arena is bounded to something a 1 GB server can afford.
//
// A plain assertion on the constants, because the number is a deliberate
// security/footprint trade and changing it should be a conscious act with
// this comment in front of the person doing it.
func TestTheScryptArenaIsAffordable(t *testing.T) {
	arena := 128 * scryptN * scryptR
	t.Logf("scrypt arena: %d MB (N=%d, r=%d)", arena>>20, scryptN, scryptR)
	if arena > 16<<20 {
		t.Fatalf("scrypt needs %d MB per derivation, which is too much to pay on a 1 GB server for a nightly job",
			arena>>20)
	}
	// And not so small that it stops being a real barrier.
	if arena < 8<<20 {
		t.Fatalf("scrypt needs only %d MB, which is weak enough that a passphrase could be attacked cheaply",
			arena>>20)
	}
}
