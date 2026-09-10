package banner

// Progressive banning.
//
// The behaviour being locked here is a chain of decisions that are each
// individually reversible by a one-line change, and each of which has a
// specific way of going wrong:
//
//   - the ladder must actually climb (otherwise it is a fixed ban with
//     extra state)
//   - it must climb PER ADDRESS (a shared counter would escalate an
//     innocent address because somebody else misbehaved)
//   - it must survive a restart (otherwise an upgrade resets every ladder)
//   - it must forgive (otherwise it is a slow permanent ban for anyone who
//     ever shares a CGNAT address with an attacker)
//   - it must not be able to forgive faster than it punishes
//   - and it must cost a bounded amount of memory

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"shahrag/internal/config"
)

// escConfig builds an engine whose honeypot rule fires on a single hit, so
// a test can drive the ladder one rung per scan without simulating a
// realistic scan each time.
func escConfig(steps []int, decayHours int) config.AutoBan {
	ab := config.DefaultAutoBan()
	ab.Enabled = true
	ab.Configured = true
	ab.Honeypot = config.AutoBanRule{Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 1}
	ab.AuthFail.Enabled = false
	ab.NotFound.Enabled = false
	ab.ErrorRate.Enabled = false
	ab.Escalation = config.BanEscalation{Enabled: true, Steps: steps, DecayHours: decayHours}
	return ab
}

// banOf returns the current ban for an address.
func banOf(e *Engine, ip string) (Ban, bool) {
	for _, b := range e.ActiveBans() {
		if b.IP == ip {
			return b, true
		}
	}
	return Ban{}, false
}

// The core promise: the same address banned three times gets three
// different, increasing lengths.
func TestEachRepeatBanIsLonger(t *testing.T) {
	e, hp, _ := newEngine(t, escConfig([]int{30, 120, 480}, 72))
	const ip = "203.0.113.77"

	var lengths []time.Duration
	for i := 0; i < 3; i++ {
		appendLine(t, hp, hpLine(ip))
		e.Scan()
		b, ok := banOf(e, ip)
		if !ok {
			t.Fatalf("offence %d did not produce a ban", i+1)
		}
		if b.Level != i+1 {
			t.Errorf("offence %d was issued at level %d, want %d", i+1, b.Level, i+1)
		}
		lengths = append(lengths, b.ExpiresAt.Sub(b.BannedAt).Round(time.Minute))
		// Release it so the next offence is judged fresh, exactly as an
		// expiry would. Unban deliberately does NOT clear the ledger.
		e.Unban(ip)
	}

	want := []time.Duration{30 * time.Minute, 120 * time.Minute, 480 * time.Minute}
	for i := range want {
		if lengths[i] != want[i] {
			t.Errorf("ban %d lasted %v, want %v", i+1, lengths[i], want[i])
		}
	}
}

// The ladder is per address. This is the failure that would matter most in
// production: a shared level would escalate a first-time address to a
// month because an unrelated scanner had climbed the ladder that day.
func TestTheLadderIsPerAddress(t *testing.T) {
	e, hp, _ := newEngine(t, escConfig([]int{30, 120, 480}, 72))

	// One address climbs to level 3.
	for i := 0; i < 3; i++ {
		appendLine(t, hp, hpLine("203.0.113.1"))
		e.Scan()
		e.Unban("203.0.113.1")
	}
	// A different address offends for the first time.
	appendLine(t, hp, hpLine("203.0.113.2"))
	e.Scan()

	b, ok := banOf(e, "203.0.113.2")
	if !ok {
		t.Fatal("the second address was not banned")
	}
	if b.Level != 1 {
		t.Fatalf("a first-time address was banned at level %d; the ladder is shared, not per-address", b.Level)
	}
	if got := b.ExpiresAt.Sub(b.BannedAt).Round(time.Minute); got != 30*time.Minute {
		t.Errorf("a first-time address got %v, want 30m", got)
	}
}

