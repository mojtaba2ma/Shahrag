package stats

// The access log is the source of every request number the panel shows.
// Three properties are tested here and all three were broken:
//
//   1. The log is read INCREMENTALLY. The whole file used to be re-parsed
//      every 30 seconds, so each historical request was counted again on
//      every tick and the traffic charts grew with the size of the log
//      rather than with real traffic.
//   2. Rotation is survived. logrotate either renames the file (new inode)
//      or truncates it (size drops below the saved offset); either one must
//      resume reading rather than seek past the end and go silent.
//   3. A request lands in the minute its LOG LINE says, not the minute the
//      parser happened to run.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestCollector() *Collector {
	return &Collector{
		topIPs:   make(map[string]*ipAgg),
		topPaths: make(map[string]*pathAgg),
		maxAge:   MaxRetention(),
	}
}

// logLine renders one combined-format access line stamped at `when`.
func logLine(when time.Time, ip, path string, status int) string {
	return fmt.Sprintf("%s - - [%s] \"GET %s HTTP/1.1\" %d 512 \"-\" \"ua\"\n",
		ip, when.Format(accessTimeLayout), path, status)
}

func appendLog(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func totalRequests(c *Collector) int64 {
	var n int64
	for _, b := range c.buckets {
		n += b.Total
	}
	return n
}

// useLog points the collector at a temporary log and restores the default.
func useLog(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "access.log")
	old := accessLogPath
	accessLogPath = p
	t.Cleanup(func() { accessLogPath = old })
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The bug: every pass re-read the entire file, so three lines became six,
// then nine, then twelve — a panel left running showed traffic that never
// happened.
func TestParseLogsDoesNotRecountOldLines(t *testing.T) {
	p := useLog(t)
	c := newTestCollector()
	c.parseLogs() // establish the starting offset

	now := time.Now()
	for i := 0; i < 3; i++ {
		appendLog(t, p, logLine(now, "1.2.3.4", "/a", 200))
	}
	c.parseLogs()
	if got := totalRequests(c); got != 3 {
		t.Fatalf("after 3 lines the collector counted %d requests, want 3", got)
	}

	// Nothing new was written, so nothing new may be counted.
	for i := 0; i < 5; i++ {
		c.parseLogs()
	}
	if got := totalRequests(c); got != 3 {
		t.Errorf("re-reading an unchanged log counted %d requests, want 3 "+
			"— the whole file is being parsed again on every pass", got)
	}

	// New lines still arrive.
	appendLog(t, p, logLine(now, "5.6.7.8", "/b", 200))
	c.parseLogs()
	if got := totalRequests(c); got != 4 {
		t.Errorf("a newly appended line gave %d requests, want 4", got)
	}
}

// logrotate renames the old file and creates a new one. The saved offset
// belongs to a file that is no longer there, so reading must start over.
func TestParseLogsSurvivesRotationByRename(t *testing.T) {
	p := useLog(t)
	c := newTestCollector()
	c.parseLogs()

	now := time.Now()
	for i := 0; i < 10; i++ {
		appendLog(t, p, logLine(now, "1.2.3.4", "/a", 200))
	}
	c.parseLogs()
	if got := totalRequests(c); got != 10 {
		t.Fatalf("setup: counted %d, want 10", got)
	}

	// Rotate: the old file moves aside and a fresh, shorter one appears.
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	appendLog(t, p, logLine(now, "9.9.9.9", "/c", 200))
	c.parseLogs()

	if got := totalRequests(c); got != 11 {
		t.Errorf("after rotation the collector counted %d requests, want 11 "+
			"— the line written to the new file was skipped", got)
	}
}

// copytruncate rotation keeps the same inode and resets the size to zero.
func TestParseLogsSurvivesTruncation(t *testing.T) {
	p := useLog(t)
	c := newTestCollector()
	c.parseLogs()

	now := time.Now()
	for i := 0; i < 10; i++ {
		appendLog(t, p, logLine(now, "1.2.3.4", "/a", 200))
	}
	c.parseLogs()
	before := totalRequests(c)

	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	appendLog(t, p, logLine(now, "9.9.9.9", "/c", 200))
	c.parseLogs()

	if got := totalRequests(c); got != before+1 {
		t.Errorf("after truncation counted %d, want %d — the offset was left "+
			"past the end of the shortened file", got, before+1)
	}
}

// A line carries its own timestamp. Using time.Now() instead piled a whole
// backlog into whichever minute the parser happened to run in, inventing a
// traffic spike that never existed.
func TestRequestsLandInTheMinuteTheLogSays(t *testing.T) {
	p := useLog(t)
	c := newTestCollector()
	c.parseLogs()

	old := time.Now().Add(-40 * time.Minute)
	appendLog(t, p, logLine(old, "1.2.3.4", "/a", 200))
	c.parseLogs()

	if len(c.buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(c.buckets))
	}
	got := time.Unix(c.buckets[0].TS, 0)
	want := old.Truncate(time.Minute)
	if !got.Equal(want) {
		t.Errorf("the request landed at %s but the log said %s",
			got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// Buckets must stay sorted even when a line arrives out of order, otherwise
// every range query (which assumes ascending order) reads the wrong window.
func TestBucketsStaySortedWithOutOfOrderLines(t *testing.T) {
	c := newTestCollector()
	base := time.Now().Add(-time.Hour)
	for _, off := range []time.Duration{0, 10 * time.Minute, 5 * time.Minute, 3 * time.Minute} {
		c.recordAt(base.Add(off), "1.2.3.4", "/a", 200, 1)
	}
	if len(c.buckets) != 4 {
		t.Fatalf("expected 4 distinct minutes, got %d", len(c.buckets))
	}
	for i := 1; i < len(c.buckets); i++ {
		if c.buckets[i-1].TS >= c.buckets[i].TS {
			t.Fatalf("buckets are out of order at %d: %d then %d",
				i, c.buckets[i-1].TS, c.buckets[i].TS)
		}
	}
	// The same minute twice must merge, not duplicate.
	c.recordAt(base.Add(5*time.Minute), "1.2.3.4", "/a", 200, 1)
	if len(c.buckets) != 4 {
		t.Errorf("re-recording an existing minute created a duplicate bucket: %d", len(c.buckets))
	}
}
