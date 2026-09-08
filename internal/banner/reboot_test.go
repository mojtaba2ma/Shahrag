package banner

// What survives a reboot.
//
// A reboot is not a graceful shutdown. systemd sends SIGTERM and waits, but
// a power cut, an OOM kill, or a `reboot -f` gives the process nothing at
// all — and those are exactly the cases where an operator most wants their
// bans and statistics still to be there afterwards.
//
// So there are two distinct promises to test, and they are different:
//
//	graceful stop  -> everything up to the last second survives
//	hard kill      -> everything up to the last periodic flush survives
//
// The second one is the honest limit. It is bounded by the flush interval,
// and the test asserts that bound rather than pretending the window is zero.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shahrag/internal/config"
)

// rebootFixture builds an engine whose state lives in a temp dir, so a
// "reboot" is just constructing a second engine over the same files.
func rebootFixture(t *testing.T) (*config.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	_ = os.Remove(config.ConfigPath)

	mgr := config.New()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = config.DefaultAutoBan()
		c.AutoBan.Enabled = true
		c.AutoBan.LogBans = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return mgr, dir
}

func newEngineIn(t *testing.T, mgr *config.Manager, dir string) *Engine {
	t.Helper()
	old := StatePath
	StatePath = filepath.Join(dir, "bans.json")
	t.Cleanup(func() { StatePath = old })
	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	// Same reason as newEngine: a background save must not outlive the
	// temp directory it is writing into.
	t.Cleanup(e.waitForSave)
	return e
}