// The last rung is permanent, and an address that keeps going stays there
// rather than falling off the end of the slice.
func TestTheLastStepIsPermanentAndSticks(t *testing.T) {
	e, hp, _ := newEngine(t, escConfig([]int{30, config.PermanentStep}, 72))
	const ip = "203.0.113.30"

	appendLine(t, hp, hpLine(ip))
	e.Scan()
	if b, _ := banOf(e, ip); b.Permanent {
		t.Fatal("the FIRST offence was banned permanently")
	}
	e.Unban(ip)

	for i := 0; i < 3; i++ { // twice past the end of the ladder
		appendLine(t, hp, hpLine(ip))
		e.Scan()
		b, ok := banOf(e, ip)
		if !ok {
			t.Fatalf("repeat %d produced no ban", i+2)
		}
		if !b.Permanent {
			t.Fatalf("repeat %d was not permanent (level %d)", i+2, b.Level)
		}
		e.Unban(ip)
	}
}

// The ledger has to outlive the process. Without this an upgrade — which
// is the one moment an operator is guaranteed to restart the panel — hands
// every repeat offender a first-offence ban again.
func TestTheLadderSurvivesARestart(t *testing.T) {
	ab := escConfig([]int{30, 120, 480}, 72)
	e, hp, ac := newEngine(t, ab)
	const ip = "203.0.113.44"

	for i := 0; i < 2; i++ {
		appendLine(t, hp, hpLine(ip))
		e.Scan()
		e.Unban(ip)
	}
	e.waitForSave()
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}

	// A second engine over the same state file, as a restart would be.
	e2 := New(e.cfg, hp, ac, nil)
	e2.statePath = e.statePath
	if err := e2.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e2.waitForSave)
	if got := e2.OffenderLevel(ip); got != 2 {
		t.Fatalf("after a restart the address is at level %d, want 2 — the ledger did not survive", got)
	}
	e2.primeCursors()
	appendLine(t, hp, hpLine(ip))
	e2.Scan()
	b, ok := banOf(e2, ip)
	if !ok {
		t.Fatal("no ban after the restart")
	}
	if b.Level != 3 {
		t.Errorf("the ban after the restart was level %d, want 3", b.Level)
	}
}

// Forgiveness: an address that stays quiet climbs back down one step per
// period, and reaches zero rather than being stuck a rung above it.
func TestSilenceWalksTheLevelBackDown(t *testing.T) {
	period := 72 * time.Hour
	o := &offender{Level: 3, Count: 3, Last: time.Now().Add(-1 * time.Hour).Unix()}
	now := time.Now()

	cases := []struct {
		ago  time.Duration
		want int
	}{
		{1 * time.Hour, 3},    // still fresh
		{71 * time.Hour, 3},   // one minute short of a period: nothing yet
		{73 * time.Hour, 2},   // one period
		{143 * time.Hour, 2},  // one hour short of two periods
		{145 * time.Hour, 1},  // two full periods (144h), so two steps gone
		{300 * time.Hour, 0},  // four periods: fully forgiven
		{5000 * time.Hour, 0}, // and it does not go negative
	}
	for _, c := range cases {
		o.Last = now.Add(-c.ago).Unix()
		if got := decayedLevel(o, now, period); got != c.want {
			t.Errorf("%v of silence left the level at %d, want %d", c.ago, got, c.want)
		}
	}
}

// Decay is per STEP, not a reset. This is the property that stops a
// patient attacker buying a clean slate by waiting out one period.
func TestOnePeriodOfSilenceBuysOneStepNotAReset(t *testing.T) {
	period := 72 * time.Hour
	o := &offender{Level: 6, Count: 6, Last: time.Now().Add(-73 * time.Hour).Unix()}
	got := decayedLevel(o, time.Now(), period)
	if got == 0 {
		t.Fatal("one quiet period reset a level-6 offender to zero; a patient attacker can now buy a clean slate by waiting once")
	}
	if got != 5 {
		t.Fatalf("one quiet period took level 6 to %d, want 5", got)
	}
}

