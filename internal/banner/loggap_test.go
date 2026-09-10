package banner

// The ban history that was silently empty.
//
// Reported from a real install: the trap catches an address, it appears in
// "currently banned", and the history stays empty for ever.
//
// The cause is a disagreement between two pieces of code about what an
// UNSET setting means. The API answers the form with LogBans=true when the
// feature has never been configured, because recording what a detector
// catches is the sensible default and nobody thinks to tick a second box.
// The engine, meanwhile, read the raw stored value — which is false. So the
// panel showed the switch ON while nothing was ever written.
//
// The lesson is the one this project keeps relearning: a default that is
// applied in one place and not the other is worse than no default at all.
// The rule now lives in ONE function, config.AutoBan.ShouldLogBans, and
// both sides call it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
)

// A never-configured install must still record its bans.
func TestBansAreRecordedBeforeTheFormIsEverSaved(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	_ = os.Remove(config.ConfigPath)

	mgr := config.New()
	// Exactly the state a fresh install is in: the feature switched on,
	// rules at their defaults, and Configured never set because the
	// operator has not opened the form.
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 60}
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = ab
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	oldState, oldLog := StatePath, BanLogPath
	StatePath = filepath.Join(dir, "bans.json")
	BanLogPath = filepath.Join(dir, "bans.log")
	t.Cleanup(func() { StatePath, BanLogPath = oldState, oldLog })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)

	now := time.Now()
	e.record("203.0.113.77", ReasonHoneypot, now)
	e.evaluate(ab, now)

	if len(e.ActiveBans()) != 1 {
		t.Fatalf("the address was not banned at all: %+v", e.ActiveBans())
	}

	// The write is asynchronous; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		lines = ReadBanLog(10)
		if len(lines) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(lines) == 0 {
		t.Fatal("the ban was applied but NOTHING was written to the history.\n" +
			"The panel shows 'record bans' as on, because the API reports the\n" +
			"recommended default for a never-configured install — but the engine\n" +
			"read the raw stored value, which is false. The switch looks on and\n" +
			"the file is never written.")
	}
	if !strings.Contains(lines[0], "203.0.113.77") {
		t.Fatalf("the wrong event was recorded: %q", lines[0])
	}
}

// ...and an operator who deliberately turned recording OFF must still get
// nothing. The fix must not become "always log".
func TestRecordingStaysOffWhenTheOperatorTurnedItOff(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	_ = os.Remove(config.ConfigPath)

	mgr := config.New()
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 60}
	// Saved once, with recording explicitly off.
	ab.Configured = true
	ab.LogBans = false
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = ab
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	oldState, oldLog := StatePath, BanLogPath
	StatePath = filepath.Join(dir, "bans.json")
	BanLogPath = filepath.Join(dir, "bans.log")
	t.Cleanup(func() { StatePath, BanLogPath = oldState, oldLog })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)

	now := time.Now()
	e.record("203.0.113.78", ReasonHoneypot, now)
	e.evaluate(ab, now)
	time.Sleep(300 * time.Millisecond)

	if got := ReadBanLog(10); len(got) != 0 {
		t.Fatalf("recording was switched off but %d events were written: %v",
			len(got), got)
	}
}

// A manual ban must reach the history too, on the same terms.
func TestAManualBanIsRecordedOnAFreshInstall(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	_ = os.Remove(config.ConfigPath)

	mgr := config.New()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.AutoBan = config.DefaultAutoBan()
		c.AutoBan.Enabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	oldState, oldLog := StatePath, BanLogPath
	StatePath = filepath.Join(dir, "bans.json")
	BanLogPath = filepath.Join(dir, "bans.log")
	t.Cleanup(func() { StatePath, BanLogPath = oldState, oldLog })

	e := New(mgr, filepath.Join(dir, "hp.log"), filepath.Join(dir, "access.log"), nil)
	t.Cleanup(e.waitForSave)

	if err := e.Ban("203.0.113.79", ReasonManual, time.Hour, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(ReadBanLog(10)) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a manual ban on a fresh install was not recorded")
}

// The history must carry a full timestamp, not just a duration. "banned 3
// hours ago" cannot be correlated with anything; an ISO timestamp can be
// matched against nginx's own logs line for line.
func TestTheHistoryCarriesAFullTimestamp(t *testing.T) {
	when := time.Date(2026, 9, 10, 14, 3, 11, 0, time.UTC)
	line := BanEvent{
		When: when, Action: "ban", IP: "203.0.113.5",
		Reason: ReasonHoneypot, Hits: 3, Until: when.Add(4 * time.Hour),
	}.Line()

	if !strings.HasPrefix(line, "2026-09-10T14:03:11Z") {
		t.Fatalf("no full timestamp: %q", line)
	}
	if !strings.Contains(line, `until="2026-09-10T18:03:11Z"`) {
		t.Fatalf("no expiry timestamp: %q", line)
	}
}
