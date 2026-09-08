package stats

// The access log that does not exist yet.
//
// This is the same bug that was found in the ban engine in r45, living in a
// second place. The reasoning is worth writing down because it will be
// tempting to "simplify" it away again.
//
// parseLogs decides where to start reading from three cases:
//
//	prevInode == 0        -> first sight: start at the END, do not replay
//	inode changed         -> rotated: start at 0
//	otherwise             -> continue from the stored offset
//
// "First sight: start at the end" is correct and important — replaying a
// rotated 200 MB log at boot would stall startup and date every historical
// request to now.
//
// But the cursor was only ever recorded after a SUCCESSFUL open. On a fresh
// install, or after someone cleared /var/log/nginx, the file does not exist
// when the panel starts. parseLogs returns early, records nothing, and
// prevInode stays 0. When nginx finally creates the file and writes to it,
// the NEXT pass meets prevInode == 0, concludes "first sight", and seeks to
// the end — silently throwing away everything written in between.
//
// On a real server the window is small, because nginx usually created the
// log long ago. On a fresh install it is the entire first period of traffic,
// and the symptom is the one that is hardest to act on: the statistics page
// is simply empty and nothing anywhere says why.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func lateLogLine(ip string, when time.Time) string {
	return fmt.Sprintf(`%s - - [%s] "GET /index.html HTTP/1.1" 200 2048 "-" "-"`+"\n",
		ip, when.Format(accessTimeLayout))
}

// The regression: a log created AFTER the collector started must be read
// from the beginning, not skipped.
func TestALogCreatedAfterStartupIsCounted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")

	old := accessLogPath
	accessLogPath = path
	t.Cleanup(func() { accessLogPath = old })

	c := quietCollector()

	// Startup: the file does not exist yet. This is a fresh install, or a
	// machine where the log was just rotated away.
	c.parseLogs()

	// nginx now creates the log and serves some traffic.
	now := time.Now()
	body := ""
	for i := 0; i < 25; i++ {
		body += lateLogLine("203.0.113.77", now)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c.parseLogs()

	got := c.Summary().TotalRequests
	if got != 25 {
		t.Fatalf("counted %d of 25 requests written after startup\n"+
			"the collector treated the newly created log as a 'first sight' "+
			"and seeked past everything already in it", got)
	}
}

// And the promise that makes the above non-trivial: a log that ALREADY had
// content when the collector started must still not be replayed.
func TestAnExistingLogIsStillNotReplayed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")

	old := accessLogPath
	accessLogPath = path
	t.Cleanup(func() { accessLogPath = old })

	// Yesterday's traffic, present before the panel starts.
	body := ""
	for i := 0; i < 500; i++ {
		body += lateLogLine("198.51.100.80", time.Now().Add(-24*time.Hour))
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c := quietCollector()
	c.parseLogs()

	if got := c.Summary().TotalRequests; got != 0 {
		t.Fatalf("replayed %d lines of pre-existing history as fresh traffic", got)
	}

	// New traffic still counts.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(lateLogLine("198.51.100.81", time.Now()))
	f.Close()

	c.parseLogs()
	if got := c.Summary().TotalRequests; got != 1 {
		t.Fatalf("a new line counted %d times, want 1", got)
	}
}

// A log that disappears and comes back — logrotate with `create`, or
// somebody running `rm` — must be picked up from the start of the new file,
// not skipped and not double counted.
func TestALogThatDisappearsAndReturnsIsPickedUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")

	old := accessLogPath
	accessLogPath = path
	t.Cleanup(func() { accessLogPath = old })

	now := time.Now()
	if err := os.WriteFile(path, []byte(lateLogLine("1.1.1.1", now)), 0o644); err != nil {
		t.Fatal(err)
	}
	c := quietCollector()
	c.parseLogs() // first sight of an existing file: skipped, correctly

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(lateLogLine("2.2.2.2", now))
	f.Close()
	c.parseLogs()
	if got := c.Summary().TotalRequests; got != 1 {
		t.Fatalf("expected 1 request, got %d", got)
	}

	// The file is deleted entirely.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	c.parseLogs() // nothing to read

	// nginx recreates it with new traffic.
	body := ""
	for i := 0; i < 10; i++ {
		body += lateLogLine("3.3.3.3", now)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c.parseLogs()

	if got := c.Summary().TotalRequests; got != 11 {
		t.Fatalf("after the log was deleted and recreated the total is %d, want 11", got)
	}
}

// The cost of the fix must be nothing: an absent log is checked on every
// 30-second pass and that check has to stay a single failed open.
func TestAMissingLogIsCheapToCheck(t *testing.T) {
	dir := t.TempDir()
	old := accessLogPath
	accessLogPath = filepath.Join(dir, "does-not-exist.log")
	t.Cleanup(func() { accessLogPath = old })

	c := quietCollector()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		c.parseLogs()
	}
	per := time.Since(start) / 1000
	t.Logf("one pass over a missing log = %v", per)
	if per > 100*time.Microsecond {
		t.Fatalf("checking for a missing log costs %v per pass", per)
	}
}