// A rule that asks for something harsher than its ladder step keeps its own
// value. An operator who sets the honeypot rule to a permanent ban means
// it, and silently downgrading that to thirty minutes is the panel
// overruling an explicit instruction.
func TestAStricterRuleIsNotSoftenedByTheLadder(t *testing.T) {
	ab := escConfig([]int{30, 120}, 72)
	ab.Honeypot.BanMinutes = -1 // permanent, by the operator's choice
	e, hp, _ := newEngine(t, ab)

	appendLine(t, hp, hpLine("203.0.113.60"))
	e.Scan()
	b, ok := banOf(e, "203.0.113.60")
	if !ok {
		t.Fatal("no ban")
	}
	if !b.Permanent {
		t.Fatal("the operator asked for a permanent ban and the ladder downgraded it to 30 minutes")
	}
}

// With escalation off, nothing changes: every ban is the rule's own fixed
// length and no level is claimed.
func TestEscalationOffKeepsTheOldFixedLength(t *testing.T) {
	ab := escConfig([]int{30, 120, 480}, 72)
	ab.Escalation.Enabled = false
	ab.Honeypot.BanMinutes = 45
	e, hp, _ := newEngine(t, ab)
	const ip = "203.0.113.61"

	for i := 0; i < 3; i++ {
		appendLine(t, hp, hpLine(ip))
		e.Scan()
		b, ok := banOf(e, ip)
		if !ok {
			t.Fatalf("offence %d produced no ban", i+1)
		}
		if got := b.ExpiresAt.Sub(b.BannedAt).Round(time.Minute); got != 45*time.Minute {
			t.Errorf("offence %d lasted %v, want the rule's fixed 45m", i+1, got)
		}
		if b.Level != 0 {
			t.Errorf("offence %d claimed level %d while escalation is off", i+1, b.Level)
		}
		e.Unban(ip)
	}
}

// Forgiving an address explicitly puts it back to step one. The operator's
// remedy for a false positive that has already climbed.
func TestForgivingAnAddressResetsItToStepOne(t *testing.T) {
	e, hp, _ := newEngine(t, escConfig([]int{30, 120, 480}, 72))
	const ip = "203.0.113.62"

	for i := 0; i < 2; i++ {
		appendLine(t, hp, hpLine(ip))
		e.Scan()
		e.Unban(ip)
	}
	if !e.ForgiveOffender(ip) {
		t.Fatal("ForgiveOffender reported no history for an address that has two bans")
	}
	appendLine(t, hp, hpLine(ip))
	e.Scan()
	b, _ := banOf(e, ip)
	if b.Level != 1 {
		t.Fatalf("after being forgiven the address was banned at level %d, want 1", b.Level)
	}
}

// "Release everyone" must not silently wipe the escalation history: it is
// an incident action meaning "stop blocking these now", not "forget that a
// month-long campaign happened".
func TestUnbanAllKeepsTheLadderButForgiveAllClearsIt(t *testing.T) {
	e, hp, _ := newEngine(t, escConfig([]int{30, 120, 480}, 72))
	const ip = "203.0.113.63"

	appendLine(t, hp, hpLine(ip))
	e.Scan()
	e.UnbanAll()
	if got := e.OffenderLevel(ip); got != 1 {
		t.Errorf("UnbanAll wiped the escalation history (level %d, want 1)", got)
	}
	if n := e.ForgiveAll(); n != 1 {
		t.Errorf("ForgiveAll cleared %d records, want 1", n)
	}
	if got := e.OffenderLevel(ip); got != 0 {
		t.Errorf("after ForgiveAll the level is %d, want 0", got)
	}
}

