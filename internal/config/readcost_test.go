package config

// The cost of Read().
//
// Read() runs on essentially every authenticated request, so its cost is
// paid constantly. It used to serialise the entire config TWICE — once
// before migrating and once after — purely to detect whether a migration
// had changed anything. Measured on a 7 KB config that was 208µs and 60 KB
// of garbage per call, for work that changes nothing on all but the first
// read after an upgrade.
//
// migrate() now reports whether it changed something, so the serialisation
// only happens when there is genuinely something to write. These tests pin
// both halves: the fast path must not write, and the slow path still must.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tmpManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")
	return New()
}

// A read of an already-current config must not rewrite the file, and —
// the actual saving — must not serialise it either.
//
// Honest note: the OLD code also skipped the write. What it did NOT skip
// was the two MarshalIndent calls it performed to discover that, which is
// where the 208µs and 60 KB went. So the test that discriminates is an
// allocation count, not an mtime.
func TestReadingACurrentConfigIsCheap(t *testing.T) {
	m := tmpManager(t)
	if err := m.AddDomain("example.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := m.AddService(fmt.Sprintf("svc%d", i), "s", "example.test",
			3000+i, 443, fmt.Sprintf("p%d", i), true, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Read(); err != nil { // settle any migration
		t.Fatal(err)
	}

	// The file must not change.
	raw1, _ := os.ReadFile(ConfigPath)
	// And the read must not allocate the whole config several times over.
	allocs := testing.AllocsPerRun(20, func() {
		if _, err := m.Read(); err != nil {
			t.Fatal(err)
		}
	})
	raw2, _ := os.ReadFile(ConfigPath)

	if string(raw1) != string(raw2) {
		t.Error("a plain read changed the config file")
	}
	t.Logf("%.0f allocations per read", allocs)
	// Measured on this fixture: 165 allocations with the double-marshal,
	// 113 without. 140 sits between them, so a reintroduced marshal fails
	// while leaving room for the JSON parse itself, which is irreducible.
	if allocs > 140 {
		t.Errorf("%.0f allocations per read — the config is being serialised "+
			"again just to detect a migration", allocs)
	}
}

// The slow path must still work: a config needing migration is repaired AND
// written back, or the repair would be redone on every read forever.
func TestAConfigNeedingMigrationIsStillWrittenBack(t *testing.T) {
	m := tmpManager(t)
	// A legacy service: domain/subdomain on the service instead of in
	// bindings. This is exactly what older versions wrote.
	legacy := `{
	  "domains": {"example.test": {"cert": "/c.pem", "key": "/k.pem"}},
	  "services": {"old": {"local_port": 3000, "listen_port": 443,
	    "path": "app", "domain": "example.test", "subdomain": "a"}},
	  "listen_ports": [443]
	}`
	if err := os.WriteFile(ConfigPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Services["old"].Bindings) != 1 {
		t.Fatalf("the legacy service was not migrated: %+v", c.Services["old"])
	}

	// The repair must be on DISK, not only in the returned value.
	raw, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Config
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Services["old"].Bindings) != 1 {
		t.Error("the migration was not persisted, so it would be redone on every read")
	}
	if onDisk.Services["old"].Domain != "" {
		t.Error("the legacy field survived on disk")
	}

	// And a second read must now be on the fast path.
	before, _ := os.Stat(ConfigPath)
	time.Sleep(1100 * time.Millisecond)
	if _, err := m.Read(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(ConfigPath)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the config was rewritten again after it had already been migrated")
	}
}

// migrate() must report accurately: saying "changed" when nothing did would
// reintroduce the write on every read.
func TestMigrateReportsWhetherItChangedAnything(t *testing.T) {
	m := tmpManager(t)
	if err := m.AddDomain("example.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	c, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if m.migrate(c) {
		t.Error("migrate reported a change on an already-current config")
	}

	// Now break something it repairs.
	c.Nginx.OutputPath = ""
	if !m.migrate(c) {
		t.Error("migrate did not report filling in a missing field")
	}
	if c.Nginx.OutputPath == "" {
		t.Error("migrate reported a change but did not make one")
	}
}

// A read must not lose data. This is the safety net for the whole change:
// whatever the fast path skips, it must never skip correctness.
func TestReadPreservesEverything(t *testing.T) {
	m := tmpManager(t)
	if err := m.AddDomain("example.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddService("app", "a", "example.test", 3000, 443, "app", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Mutate(func(c *Config) error {
		svc := c.Services["app"]
		svc.AllowIPs = []string{"10.0.0.0/24"}
		svc.Gate = GateSecret
		svc.GateSecret = "TestKey_12345"
		c.Services["app"] = svc
		c.Honeypot = Honeypot{Enabled: true, Mode: HoneypotThrottle}
		c.AutoBan = DefaultAutoBan()
		c.AutoBan.Enabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		c, err := m.Read()
		if err != nil {
			t.Fatal(err)
		}
		svc := c.Services["app"]
		if len(svc.AllowIPs) != 1 || svc.AllowIPs[0] != "10.0.0.0/24" {
			t.Fatalf("read %d lost the address lock: %+v", i, svc.AllowIPs)
		}
		if svc.GateSecret != "TestKey_12345" {
			t.Fatalf("read %d lost the gate key", i)
		}
		if !c.Honeypot.Enabled || !c.AutoBan.Enabled {
			t.Fatalf("read %d lost a security setting", i)
		}
	}
}
