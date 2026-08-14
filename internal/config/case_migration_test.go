package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCaseVariantDomainCanonicalised reproduces the production bug: the
// wizard saved the panel domain as "Sugerdood.com" (phone auto-capitalise)
// while the real domain "sugerdood.com" already existed with certificates.
// The generator skipped the cert-less duplicate and the panel URL served
// the fake page. After migration the binding must point at the canonical
// key, the panel domain must follow, and the empty duplicate is dropped.
func TestCaseVariantDomainCanonicalised(t *testing.T) {
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")

	broken := `{
  "domains": {
    "sugerdood.com": {"cert": "/root/cert.crt", "key": "/root/private.key"},
    "Sugerdood.com": {"cert": "", "key": ""}
  },
  "services": {
    "Shahrag": {
      "local_port": 42159, "listen_port": 443, "path": "Xp3IYReUB55CmT4J9RwS1t",
      "path_owned": true, "ssl_backend": false,
      "bindings": [{"domain": "Sugerdood.com", "subdomain": "kannb"}]
    },
    "xray": {
      "local_port": 4628, "listen_port": 443, "path": "take",
      "path_owned": true, "ssl_backend": false,
      "bindings": [{"domain": "sugerdood.com", "subdomain": "kannb"}]
    }
  },
  "listen_ports": [80, 443],
  "shahrag": {
    "panel": {"domain": "Sugerdood.com", "subdomain": "kannb",
              "local_port": 42159, "listen_port": 443, "path": "Xp3IYReUB55CmT4J9RwS1t",
              "service_name": "Shahrag", "installed": true}
  }
}`
	if err := os.WriteFile(ConfigPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New()
	c, err := m.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Shahrag binding must now reference the canonical key.
	svc := c.Services["Shahrag"]
	if len(svc.Bindings) != 1 || svc.Bindings[0].Domain != "sugerdood.com" {
		t.Fatalf("Shahrag binding not canonicalised: %+v", svc.Bindings)
	}
	if c.Shahrag.Panel.Domain != "sugerdood.com" {
		t.Errorf("panel domain not canonicalised: %q", c.Shahrag.Panel.Domain)
	}
	// The cert-less duplicate must be dropped.
	if _, ok := c.Domains["Sugerdood.com"]; ok {
		t.Error("empty case-variant duplicate domain was not removed")
	}
	if _, ok := c.Domains["sugerdood.com"]; !ok {
		t.Error("canonical domain missing")
	}
	// Panel cert should follow the canonical domain.
	if c.Shahrag.Panel.Cert != "/root/cert.crt" {
		t.Errorf("panel cert not inherited: %q", c.Shahrag.Panel.Cert)
	}
	// xray binding untouched.
	if c.Services["xray"].Bindings[0].Domain != "sugerdood.com" {
		t.Error("xray binding damaged")
	}
}