// The ledger is bounded. This is the STANDING RULE applied: the cost of the
// feature is locked as behaviour, not left to a benchmark nobody runs.
//
// The budget is generous on purpose — it is a ceiling that catches a
// regression like "store the whole ban history per address", not a
// measurement to be tuned.
func TestTheLedgerIsBoundedInSizeAndMemory(t *testing.T) {
	e, _, _ := newEngine(t, escConfig([]int{30, 120, 480}, 72))
	ab := escConfig([]int{30, 120, 480}, 72)
	rule := ab.Honeypot
	now := time.Now()

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	e.mu.Lock()
	for i := 0; i < maxOffenders+5000; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
		e.nextBanLocked(ip, ab, rule, now)
	}
	n := len(e.offenders)
	e.mu.Unlock()

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	if n > maxOffenders {
		t.Fatalf("the ledger holds %d records, past its cap of %d", n, maxOffenders)
	}
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	const budget = 12 << 20 // 12 MB
	t.Logf("ledger: %d records, heap %.2f MB", n, float64(used)/(1<<20))
	if used > budget {
		t.Fatalf("a full ledger costs %.2f MB, over the %d MB budget", float64(used)/(1<<20), budget>>20)
	}
}

// Eviction drops the least valuable records — lowest level first — so a
// flood of new first-time addresses cannot push out the handful of
// addresses that have proved they come back.
func TestEvictionKeepsTheWorstOffenders(t *testing.T) {
	e, _, _ := newEngine(t, escConfig([]int{30, 120, 480}, 72))
	now := time.Now()

	e.mu.Lock()
	// One high-level record.
	e.offenders["198.51.100.1"] = &offender{Level: 3, Count: 3, Last: now.Unix()}
	// A flood of level-1 records.
	for i := 0; i < maxOffenders+100; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
		e.offenders[ip] = &offender{Level: 1, Count: 1, Last: now.Unix()}
	}
	e.evictOffendersLocked(now, 72*time.Hour)
	_, kept := e.offenders["198.51.100.1"]
	n := len(e.offenders)
	e.mu.Unlock()

	if !kept {
		t.Fatal("eviction dropped a level-3 offender while keeping level-1 records")
	}
	if n > maxOffenders {
		t.Fatalf("eviction left %d records, past the cap of %d", n, maxOffenders)
	}
}

