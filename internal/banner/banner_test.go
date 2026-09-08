package banner

// Automatic banning.
//
// The dangerous failure here is not "a scanner got through" — it is "a
// customer got banned". So these tests care as much about who is NOT banned
// as about who is.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"shahrag/internal/config"
)

func newEngine(t *testing.T, ab config.AutoBan) (*Engine, string, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = ab
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	hp := filepath.Join(dir, "honeypot.log")
	ac := filepath.Join(dir, "access.log")
	for _, p := range []string{hp, ac} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := New(mgr, hp, ac, nil)
	e.statePath = filepath.Join(dir, "bans.json")
	// Since r47 a ban is persisted in the background, so a save can still
	// be in flight when this test's TempDir is torn down — the engine then
	// writes into a directory being deleted underneath it and Go reports
	// "TempDir RemoveAll cleanup: directory not empty". Intermittent, and
	// it is the TEST that is racing, not the product. Waiting here covers
	// every test using this fixture, including the ones that never call
	// Stop().
	t.Cleanup(e.waitForSave)
	// Establish the cursors, as the running engine does before its first
	// scan, so pre-existing content is not replayed as fresh traffic.
	e.primeCursors()
	return e, hp, ac
}

func appendLine(t *testing.T, p, line string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func hpLine(ip string) string {
	return fmt.Sprintf("%s %s \"GET /.env HTTP/1.1\" ua=\"curl\" ref=\"-\"",
		time.Now().Format(time.RFC3339), ip)
}

func accessLine(ip string, status int) string {
	return fmt.Sprintf(`%s - - [07/Sep/2026:12:00:00 +0000] "GET /x HTTP/1.1" %d 0`,
		ip, status)
}

func banned(e *Engine, ip string) bool {
	for _, b := range e.ActiveBans() {
		if b.IP == ip {
			return true
		}
	}
	return false
}

// Off means off: nothing is banned however much the logs scream.
func TestDisabledEngineBansNothing(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = false
	e, hp, _ := newEngine(t, ab)
	for i := 0; i < 50; i++ {
		appendLine(t, hp, hpLine("203.0.113.9"))
	}
	e.Scan()
	if len(e.ActiveBans()) != 0 {
		t.Error("a disabled engine banned somebody")
	}
}

// A scanner tripping the honeypot repeatedly gets banned.
func TestScannerTrippingTheTrapIsBanned(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 3, WindowMinutes: 60, BanMinutes: 30}
	e, hp, _ := newEngine(t, ab)

	appendLine(t, hp, hpLine("203.0.113.9"))
	appendLine(t, hp, hpLine("203.0.113.9"))
	e.Scan()
	if banned(e, "203.0.113.9") {
		t.Fatal("banned after 2 hits with a threshold of 3")
	}
	appendLine(t, hp, hpLine("203.0.113.9"))
	e.Scan()
	if !banned(e, "203.0.113.9") {
		t.Fatal("not banned after reaching the threshold")
	}

	b := e.ActiveBans()[0]
	if b.Reason != ReasonHoneypot {
		t.Errorf("wrong reason recorded: %q", b.Reason)
	}
	if b.Hits < 3 {
		t.Errorf("recorded %d hits, expected at least 3", b.Hits)
	}
	if b.Permanent {
		t.Error("a 30-minute ban was recorded as permanent")
	}
	if d := b.Remaining(time.Now()); d < 25*time.Minute || d > 31*time.Minute {
		t.Errorf("ban lasts %s, expected about 30 minutes", d)
	}
}

// Repeated failed logins get banned; the count is per address.
func TestBruteForceLoginIsBanned(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.AuthFail = config.AutoBanRule{Enabled: true, Hits: 4, WindowMinutes: 10, BanMinutes: 15}
	e, _, _ := newEngine(t, ab)

	for i := 0; i < 3; i++ {
		e.RecordAuthFailure("198.51.100.7")
	}
	if banned(e, "198.51.100.7") {
		t.Fatal("banned before reaching the threshold")
	}
	// A different address must not contribute to the first one's count.
	for i := 0; i < 5; i++ {
		e.RecordAuthFailure("198.51.100.8")
	}
	if banned(e, "198.51.100.7") {
		t.Error("one address's failures banned a different address")
	}
	if !banned(e, "198.51.100.8") {
		t.Error("the address that did cross the threshold was not banned")
	}
	e.RecordAuthFailure("198.51.100.7")
	if !banned(e, "198.51.100.7") {
		t.Error("not banned after the fourth failure")
	}
}

