package stats

// What statistics survive a reboot.
//
// The flush is periodic (every five minutes) plus one on a graceful stop.
// That is a deliberate trade: samples arrive every five seconds and writing
// each one would be pointless SSD wear for data nobody reads until a
// restart. But it does mean a hard kill loses up to five minutes, and this
// file asserts the ACTUAL bound rather than letting anyone assume it is
// zero.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func useTempState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := StatePath
	StatePath = filepath.Join(dir, "stats.json")
	t.Cleanup(func() { StatePath = old })
	return StatePath
}

// A collector built by hand, so no background loop is running and the test
// controls exactly what is in it.
func quietCollector() *Collector {
	return &Collector{
		topIPs:   map[string]*ipAgg{},
		topPaths: map[string]*pathAgg{},
		maxAge:   MaxRetention(),
	}
}

func TestStatsSurviveARestart(t *testing.T) {
	useTempState(t)

	c1 := quietCollector()
	now := time.Now()
	for i := 0; i < 100; i++ {
		c1.recordAt(now.Add(-time.Duration(i)*time.Minute),
			"203.0.113.5", "/index.html", 200, 1024)
	}
	c1.resources = append(c1.resources, ResourceSnap{
		TS: now.Unix(), CPU: 42, RAM: 63, Disk: 20, Swap: 75})
	if err := c1.Save(); err != nil {
		t.Fatal(err)
	}

	c2 := quietCollector()
	if err := c2.Load(); err != nil {
		t.Fatal(err)
	}
	s := c2.Summary()
	if s.TotalRequests == 0 {
		t.Fatal("no requests survived the restart")
	}
	if len(c2.resources) == 0 {
		t.Fatal("no resource samples survived the restart")
	}
	if c2.resources[0].CPU != 42 {
		t.Fatalf("resource sample corrupted: %+v", c2.resources[0])
	}
	t.Logf("%d requests and %d resource samples survived",
		s.TotalRequests, len(c2.resources))
}

// A corrupt file must be ignored, not fatal. Losing statistics is an
// inconvenience; refusing to start is an outage.
func TestACorruptStatsFileDoesNotStopStartup(t *testing.T) {
	path := useTempState(t)

	c1 := quietCollector()
	c1.recordAt(time.Now(), "1.2.3.4", "/", 200, 10)
	if err := c1.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}

	c2 := quietCollector()
	err := c2.Load()
	if err == nil {
		t.Fatal("a truncated file loaded without complaint — the damage would be silent")
	}
	// The important half: the collector is still usable afterwards.
	c2.recordAt(time.Now(), "1.2.3.4", "/", 200, 10)
	if c2.Summary().TotalRequests == 0 {
		t.Fatal("the collector was left unusable by a corrupt file")
	}
	if err := c2.Save(); err != nil {
		t.Fatalf("could not write a good file over the bad one: %v", err)
	}
}

// The file is written atomically. A rename either happened or it did not, so
// a power cut mid-write leaves the PREVIOUS good file, never a half one.
func TestSaveIsAtomic(t *testing.T) {
	path := useTempState(t)

	c := quietCollector()
	c.recordAt(time.Now(), "1.2.3.4", "/", 200, 10)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	// No temp files may be left behind: a crash loop would otherwise fill
	// the directory with .tmp files nobody ever deletes.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}

	// And the file that IS there must be complete JSON.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("the saved file is not valid JSON: %v", err)
	}
	if st.Version != stateVersion {
		t.Fatalf("version %d written, want %d", st.Version, stateVersion)
	}
}

// The honest bound on a hard kill: at most SaveInterval of history is lost.
// Asserted as a constant so nobody quietly raises it to an hour.
func TestTheHardKillWindowIsBounded(t *testing.T) {
	if SaveInterval > 30*time.Second {
		t.Fatalf("a crash would lose up to %v of statistics, which is too much",
			SaveInterval)
	}
	t.Logf("a hard kill loses at most %v of statistics", SaveInterval)
}

// After a restart the access log still exists and is full. The collector
// must NOT re-count all of it as fresh traffic — that was a real bug fixed
// in r37 and this locks it.
func TestARestartDoesNotRecountTheAccessLog(t *testing.T) {
	useTempState(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "access.log")

	line := `203.0.113.9 - - [08/Sep/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 100 "-" "-"` + "\n"
	body := ""
	for i := 0; i < 500; i++ {
		body += line
	}
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	old := accessLogPath
	accessLogPath = logPath
	t.Cleanup(func() { accessLogPath = old })

	c := quietCollector()
	c.parseLogs()
	if got := c.Summary().TotalRequests; got != 0 {
		t.Fatalf("a restart re-counted %d existing log lines as fresh traffic", got)
	}

	// A genuinely new line must still be counted.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line)
	f.Close()

	c.parseLogs()
	if got := c.Summary().TotalRequests; got != 1 {
		t.Fatalf("a new line after a restart counted %d times, want 1", got)
	}
}