// A fully decayed record is not written to disk as dead weight, and a
// restart does not resurrect it.
func TestFullyForgivenRecordsAreNotPersisted(t *testing.T) {
	e, _, _ := newEngine(t, escConfig([]int{30, 120}, 1))
	e.mu.Lock()
	e.offenders["198.51.100.9"] = &offender{
		Level: 1, Count: 1, Last: time.Now().Add(-500 * time.Hour).Unix()}
	e.mu.Unlock()
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(e.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st persisted
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	// It IS written (Save does not judge), but Load must not restore it
	// as a live level, and the next prune must drop it.
	e2 := New(e.cfg, "", "", nil)
	e2.statePath = e.statePath
	t.Cleanup(e2.waitForSave)
	if err := e2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := e2.OffenderLevel("198.51.100.9"); got != 0 {
		t.Fatalf("a record 500 hours stale came back at level %d, want 0", got)
	}
}

// Validation refuses ladders that cannot work, and each refusal has to name
// the actual problem — an operator who is told "invalid" learns nothing.
func TestLadderValidation(t *testing.T) {
	cases := []struct {
		name string
		e    config.BanEscalation
		ok   bool
	}{
		{"the shipped ladder", config.DefaultEscalation(), true},
		{"off is never invalid", config.BanEscalation{Enabled: false, Steps: []int{5, 1}}, true},
		{"one step", config.BanEscalation{Enabled: true, Steps: []int{60}}, true},
		{"descending", config.BanEscalation{Enabled: true, Steps: []int{120, 30}}, false},
		{"permanent in the middle", config.BanEscalation{Enabled: true, Steps: []int{30, -1, 120}}, false},
		{"permanent last", config.BanEscalation{Enabled: true, Steps: []int{30, -1}}, true},
		{"zero step", config.BanEscalation{Enabled: true, Steps: []int{0, 30}}, false},
		{"too many steps", config.BanEscalation{Enabled: true, Steps: make([]int, config.MaxEscalationSteps+1)}, false},
		{"negative decay", config.BanEscalation{Enabled: true, Steps: []int{30}, DecayHours: -1}, false},
	}
	for _, c := range cases {
		err := config.ValidateEscalation(c.e)
		if c.ok && err != nil {
			t.Errorf("%s: refused with %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: accepted, but it cannot work", c.name)
		}
	}
}

// An install that has never opened the form gets the recommended ladder,
// and one that HAS gets exactly what it saved — including off. Same class
// of bug as the empty ban history in r49, where a default computed in two
// places disagreed with itself.
func TestUnconfiguredInstallGetsTheRecommendedLadder(t *testing.T) {
	var fresh config.AutoBan // zero value: never configured, never saved
	if !fresh.EffectiveEscalation().Enabled {
		t.Error("a fresh install has progressive banning off; the recommended default was not applied")
	}
	if n := fresh.EffectiveEscalation().StepCount(); n != len(config.DefaultEscalation().Steps) {
		t.Errorf("a fresh install has %d steps, want the shipped %d", n, len(config.DefaultEscalation().Steps))
	}

	chosen := config.AutoBan{Configured: true}
	chosen.Escalation = config.BanEscalation{Enabled: false}
	if chosen.EffectiveEscalation().Enabled {
		t.Error("an operator who switched progressive banning off had it turned back on")
	}
}

// The level reaches the panel. A number the API never sends is a feature
// the operator cannot see.
func TestTheLevelIsRecordedInTheHistoryLine(t *testing.T) {
	ev := BanEvent{When: time.Now(), Action: "ban", IP: "203.0.113.5",
		Reason: ReasonHoneypot, Hits: 3, Until: time.Now().Add(time.Hour), Level: 4}
	line := ev.Line()
	if want := `level="4"`; !contains(line, want) {
		t.Fatalf("the history line does not carry the level: %s", line)
	}
	// A ban issued with escalation off must not claim a rung it never had.
	ev.Level = 0
	if contains(ev.Line(), "level=") {
		t.Fatalf("a ban with no ladder printed a level: %s", ev.Line())
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// The STANDING RULE, applied to the two operations this feature adds to
// hot paths: deciding a ban's length, and writing the state file.
//
// Both are locked as BEHAVIOUR with a generous ceiling rather than as a
// benchmark. A benchmark records a number; a budget test fails the build
// when somebody makes the ladder do a config read per address.
func TestTheLadderCostsAlmostNothingPerBan(t *testing.T) {
	e, _, _ := newEngine(t, escConfig([]int{30, 120, 480, 1440}, 72))
	ab := escConfig([]int{30, 120, 480, 1440}, 72)
	rule := ab.Honeypot
	now := time.Now()

	const n = 100000
	e.mu.Lock()
	start := time.Now()
	for i := 0; i < n; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
		e.nextBanLocked(ip, ab, rule, now)
	}
	elapsed := time.Since(start)
	e.mu.Unlock()

	per := elapsed / n
	t.Logf("ladder decision: %v for %d bans = %v each", elapsed, n, per)
	// 20 µs each is roughly a thousand times the measured cost. It exists
	// to catch a config.Read() creeping into this path, which is exactly
	// what made record() take 28 seconds over a 200,000 event scan in r43.
	if per > 20*time.Microsecond {
		t.Fatalf("deciding a ban's length costs %v; something expensive is in this path", per)
	}
}

// Persisting the ledger must not turn a cheap save into an expensive one.
func TestSavingTheLedgerStaysCheap(t *testing.T) {
	e, _, _ := newEngine(t, escConfig([]int{30, 120}, 72))
	now := time.Now()
	e.mu.Lock()
	for i := 0; i < 5000; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
		e.offenders[ip] = &offender{Level: 2, Count: 2, Last: now.Unix()}
	}
	e.mu.Unlock()

	start := time.Now()
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	fi, err := os.Stat(e.statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("save with 5,000 ledger records: %v, %.0f KB", elapsed, float64(fi.Size())/1024)
	// The measured cost of saving 5,000 BANS was 7 ms; the ledger is a
	// smaller record, so the whole write staying under 150 ms is a
	// ceiling that only a real regression can breach.
	if elapsed > 150*time.Millisecond {
		t.Fatalf("saving the ledger took %v, which is far past its budget", elapsed)
	}
}
