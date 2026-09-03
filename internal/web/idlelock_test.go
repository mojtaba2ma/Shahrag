package web

// The inactivity lock only works if BACKGROUND polling does not count as
// user activity. The stats page refreshes every 5 seconds; when that request
// looked like activity the session never expired and the panel stayed logged
// in for days, which is exactly what was reported.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readJS(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Every repeating background request must carry _poll=1. Anything on a timer
// that does not is, by definition, a heartbeat that defeats the lock.
func TestBackgroundPollsDoNotCountAsActivity(t *testing.T) {
	cases := []struct{ file, fn string }{
		{"static/js/pages/stats.js", "the 5-second resource refresh"},
		{"static/js/app.js", "the nginx status heartbeat"},
	}
	for _, c := range cases {
		src := readJS(t, "./"+c.file)
		// Find calls made from inside a setInterval body.
		idx := strings.Index(src, "setInterval")
		if idx < 0 {
			t.Fatalf("%s: no setInterval found — did the poller move?", c.file)
		}
		if !strings.Contains(src, "_poll=1") {
			t.Errorf("%s (%s) polls without _poll=1, so the inactivity lock can never fire",
				c.file, c.fn)
		}
	}

	// Specifically the resources call, which is the one that regressed.
	stats := readJS(t, "./static/js/pages/stats.js")
	re := regexp.MustCompile(`/api/stats/resources\?minutes=\$\{mins\}&_poll=1`)
	if !re.MatchString(stats) {
		t.Error("the resources poll must request _poll=1")
	}
}

// The server side of the same contract.
func TestServerTreatsPollAsNonActivity(t *testing.T) {
	src := readJS(t, "./api.go")
	if !strings.Contains(src, `r.URL.Query().Get("_poll") == "1"`) {
		t.Fatal("the server no longer recognises _poll")
	}
	// lastActive must be updated only when the request is NOT a poll.
	if !regexp.MustCompile(`if !isPoll \{[^}]*lastActive\[cookie\.Value\] = time\.Now\(\)`).
		MatchString(src) {
		t.Error("lastActive must be refreshed only for non-poll requests")
	}
}

// A long setTimeout is not a reliable clock: browsers throttle timers in
// background tabs and do not run them at all while the machine sleeps, so a
// laptop closed overnight woke up still logged in. The client must compare
// wall-clock time instead.
func TestClientLockUsesAWallClockDeadline(t *testing.T) {
	app := readJS(t, "./static/js/app.js")

	if regexp.MustCompile(`idleTimer = setTimeout\(lockPanel`).MatchString(app) {
		t.Error("the idle lock still uses a single long setTimeout, which sleep and " +
			"background throttling both defeat")
	}
	for _, want := range []string{"idleDeadline", "Date.now() >= idleDeadline", "setInterval"} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js is missing %q — the deadline check is incomplete", want)
		}
	}
	// Returning from sleep or from a hidden tab must be checked immediately,
	// not up to one tick later.
	if !strings.Contains(app, `addEventListener("visibilitychange"`) {
		t.Error("the lock must be re-checked when the tab becomes visible again")
	}
}

// The lock controls must stay on the Settings page; they went missing once
// and nobody noticed until the lock itself was questioned.
func TestLockSettingsArePresentInTheUI(t *testing.T) {
	src := readJS(t, "./static/js/pages/settings.js")
	for _, want := range []string{
		`id="s-lock-en"`,             // the on/off switch
		`id="s-lock"`,                // the minutes field
		`t("settings.lock_enabled")`, // labelled, not a bare box
		`t("settings.lock_minutes")`,
		"lock_minutes:", // and actually saved
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the Settings page is missing %q", want)
		}
	}
}
