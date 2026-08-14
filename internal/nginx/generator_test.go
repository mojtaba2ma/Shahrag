package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

func TestGenerateHTTP(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	mgr := config.New()
	_ = mgr.AddDomain("test.example.com", "/etc/ssl/cert.pem", "/etc/ssl/key.pem")
	_ = mgr.AddService("Shahrag", "panel", "test.example.com", 8080, 443,
		"secretPanelPath", true, false)
	_ = mgr.AddService("app", "app", "test.example.com", 3000, 443,
		"/", false, false)

	gen := NewGenerator(mgr)
	gen.NginxConf = filepath.Join(dir, "nginx.conf") // fixture mode
	_ = os.WriteFile(gen.NginxConf, []byte("# fixture nginx.conf\nhttp {}\n"), 0o644)
	_, err := mgr.Mutate(func(c *config.Config) error {
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := gen.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	data, err := os.ReadFile(res.HTTPPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "location /secretPanelPath {") {
		t.Error("panel location missing")
	}
	if !strings.Contains(s, "proxy_redirect off;") {
		t.Error("proxy_redirect off missing for HTTP backend")
	}
	if !strings.Contains(s, "proxy_pass http://127.0.0.1:8080") {
		t.Error("panel proxy_pass missing")
	}
	if !strings.Contains(s, "proxy_pass http://127.0.0.1:3000") {
		t.Error("app proxy_pass missing")
	}
	if !strings.Contains(s, "panel.test.example.com") {
		t.Error("panel FQDN host check missing")
	}
}

func TestRealityStream(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, _ = mgr.Mutate(func(c *config.Config) error {
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services["x"] = config.RealityService{
			SNI:       "dl.google.com",
			LocalPort: 443,
			Ports:     []int{443},
		}
		return nil
	})
	gen := NewGenerator(mgr)
	gen.NginxConf = filepath.Join(dir, "nginx.conf") // fixture mode: no real nginx edits
	_ = os.WriteFile(gen.NginxConf, []byte("# fake nginx.conf\nhttp {}\n"), 0o644)
	res, err := gen.Generate()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(res.StreamPath)
	if !strings.Contains(string(data), "dl.google.com") {
		t.Error("SNI missing from stream config")
	}
	if !strings.Contains(string(data), "listen 443;") {
		t.Error("listen port missing")
	}
	// The marked stream include must have been added to the fixture nginx.conf.
	conf, _ := os.ReadFile(gen.NginxConf)
	if !strings.Contains(string(conf), "stream-gateway.conf") &&
		!strings.Contains(string(conf), "stream.conf") {
		t.Errorf("stream include missing from nginx.conf fixture:\n%s", conf)
	}
}

// TestRealityDisabledRemovesInclude ensures disabling Reality cleans the
// marked include out of nginx.conf and leaves a harmless comment-only
// stream file (the dangling-include outage bug).
func TestRealityDisabledRemovesInclude(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, _ = mgr.Mutate(func(c *config.Config) error {
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		c.Reality.Enabled = false
		return nil
	})
	gen := NewGenerator(mgr)
	gen.NginxConf = filepath.Join(dir, "nginx.conf")
	// Simulate the state left by a previous version: include present, file gone.
	base := "http {\n    server { listen 80; }\n}\n"
	withInclude := addStreamInclude(base, "/etc/nginx/stream-gateway.conf")
	if err := os.WriteFile(gen.NginxConf, []byte(withInclude), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gen.Generate(); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(gen.NginxConf)
	if strings.Contains(string(conf), "include /etc/nginx/stream-gateway.conf") {
		t.Errorf("dangling include was not removed:\n%s", conf)
	}
	if strings.Contains(string(conf), streamMarkerBegin) {
		t.Errorf("managed stream block still present:\n%s", conf)
	}
	// The stream file must exist (comment-only) so any leftover include
	// elsewhere can never break nginx -t.
	if _, err := os.Stat(filepath.Join(dir, "stream.conf")); err != nil {
		t.Errorf("stream file missing after disable: %v", err)
	}
}

// TestEmptyCertDomainSkipped ensures domains without a certificate produce a
// comment instead of an invalid `ssl_certificate ;` server block.
func TestEmptyCertDomainSkipped(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	mgr := config.New()
	_ = mgr.AddDomain("nocert.example.com", "", "")
	_ = mgr.AddService("Shahrag", "panel", "nocert.example.com", 8080, 443,
		"secretPanelPath", true, false)

	gen := NewGenerator(mgr)
	gen.NginxConf = filepath.Join(dir, "nginx.conf") // fixture mode
	_ = os.WriteFile(gen.NginxConf, []byte("# fixture nginx.conf\nhttp {}\n"), 0o644)
	_, err := mgr.Mutate(func(c *config.Config) error {
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := gen.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	data, _ := os.ReadFile(res.HTTPPath)
	s := string(data)
	if strings.Contains(s, "ssl_certificate ;") {
		t.Error("invalid empty ssl_certificate emitted")
	}
	if !strings.Contains(s, "SKIPPED") {
		t.Error("expected a SKIPPED comment for the cert-less domain")
	}
	if strings.Contains(s, "proxy_pass http://127.0.0.1:8080") {
		t.Error("cert-less domain must not emit server blocks")
	}
}

func TestAddRemoveStreamIncludeRoundTrip(t *testing.T) {
	base := "user www-data;\nhttp {\n    include /etc/nginx/conf.d/*.conf;\n}\n"
	out := addStreamInclude(base, "/etc/nginx/stream-gateway.conf")
	if !strings.Contains(out, streamMarkerBegin) || !strings.Contains(out, "stream {") {
		t.Fatalf("include not added:\n%s", out)
	}
	if addStreamInclude(out, "/etc/nginx/stream-gateway.conf") != out {
		t.Error("addStreamInclude must be idempotent")
	}
	back := removeStreamInclude(out, "/etc/nginx/stream-gateway.conf")
	if strings.Contains(back, "stream-gateway.conf") || strings.Contains(back, streamMarkerBegin) {
		t.Fatalf("include not removed:\n%s", back)
	}
	if !strings.Contains(back, "http {") {
		t.Fatalf("unrelated content damaged:\n%s", back)
	}
}

func TestSnapBackupRestore(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.conf")
	b := filepath.Join(dir, "b.conf")
	if err := os.WriteFile(a, []byte("old-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	bak, err := SnapBackup(a, b)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate both.
	if err := os.WriteFile(a, []byte("new-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("new-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bak.Restore(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(a)
	if string(data) != "old-a" {
		t.Errorf("a not restored: %q", data)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Error("b should have been removed again (did not exist before)")
	}
}
