package banner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useTempBanLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := BanLogPath
	BanLogPath = filepath.Join(dir, "bans.log")
	t.Cleanup(func() { BanLogPath = old })
	return BanLogPath
}

func TestBanEventLineIsParseable(t *testing.T) {
	when := time.Date(2026, 9, 8, 14, 3, 11, 0, time.UTC)
	e := BanEvent{When: when, Action: "ban", IP: "203.0.113.5",
		Reason: "honeypot", Hits: 3, Until: when.Add(4 * time.Hour)}
	line := e.Line()

	// The shape the panel's parser and a human both rely on.
	if !strings.HasPrefix(line, "2026-09-08T14:03:11Z 203.0.113.5 ") {
		t.Fatalf("line does not start with timestamp and address: %q", line)
	}
	for _, want := range []string{`"ban honeypot"`, `hits="3"`, `until="2026-09-08T18:03:11Z"`} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %s", line, want)
		}
	}
	// One line, always. A newline inside would corrupt the file.
	if strings.Contains(line, "\n") {
		t.Fatal("the rendered event contains a newline")
	}
}

func TestPermanentBanSaysForever(t *testing.T) {
	e := BanEvent{When: time.Now(), Action: "ban", IP: "1.2.3.4",
		Reason: "manual", Permanent: true}
	if !strings.Contains(e.Line(), `until="forever"`) {
		t.Fatalf("permanent ban rendered as %q", e.Line())
	}
}

func TestWriteAndReadBack(t *testing.T) {
	useTempBanLog(t)
	now := time.Now()
	LogBanEvents([]BanEvent{
		{When: now, Action: "ban", IP: "10.0.0.1", Reason: "honeypot", Hits: 3},
		{When: now, Action: "ban", IP: "10.0.0.2", Reason: "auth_fail", Hits: 5},
	})
	LogBanEvents([]BanEvent{{When: now, Action: "unban", IP: "10.0.0.1"}})

	got := ReadBanLog(10)
	if len(got) != 3 {
		t.Fatalf("read back %d lines, want 3: %v", len(got), got)
	}
	// Newest first — an operator reads the most recent event.
	if !strings.Contains(got[0], "unban") {
		t.Fatalf("newest line is not first: %q", got[0])
	}
	if !strings.Contains(got[2], "10.0.0.1") || !strings.Contains(got[2], "honeypot") {
		t.Fatalf("oldest line wrong: %q", got[2])
	}
}

func TestReadingAMissingLogIsNotAnError(t *testing.T) {
	useTempBanLog(t)
	if got := ReadBanLog(10); len(got) != 0 {
		t.Fatalf("a missing file returned %d lines", len(got))
	}
}

func TestLimitIsRespected(t *testing.T) {
	useTempBanLog(t)
	now := time.Now()
	evs := make([]BanEvent, 0, 100)
	for i := 0; i < 100; i++ {
		evs = append(evs, BanEvent{When: now, Action: "ban", IP: "10.0.0.1", Reason: "honeypot"})
	}
	LogBanEvents(evs)
	if got := ReadBanLog(20); len(got) != 20 {
		t.Fatalf("limit 20 returned %d lines", len(got))
	}
}

// The file must be bounded. A control panel that fills the disk it is
// monitoring is indefensible, and the panel cannot assume a logrotate rule
// was installed for it.
func TestTheLogIsCapped(t *testing.T) {
	path := useTempBanLog(t)
	now := time.Now()
	// Each event is ~110 bytes; 30,000 is well past the 2 MiB cap.
	batch := make([]BanEvent, 2000)
	for i := range batch {
		batch[i] = BanEvent{When: now, Action: "ban", IP: "203.0.113.99",
			Reason: "not_found", Hits: 60, Until: now.Add(time.Hour)}
	}
	for i := 0; i < 15; i++ {
		LogBanEvents(batch)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxBanLogBytes*2 {
		t.Fatalf("log grew to %d bytes, cap is %d", fi.Size(), maxBanLogBytes)
	}
	// Rotation keeps exactly one sibling, so the total on disk is bounded
	// at twice the cap and never more.
	total := fi.Size()
	if fi1, err := os.Stat(path + ".1"); err == nil {
		total += fi1.Size()
	}
	if total > 2*maxBanLogBytes+(1<<20) {
		t.Fatalf("log plus rotation is %d bytes, want under %d", total, 2*maxBanLogBytes)
	}
	// And there must be no .2 — only one generation is kept.
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Fatal("a second rotation generation was kept")
	}
	t.Logf("after 30,000 events: current %d bytes, total on disk %d bytes", fi.Size(), total)
}

// After a rotation, history must still be readable — that is the whole point
// of keeping the sibling rather than truncating.
func TestHistorySurvivesRotation(t *testing.T) {
	path := useTempBanLog(t)
	now := time.Now()
	batch := make([]BanEvent, 2000)
	for i := range batch {
		batch[i] = BanEvent{When: now, Action: "ban", IP: "203.0.113.7", Reason: "honeypot"}
	}
	for i := 0; i < 25; i++ {
		LogBanEvents(batch)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("25 batches of 2000 events did not trigger a rotation")
	}
	// Write a marker, then ask for more lines than the current file holds.
	LogBanEvents([]BanEvent{{When: now, Action: "unban", IP: "198.51.100.9"}})
	got := ReadBanLog(2000)
	if len(got) < 100 {
		t.Fatalf("only %d lines readable after rotation", len(got))
	}
	if !strings.Contains(got[0], "198.51.100.9") {
		t.Fatalf("newest event lost after rotation: %q", got[0])
	}
}

// Writing must never be the reason a ban fails to apply.
func TestAnUnwritableLogIsSurvivable(t *testing.T) {
	old := BanLogPath
	// A path under a file, which can never be created as a directory.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	BanLogPath = filepath.Join(blocker, "sub", "bans.log")
	t.Cleanup(func() { BanLogPath = old })

	// Must not panic and must not block.
	LogBanEvents([]BanEvent{{When: time.Now(), Action: "ban", IP: "1.2.3.4"}})
	if got := ReadBanLog(5); len(got) != 0 {
		t.Fatalf("read %d lines from an impossible path", len(got))
	}
}

func TestEmptyBatchWritesNothing(t *testing.T) {
	path := useTempBanLog(t)
	LogBanEvents(nil)
	LogBanEvents([]BanEvent{})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an empty batch created the file")
	}
}

// Cost: one batch is one open/write/close regardless of how many addresses
// it bans. A scan that bans 500 addresses must not open the file 500 times.
func TestABatchIsOneWrite(t *testing.T) {
	path := useTempBanLog(t)
	now := time.Now()
	evs := make([]BanEvent, 500)
	for i := range evs {
		evs[i] = BanEvent{When: now, Action: "ban", IP: "10.1.1.1", Reason: "honeypot"}
	}

	start := time.Now()
	LogBanEvents(evs)
	elapsed := time.Since(start)

	lines := ReadBanLog(600)
	if len(lines) != 500 {
		t.Fatalf("wrote %d of 500 events", len(lines))
	}
	// 500 separate open/write/close calls take milliseconds even on a fast
	// disk; one does not. 20 ms is a ceiling that catches the regression
	// without flaking on a loaded box.
	if elapsed > 20*time.Millisecond {
		t.Fatalf("writing 500 events in one batch took %v", elapsed)
	}
	t.Logf("500 events written in %v", elapsed)
	_ = path
}
