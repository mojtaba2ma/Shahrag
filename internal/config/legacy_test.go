package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLegacyServiceBindingMigration ensures services that store
// subdomain/domain directly on the service (pre-bindings config format) are
// migrated into Bindings when read, so the nginx generator produces server
// blocks for them instead of silently serving the fake page.
func TestLegacyServiceBindingMigration(t *testing.T) {
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")

	legacy := `{
  "domains": {"example.com": {"cert": "/c.pem", "key": "/k.pem"}},
  "services": {
    "app": {
      "local_port": 3000,
      "listen_port": 443,
      "path": "/app",
      "subdomain": "app",
      "domain": "example.com"
    },
    "rootapp": {
      "local_port": 4000,
      "listen_port": 443,
      "path": "/",
      "domain": "example.com"
    }
  },
  "listen_ports": [80, 443],
  "reality": {"enabled": false, "http_port": 6038, "services": {}}
}`
	if err := os.WriteFile(ConfigPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New()
	c, err := m.Read()
	if err != nil {
		t.Fatalf("read legacy config: %v", err)
	}
	app := c.Services["app"]
	if len(app.Bindings) != 1 {
		t.Fatalf("expected 1 migrated binding, got %d", len(app.Bindings))
	}
	if app.Bindings[0].Domain != "example.com" || app.Bindings[0].Subdomain != "app" {
		t.Errorf("binding not migrated correctly: %+v", app.Bindings[0])
	}
	if app.Subdomain != "" || app.Domain != "" {
		t.Error("legacy fields should be cleared after migration")
	}
	root := c.Services["rootapp"]
	if len(root.Bindings) != 1 || root.Bindings[0].Domain != "example.com" || root.Bindings[0].Subdomain != "" {
		t.Errorf("root binding not migrated correctly: %+v", root.Bindings)
	}
}

// TestRandomPortExcludesUsedPorts ensures the wizard's random port never
// collides with ports already referenced by the config.
func TestRandomPortExcludesUsedPorts(t *testing.T) {
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")
	m := New()
	_, err := m.Mutate(func(c *Config) error {
		c.ListenPorts = []int{80, 443, 2053}
		c.Services["x"] = Service{LocalPort: 4242, ListenPort: 443, Bindings: []Binding{{Domain: "d.com", Subdomain: "x"}}}
		c.Reality.Enabled = true
		c.Reality.Services["r"] = RealityService{SNI: "s", LocalPort: 5151, Ports: []int{443}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := m.Read()
	for i := 0; i < 50; i++ {
		p := RandomPort(c)
		if p < 10000 || p > 65000 {
			t.Fatalf("port out of random range: %d", p)
		}
		for _, used := range []int{80, 443, 2053, 4242, 5151} {
			if p == used {
				t.Fatalf("random port collided with a used port: %d", p)
			}
		}
	}
}

// TestLegacyFieldsDroppedOnWrite ensures migrated legacy fields are not
// written back at the service level (only as proper bindings).
func TestLegacyFieldsDroppedOnWrite(t *testing.T) {
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")
	legacy := `{
  "domains": {"example.com": {"cert": "/c.pem", "key": "/k.pem"}},
  "services": {
    "app": {"local_port": 3000, "listen_port": 443, "path": "/app",
            "subdomain": "app", "domain": "example.com"}
  },
  "listen_ports": [80, 443]
}`
	if err := os.WriteFile(ConfigPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New()
	c, _ := m.Read()
	if err := m.Write(c); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ConfigPath)
	// Re-parse the written file: the service must carry bindings and no
	// legacy subdomain/domain fields.
	var c2 Config
	if err := json.Unmarshal(data, &c2); err != nil {
		t.Fatal(err)
	}
	svc := c2.Services["app"]
	if svc.Subdomain != "" || svc.Domain != "" {
		t.Errorf("legacy fields written back: sub=%q dom=%q", svc.Subdomain, svc.Domain)
	}
	if len(svc.Bindings) != 1 || svc.Bindings[0].Subdomain != "app" {
		t.Errorf("bindings not preserved on write: %+v", svc.Bindings)
	}
}