// A directory scanner walking a wordlist produces a flood of 404s.
func TestDirectoryScannerIsBannedOn404Flood(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.NotFound = config.AutoBanRule{Enabled: true, Hits: 20, WindowMinutes: 5, BanMinutes: 30}
	e, _, ac := newEngine(t, ab)

	for i := 0; i < 19; i++ {
		appendLine(t, ac, accessLine("203.0.113.50", 404))
	}
	e.Scan()
	if banned(e, "203.0.113.50") {
		t.Fatal("banned one hit early")
	}
	appendLine(t, ac, accessLine("203.0.113.50", 404))
	e.Scan()
	if !banned(e, "203.0.113.50") {
		t.Fatal("a 404 flood was not banned")
	}
}

// The property that matters most: ordinary traffic is never banned.
func TestOrdinaryTrafficIsNeverBanned(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.NotFound = config.AutoBanRule{Enabled: true, Hits: 20, WindowMinutes: 5, BanMinutes: 30}
	ab.ErrorRate = config.AutoBanRule{Enabled: true, Hits: 40, WindowMinutes: 5, BanMinutes: 30}
	e, _, ac := newEngine(t, ab)

	// A busy, perfectly normal visitor: hundreds of successful requests
	// and a handful of missing images.
	for i := 0; i < 300; i++ {
		appendLine(t, ac, accessLine("192.0.2.15", 200))
	}
	for i := 0; i < 5; i++ {
		appendLine(t, ac, accessLine("192.0.2.15", 404))
	}
	for i := 0; i < 3; i++ {
		appendLine(t, ac, accessLine("192.0.2.15", 301))
	}
	e.Scan()
	if banned(e, "192.0.2.15") {
		t.Error("a normal visitor was banned — this is the failure that " +
			"actually costs users")
	}
}

// An exempt address is never banned, whatever it does.
func TestAllowListedAddressIsNeverBanned(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 30}
	ab.AllowIPs = []string{"203.0.113.0/24", "198.51.100.5"}
	e, hp, _ := newEngine(t, ab)

	for _, ip := range []string{"203.0.113.9", "203.0.113.200", "198.51.100.5"} {
		for i := 0; i < 10; i++ {
			appendLine(t, hp, hpLine(ip))
		}
	}
	appendLine(t, hp, hpLine("203.0.114.1")) // just outside the range
	e.Scan()

	for _, ip := range []string{"203.0.113.9", "203.0.113.200", "198.51.100.5"} {
		if banned(e, ip) {
			t.Errorf("%s is on the allow list but was banned", ip)
		}
	}
	if !banned(e, "203.0.114.1") {
		t.Error("an address outside the allowed range should still be banned")
	}
}

// Loopback can never be banned: locking the server out of itself would be
// unrecoverable from the panel.
func TestLoopbackCanNeverBeBanned(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 30}
	e, hp, _ := newEngine(t, ab)
	for i := 0; i < 20; i++ {
		appendLine(t, hp, hpLine("127.0.0.1"))
	}
	e.Scan()
	if banned(e, "127.0.0.1") {
		t.Fatal("the server banned itself")
	}
}

// Offences outside the window do not count, or a slow trickle over weeks
// would eventually ban an innocent address.
func TestOffencesExpireOutOfTheWindow(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 3, WindowMinutes: 10, BanMinutes: 30}
	e, _, _ := newEngine(t, ab)

	old := time.Now().Add(-30 * time.Minute)
	e.record("203.0.113.77", ReasonHoneypot, old)
	e.record("203.0.113.77", ReasonHoneypot, old)

	e.record("203.0.113.77", ReasonHoneypot, time.Now())
	e.evaluate(ab, time.Now())
	if banned(e, "203.0.113.77") {
		t.Error("two offences from half an hour ago counted inside a 10-minute window")
	}
}