// The basic promise: a ban applied before a restart is still in force after
// it. A ban that does not survive a restart is not a ban — an upgrade would
// hand every scanner a clean slate.
func TestBansSurviveARealRestartThroughNew(t *testing.T) {
	mgr, dir := rebootFixture(t)

	e1 := newEngineIn(t, mgr, dir)
	if err := e1.Ban("203.0.113.10", ReasonHoneypot, 4*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	if err := e1.Ban("203.0.113.11", ReasonManual, 0, true); err != nil {
		t.Fatal(err)
	}
	e1.Stop()

	// "Reboot": a brand-new engine over the same files.
	e2 := newEngineIn(t, mgr, dir)
	bans := e2.ActiveBans()
	if len(bans) != 2 {
		t.Fatalf("after a restart %d of 2 bans survived", len(bans))
	}
	byIP := map[string]Ban{}
	for _, b := range bans {
		byIP[b.IP] = b
	}
	if got := byIP["203.0.113.10"]; got.Reason != ReasonHoneypot {
		t.Errorf("reason lost: %+v", got)
	}
	if !byIP["203.0.113.11"].Permanent {
		t.Error("a permanent ban came back as temporary")
	}
	// And the generated nginx list still contains them, which is what
	// actually enforces the ban.
	ips := e2.BannedIPs(100)
	if len(ips) != 2 {
		t.Fatalf("the nginx list has %d entries after a restart", len(ips))
	}
}

// A ban whose clock ran out WHILE the server was off must not come back.
// Storing an absolute expiry rather than a remaining duration is what makes
// this work; a "remaining 4 hours" field would silently restart the clock on
// every reboot and turn a four-hour ban into a permanent one.
func TestABanThatExpiredWhileTheServerWasOffStaysExpired(t *testing.T) {
	mgr, dir := rebootFixture(t)
	statePath := filepath.Join(dir, "bans.json")

	e1 := newEngineIn(t, mgr, dir)
	_ = e1.Ban("198.51.100.20", ReasonAuthFail, time.Hour, false)
	e1.Stop()

	// Rewrite the saved file so the ban expired an hour ago — the same
	// state the disk would be in after the machine was off overnight.
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st persisted
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	for i := range st.Bans {
		st.Bans[i].BannedAt = time.Now().Add(-3 * time.Hour)
		st.Bans[i].ExpiresAt = time.Now().Add(-1 * time.Hour)
	}
	out, _ := json.Marshal(&st)
	if err := os.WriteFile(statePath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	e2 := newEngineIn(t, mgr, dir)
	if got := e2.ActiveBans(); len(got) != 0 {
		t.Fatalf("a ban that expired while the server was off came back: %+v", got)
	}
	if got := e2.BannedIPs(100); len(got) != 0 {
		t.Fatalf("nginx would still be blocking %v", got)
	}
}

// A hard kill — no SIGTERM, no Stop(), exactly what a power cut does.
// Everything up to the last periodic flush must still be there.
func TestBansSurviveAHardKill(t *testing.T) {
	mgr, dir := rebootFixture(t)

	e1 := newEngineIn(t, mgr, dir)
	_ = e1.Ban("203.0.113.30", ReasonHoneypot, 2*time.Hour, false)
	// Ban() saves? It must, or a crash between the ban and the next
	// five-minute flush loses it — and the window in which a freshly
	// banned attacker is forgotten is precisely when they are attacking.
	// Deliberately NO Stop() here: this is the crash.

	e2 := newEngineIn(t, mgr, dir)
	if got := e2.ActiveBans(); len(got) != 1 {
		t.Fatalf("a ban applied just before a crash was lost (%d survived)\n"+
			"Ban() must persist immediately, not wait for the periodic flush",
			len(got))
	}
}

// The state file must be written atomically. A half-written file after a
// power cut would be worse than no file: the panel would start with a
// corrupt ban list.
func TestAHalfWrittenStateFileIsSurvivable(t *testing.T) {
	mgr, dir := rebootFixture(t)
	statePath := filepath.Join(dir, "bans.json")

	e1 := newEngineIn(t, mgr, dir)
	_ = e1.Ban("203.0.113.40", ReasonHoneypot, time.Hour, false)
	e1.Stop()

	raw, _ := os.ReadFile(statePath)
	// Truncate mid-JSON, which is what a power cut during a non-atomic
	// write would leave behind.
	if err := os.WriteFile(statePath, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}

	// Must not panic, must not refuse to start. Losing the ban list is an
	// inconvenience; refusing to start is an outage.
	e2 := newEngineIn(t, mgr, dir)
	_ = e2.ActiveBans()
	// And it must be able to write a good file over the bad one.
	if err := e2.Ban("203.0.113.41", ReasonManual, time.Hour, false); err != nil {
		t.Fatalf("could not recover after a corrupt state file: %v", err)
	}
	e2.Stop()

	e3 := newEngineIn(t, mgr, dir)
	if len(e3.ActiveBans()) != 1 {
		t.Fatal("the engine did not recover from a corrupt state file")
	}
}

// After a reboot the log files still exist and are full of history. The
// engine must NOT replay them as fresh traffic — that would ban whoever
// happened to be in yesterday's log the moment the server came back.
func TestARestartDoesNotReplayOldLogHistory(t *testing.T) {
	mgr, dir := rebootFixture(t)
	hp := filepath.Join(dir, "hp.log")

	// Yesterday's attack, already in the file before the engine starts.
	var lines string
	for i := 0; i < 20; i++ {
		lines += "2026-09-07T01:00:00+00:00 198.51.100.50 \"GET /.env HTTP/1.1\" ua=\"x\" ref=\"-\"\n"
	}
	if err := os.WriteFile(hp, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	e := newEngineIn(t, mgr, dir)
	e.primeCursors()
	e.Scan()

	if got := e.ActiveBans(); len(got) != 0 {
		t.Fatalf("the engine replayed %d lines of old history and banned %+v", 20, got)
	}
}

// ...but a NEW line appended after the restart must still be caught. The
// previous test would also pass if the engine simply never read anything.
func TestNewTrafficAfterARestartIsStillCaught(t *testing.T) {
	mgr, dir := rebootFixture(t)
	hp := filepath.Join(dir, "hp.log")

	if err := os.WriteFile(hp, []byte(
		"2026-09-07T01:00:00+00:00 198.51.100.60 \"GET /.env HTTP/1.1\" ua=\"x\" ref=\"-\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	e := newEngineIn(t, mgr, dir)
	e.primeCursors()
	e.Scan()
	if len(e.ActiveBans()) != 0 {
		t.Fatal("old history was replayed")
	}

	// Now a real attack arrives. The default honeypot rule is 2 hits.
	f, err := os.OpenFile(hp, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339)
	for i := 0; i < 3; i++ {
		_, _ = f.WriteString(now + " 198.51.100.61 \"GET /.env HTTP/1.1\" ua=\"x\" ref=\"-\"\n")
	}
	f.Close()

	e.Scan()
	bans := e.ActiveBans()
	if len(bans) != 1 || bans[0].IP != "198.51.100.61" {
		t.Fatalf("a fresh attack after a restart was not caught: %+v", bans)
	}
}

// Log ROTATION across a reboot: logrotate runs, the file the cursor points
// at is gone, and a new smaller one is in its place. The engine must notice
// and read the new file from the start rather than seeking past its end.
func TestRotationAcrossARestartIsHandled(t *testing.T) {
	mgr, dir := rebootFixture(t)
	hp := filepath.Join(dir, "hp.log")

	big := ""
	for i := 0; i < 200; i++ {
		big += "2026-09-07T01:00:00+00:00 198.51.100.70 \"GET /.env HTTP/1.1\" ua=\"x\" ref=\"-\"\n"
	}
	if err := os.WriteFile(hp, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	e := newEngineIn(t, mgr, dir)
	e.primeCursors()
	e.Scan()

	// logrotate: move the old file away, start a fresh small one.
	if err := os.Rename(hp, hp+".1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339)
	fresh := ""
	for i := 0; i < 3; i++ {
		fresh += now + " 198.51.100.71 \"GET /.env HTTP/1.1\" ua=\"x\" ref=\"-\"\n"
	}
	if err := os.WriteFile(hp, []byte(fresh), 0o644); err != nil {
		t.Fatal(err)
	}

	e.Scan()
	bans := e.ActiveBans()
	if len(bans) != 1 || bans[0].IP != "198.51.100.71" {
		t.Fatalf("traffic after a rotation was missed: %+v\n"+
			"the cursor was left pointing past the end of the new file", bans)
	}
}

// The state directory may not exist on a fresh machine. Saving must create
// it rather than failing silently and losing every ban.
func TestSavingCreatesItsDirectory(t *testing.T) {
	mgr, dir := rebootFixture(t)
	nested := filepath.Join(dir, "does", "not", "exist")

	old := StatePath
	StatePath = filepath.Join(nested, "bans.json")
	t.Cleanup(func() { StatePath = old })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	if err := e.Ban("203.0.113.80", ReasonManual, time.Hour, false); err != nil {
		t.Fatal(err)
	}
	e.Stop()

	if _, err := os.Stat(StatePath); err != nil {
		t.Fatalf("the state file was not created: %v", err)
	}
}
