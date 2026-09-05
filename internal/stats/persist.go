package stats

// Persistence.
//
// Statistics used to live only in memory, so every restart — including an
// ordinary upgrade — threw away the history. A year-long retention window is
// meaningless if a reboot empties it, so the compacted series are written to
// disk and read back at startup.
//
// Three deliberate choices:
//
//   - Written every 5 minutes, not on every sample. Samples arrive every 5
//     seconds; writing that often would be pointless SSD wear for data that
//     is only ever read after a restart.
//   - Written atomically (temp file, fsync, rename). A half-written file
//     after a power cut would be worse than no file at all.
//   - A corrupt or unreadable file is logged and ignored, never fatal. A
//     damaged stats file must not stop the panel from starting; losing
//     history is an inconvenience, losing the panel is an outage.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StatePath is where the collector persists itself. A variable so tests can
// redirect it.
var StatePath = envOrDefault("SHAHRAG_STATS_FILE", "/var/lib/shahrag/stats.json")

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// SaveInterval is how often the collector flushes to disk.
const SaveInterval = 5 * time.Minute

// persistedState is the on-disk shape. It is versioned so a future change to
// the tiers can be recognised rather than silently misread.
type persistedState struct {
	Version   int                  `json:"version"`
	SavedAt   int64                `json:"saved_at"`
	Buckets   []*MinuteBucket      `json:"buckets"`
	Conns     []ConnectionSnapshot `json:"conns"`
	Protos    []ProtoSnap          `json:"protos"`
	Resources []ResourceSnap       `json:"resources"`
}

const stateVersion = 1

// Save writes the current series to disk atomically.
func (c *Collector) Save() error {
	c.mu.RLock()
	st := persistedState{
		Version:   stateVersion,
		SavedAt:   time.Now().Unix(),
		Conns:     append([]ConnectionSnapshot(nil), c.conns...),
		Protos:    append([]ProtoSnap(nil), c.protos...),
		Resources: append([]ResourceSnap(nil), c.resources...),
	}
	// Buckets hold a map of unique IPs that is large and not worth
	// persisting: it is only meaningful for the current hour, and it is the
	// single biggest consumer of memory in the collector.
	st.Buckets = make([]*MinuteBucket, 0, len(c.buckets))
	for _, b := range c.buckets {
		if b == nil {
			continue
		}
		cp := *b
		cp.UniqIPs = nil
		st.Buckets = append(st.Buckets, &cp)
	}
	c.mu.RUnlock()

	data, err := json.Marshal(&st)
	if err != nil {
		return err
	}

	dir := filepath.Dir(StatePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".stats-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// fsync before rename: without it a crash can leave an empty file where
	// a complete one is expected.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, StatePath)
}

// Load restores a previously saved state.
//
// Errors are returned for logging but the collector stays usable: a missing
// or damaged file simply means starting with no history.
func (c *Collector) Load() error {
	raw, err := os.ReadFile(StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // first run
		}
		return fmt.Errorf("cannot read %s: %w", StatePath, err)
	}

	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("%s is not readable, starting fresh: %w", StatePath, err)
	}
	if st.Version != stateVersion {
		return fmt.Errorf("%s was written by a different version (%d), starting fresh",
			StatePath, st.Version)
	}

	now := time.Now()
	// Compact on load. The panel may have been down for days, so a lot of
	// what was fine-grained at save time now belongs in a coarser tier —
	// and anything past the retention window must go.
	c.mu.Lock()
	c.buckets = compactBuckets(st.Buckets, now)
	c.conns = compactConns(st.Conns, now)
	c.protos = compactProtos(st.Protos, now)
	c.resources = compactResources(st.Resources, now)
	// Total requests is derived by summing the buckets, so restoring them
	// restores the total automatically.
	// Restart the log reader from the top of the file. The offset is not
	// persisted because the log may have been rotated or truncated while
	// the panel was down, and re-reading is cheap next to reading the wrong
	// bytes.
	c.mu.Unlock()
	return nil
}