// A ban lapses on its own, and the address starts clean.
func TestBanExpiresAndHistoryIsCleared(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 1}
	e, _, _ := newEngine(t, ab)

	e.record("203.0.113.5", ReasonHoneypot, time.Now())
	e.evaluate(ab, time.Now())
	if !banned(e, "203.0.113.5") {
		t.Fatal("setup: not banned")
	}

	// Two minutes later the ban is gone...
	later := time.Now().Add(2 * time.Minute)
	e.mu.Lock()
	e.pruneLocked(later, ab)
	e.mu.Unlock()
	if banned(e, "203.0.113.5") {
		t.Error("the ban outlived its expiry")
	}
	// ...and so is the history, or the old offence would re-ban it at once.
	e.mu.RLock()
	_, still := e.events["203.0.113.5"]
	e.mu.RUnlock()
	if still {
		t.Error("stale offences survived the ban, which would re-ban immediately")
	}
}

// A permanent ban does not expire.
func TestPermanentBanDoesNotExpire(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: -1}
	e, _, _ := newEngine(t, ab)

	e.record("203.0.113.6", ReasonHoneypot, time.Now())
	e.evaluate(ab, time.Now())
	b := e.ActiveBans()
	if len(b) != 1 || !b[0].Permanent {
		t.Fatalf("expected one permanent ban, got %+v", b)
	}
	if !b[0].Active(time.Now().AddDate(5, 0, 0)) {
		t.Error("a permanent ban expired within five years")
	}
}

// Bans survive a restart. One that does not is not a ban: an upgrade would
// hand every scanner a clean slate.
func TestBansSurviveARestart(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 60}
	e, _, _ := newEngine(t, ab)

	e.record("203.0.113.8", ReasonHoneypot, time.Now())
	e.evaluate(ab, time.Now())
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}

	e2 := &Engine{
		bans: map[string]Ban{}, events: map[string]*counter{},
		cursors: map[string]*logCursor{}, statePath: e.statePath,
	}
	if err := e2.Load(); err != nil {
		t.Fatal(err)
	}
	if !banned(e2, "203.0.113.8") {
		t.Error("the ban did not survive a restart")
	}
	// An expired ban must not come back.
	e3 := &Engine{
		bans: map[string]Ban{}, events: map[string]*counter{},
		cursors: map[string]*logCursor{}, statePath: e.statePath,
	}
	e.mu.Lock()
	b := e.bans["203.0.113.8"]
	b.ExpiresAt = time.Now().Add(-time.Hour)
	e.bans["203.0.113.8"] = b
	e.mu.Unlock()
	// Save skips inactive bans, so the file should now be empty.
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}
	if err := e3.Load(); err != nil {
		t.Fatal(err)
	}
	if len(e3.ActiveBans()) != 0 {
		t.Error("an expired ban was restored from disk")
	}
}

// The log must be read incrementally. Re-reading it would re-count every
// historical offence on every pass, which for a ban engine means one old
// scan bans its author forever, every 15 seconds.
func TestLogsAreReadIncrementally(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 5, WindowMinutes: 60, BanMinutes: 30}
	e, hp, _ := newEngine(t, ab)

	for i := 0; i < 3; i++ {
		appendLine(t, hp, hpLine("203.0.113.20"))
	}
	e.Scan()
	// Five more scans with nothing new must not accumulate anything.
	for i := 0; i < 5; i++ {
		e.Scan()
	}
	if banned(e, "203.0.113.20") {
		t.Fatal("3 offences became 5 or more because the log was re-read")
	}
	e.mu.RLock()
	c := e.events["203.0.113.20"]
	n := 0
	if c != nil {
		n = int(c.value(time.Now().Unix(), 0, 3600) + 0.5)
	}
	e.mu.RUnlock()
	if n != 3 {
		t.Errorf("recorded %d offences from 3 log lines", n)
	}
}

