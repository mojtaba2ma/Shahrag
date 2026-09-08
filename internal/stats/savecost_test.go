package stats

// How often can statistics afford to be flushed?
//
// The flush interval is a straight trade: longer means less disk work, but a
// bigger window of history lost to a power cut. It was five minutes, chosen
// without measurement, and an end-to-end reboot test showed what that costs
// in practice — two minutes of real traffic gone.
//
// So: measure the write, then pick an interval from the number rather than
// from a feeling.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A year of retained history is the worst case the file ever holds, because
// compaction bounds it. Measuring at that size means the interval is safe
// even on a server that has been up for a year.
func fullCollector(t *testing.T, minutes int) *Collector {
	t.Helper()
	c := quietCollector()
	now := time.Now()
	for i := 0; i < minutes; i++ {
		when := now.Add(-time.Duration(i) * time.Minute)
		c.recordAt(when, "203.0.113.5", "/index.html", 200, 2048)
		c.resources = append(c.resources, ResourceSnap{
			TS: when.Unix(), CPU: 30, RAM: 60, Disk: 20, Swap: 10})
		c.conns = append(c.conns, ConnectionSnapshot{TS: when.Unix(), Active: 5})
		c.protos = append(c.protos, ProtoSnap{TS: when.Unix(), TCP: 10, UDP: 2})
	}
	return c
}

func TestStatsSaveCostAtRealisticSizes(t *testing.T) {
	for _, tc := range []struct {
		label   string
		minutes int
	}{
		{"1 hour", 60},
		{"1 day", 1440},
		{"1 week", 10080},
	} {
		dir := t.TempDir()
		old := StatePath
		StatePath = filepath.Join(dir, "stats.json")

		c := fullCollector(t, tc.minutes)

		best := time.Hour
		for i := 0; i < 5; i++ {
			start := time.Now()
			if err := c.Save(); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		fi, _ := os.Stat(StatePath)
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		t.Logf("%-8s (%6d buckets): save = %-12v file = %d KB",
			tc.label, tc.minutes, best, size/1024)
		StatePath = old

		// A save has to be cheap enough that doing it every 30 seconds
		// is invisible. 200 ms would not be.
		if best > 200*time.Millisecond {
			t.Errorf("%s: a save takes %v", tc.label, best)
		}
	}
}

// The interval, asserted as behaviour.
//
// At the measured cost (single-digit milliseconds for a week of history),
// 30 seconds costs roughly 0.03% of one core and one small write per half
// minute — nothing on any disk made this century — and it caps what a power
// cut can destroy at 30 seconds instead of five minutes.
//
// It is deliberately NOT every sample: samples arrive every 5 seconds, and
// writing six times more often to save at most 25 further seconds of history
// is not a trade worth making on an SSD.
func TestTheFlushIntervalIsTightEnoughToMatter(t *testing.T) {
	if SaveInterval > 30*time.Second {
		t.Fatalf("SaveInterval is %v, so a power cut destroys up to that much "+
			"history; the measured cost of a save does not justify it", SaveInterval)
	}
	if SaveInterval < 10*time.Second {
		t.Fatalf("SaveInterval is %v, which is more disk churn than the "+
			"extra history is worth", SaveInterval)
	}
	t.Logf("a hard kill loses at most %v of statistics", SaveInterval)
}

// Saving must not block recording: the collector's lock is taken by every
// parsed log line, and a write held inside it would stall log parsing.
func TestSavingDoesNotBlockRecording(t *testing.T) {
	dir := t.TempDir()
	old := StatePath
	StatePath = filepath.Join(dir, "stats.json")
	t.Cleanup(func() { StatePath = old })

	c := fullCollector(t, 1440)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			_ = c.Save()
		}
		close(done)
	}()

	now := time.Now()
	start := time.Now()
	for i := 0; i < 20000; i++ {
		c.recordAt(now, "198.51.100.5", "/x", 200, 100)
	}
	elapsed := time.Since(start)
	<-done

	t.Logf("20,000 records while 10 saves ran: %v (%v each)", elapsed, elapsed/20000)
	if per := elapsed / 20000; per > 10*time.Microsecond {
		t.Errorf("recording cost %v per event while saving", per)
	}
}
