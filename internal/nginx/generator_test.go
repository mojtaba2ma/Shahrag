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
	if !strings.Contains(s, "location ^~ /secretPanelPath/") {
		t.Error("panel location missing")
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
}
