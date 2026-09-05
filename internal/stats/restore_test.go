package stats

// Restoring from disk must leave the collector in a state that can take the
// very next request.
//
// It did not. The unique-IP map is deliberately not persisted (it is the
// heaviest field and only meaningful for the live hour), so every restored
// bucket came back with a nil map — and the first request that landed in a
// restored minute wrote into it and panicked, taking the whole panel down.
// The window was small but exact: an ordinary upgrade, then the first hit
// within the same minute.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeState(t *testing.T, st persistedState) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stats.json")
	old := StatePath
	StatePath = p
	t.Cleanup(func() { StatePath = old })
	b, err := json.Marshal(&st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The regression: recording into a bucket restored from disk panicked with
// "assignment to entry in nil map".
func TestRecordIntoARestoredBucketDoesNotPanic(t *testing.T) {
	minute := time.Now().Truncate(time.Minute).Unix()
	writeState(t, persistedState{
		Version: stateVersion,
		SavedAt: time.Now().Unix(),
		Buckets: []*MinuteBucket{{TS: minute, Total: 5}}, // UniqIPs absent, as saved
	})

	c := newTestCollector()
	if err := c.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(c.buckets) != 1 || c.buckets[0].UniqIPs != nil {
		t.Fatalf("setup: expected one restored bucket with no IP map, got %+v", c.buckets)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("recording into a restored bucket panicked: %v", r)
		}
	}()
	c.record("1.2.3.4", "/x", 200, 10)

	if c.buckets[0].Total != 6 {
		t.Errorf("the restored count was lost: total = %d, want 6", c.buckets[0].Total)
	}
	if !c.buckets[0].UniqIPs["1.2.3.4"] {
		t.Errorf("the IP was not recorded after restore")
	}
}

// Summary walks every bucket's IP map; restored buckets have none, and it
// must treat that as "no data" rather than failing.
func TestSummaryToleratesRestoredBuckets(t *testing.T) {
	minute := time.Now().Truncate(time.Minute).Unix()
	writeState(t, persistedState{
		Version: stateVersion,
		SavedAt: time.Now().Unix(),
		Buckets: []*MinuteBucket{{TS: minute, Total: 7, Success: 7, Bytes: 100}},
	})
	c := newTestCollector()
	if err := c.Load(); err != nil {
		t.Fatal(err)
	}
	s := c.Summary()
	if s.TotalRequests != 7 {
		t.Errorf("restored totals lost: %d, want 7", s.TotalRequests)
	}
	if s.LastHour.UniqueIPs != 0 {
		t.Errorf("unique IPs should be 0 for restored buckets, got %d", s.LastHour.UniqueIPs)
	}
}