// The honeypot log does not exist until something first trips the trap, so
// the engine almost always starts before the file does.
//
// Found live: 4 bait hits against a threshold of 3 produced no ban at all.
// primeCursors skipped a file it could not stat, so readNew met it for the
// first time later and applied its "start at the end, never replay history"
// rule — which silently discarded every line already written. On a real
// server that means the first attack after any restart is invisible.
func TestLogCreatedAfterStartupIsReadFromTheBeginning(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 3, WindowMinutes: 60, BanMinutes: 60}
	e, hp, _ := newEngine(t, ab)

	// The state a real server is in: the trap has never fired, so nginx
	// has not created its log yet.
	if err := os.Remove(hp); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.cursors = map[string]*logCursor{}
	e.mu.Unlock()
	e.primeCursors()

	// Now an attacker arrives and nginx creates the file.
	f, err := os.Create(hp)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := f.WriteString(hpLine("203.0.113.99") + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	e.Scan()
	if !banned(e, "203.0.113.99") {
		t.Error("an attack written to a log created after startup was never " +
			"seen — the first attack after any restart would be invisible")
	}
}

// The opposite must still hold: an EXISTING log is not replayed, or a
// restart would re-ban everyone in it.
func TestExistingLogIsNotReplayedOnStartup(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 2, WindowMinutes: 60, BanMinutes: 60}
	e, hp, _ := newEngine(t, ab)

	// History already in the file before the engine looks at it.
	for i := 0; i < 10; i++ {
		appendLine(t, hp, hpLine("203.0.113.50"))
	}
	e.mu.Lock()
	e.cursors = map[string]*logCursor{}
	e.mu.Unlock()
	e.primeCursors()

	e.Scan()
	if banned(e, "203.0.113.50") {
		t.Error("an old scan already in the log was replayed as fresh traffic")
	}
}

// logrotate renames the file; reading must continue in the new one.
func TestLogRotationIsSurvived(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 2, WindowMinutes: 60, BanMinutes: 30}
	e, hp, _ := newEngine(t, ab)

	appendLine(t, hp, hpLine("203.0.113.30"))
	e.Scan()
	if err := os.Rename(hp, hp+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	appendLine(t, hp, hpLine("203.0.113.30"))
	e.Scan()
	if !banned(e, "203.0.113.30") {
		t.Error("the line written after rotation was never read")
	}
}

// Manual control has to work, including releasing somebody.
func TestManualBanAndUnban(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	e, _, _ := newEngine(t, ab)

	if err := e.Ban("not-an-ip", "", time.Hour, false); err == nil {
		t.Error("a malformed address was accepted")
	}
	if err := e.Ban("203.0.113.40", "", time.Hour, false); err != nil {
		t.Fatal(err)
	}
	if !banned(e, "203.0.113.40") {
		t.Fatal("the manual ban did not take")
	}
	if e.ActiveBans()[0].Reason != ReasonManual {
		t.Error("a manual ban was not labelled as such")
	}
	if !e.Unban("203.0.113.40") {
		t.Error("unban reported no such ban")
	}
	if banned(e, "203.0.113.40") {
		t.Error("the address is still banned after being released")
	}
	// Releasing must also clear the history, or the next scan re-bans it.
	e.mu.RLock()
	_, left := e.events["203.0.113.40"]
	e.mu.RUnlock()
	if left {
		t.Error("releasing an address left its offences behind")
	}
}

// The ban list feeds the generator, so it must be sorted and capped.
func TestBannedIPsAreSortedAndCapped(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	e, _, _ := newEngine(t, ab)
	for _, ip := range []string{"203.0.113.30", "203.0.113.10", "203.0.113.20"} {
		if err := e.Ban(ip, "", time.Hour, false); err != nil {
			t.Fatal(err)
		}
	}
	got := e.BannedIPs(0)
	want := []string{"203.0.113.10", "203.0.113.20", "203.0.113.30"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
	if n := len(e.BannedIPs(2)); n != 2 {
		t.Errorf("the cap was ignored: %d entries", n)
	}
}

// The engine is read by the HTTP handlers while the watcher writes to it.
func TestConcurrentUseIsSafe(t *testing.T) {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 2, WindowMinutes: 60, BanMinutes: 30}
	e, hp, _ := newEngine(t, ab)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				appendLine(t, hp, hpLine(fmt.Sprintf("203.0.113.%d", n+1)))
				e.Scan()
				_ = e.ActiveBans()
				_ = e.BannedIPs(100)
				_ = e.Pending()
			}
		}(i)
	}
	wg.Wait()
}
