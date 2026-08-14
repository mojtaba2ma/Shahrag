package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRandomPath(t *testing.T) {
	p := RandomPath(22)
	if len(p) != 22 {
		t.Fatalf("expected length 22, got %d", len(p))
	}
	for _, c := range p {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			t.Fatalf("unexpected character %c in %s", c, p)
		}
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")

	m := New()
	c, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ListenPorts) != 2 {
		t.Fatalf("expected 2 default ports, got %v", c.ListenPorts)
	}
	if err := m.AddDomain("example.com", "/cert", "/key"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddDomain("example.com", "/cert", "/key"); err == nil {
		t.Fatal("expected duplicate error")
	}
	c2, _ := m.Read()
	if _, ok := c2.Domains["example.com"]; !ok {
		t.Fatal("domain not persisted")
	}
}

func TestAddService(t *testing.T) {
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "config.json")
	LockPath = filepath.Join(dir, "config.lock")
	m := New()
	_ = m.AddDomain("example.com", "", "")
	if err := m.AddService("x", "app", "example.com", 3000, 443, "/", false, false); err != nil {
		t.Fatal(err)
	}
	c, _ := m.Read()
	if len(c.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(c.Services))
	}
	if c.Services["x"].Path != "/" {
		t.Fatalf("expected path '/', got %q", c.Services["x"].Path)
	}
	// Port should be added
	found := false
	for _, p := range c.ListenPorts {
		if p == 443 {
			found = true
		}
	}
	if !found {
		t.Fatal("listen port not added")
	}
}

func TestRandomPort(t *testing.T) {
	c := Default()
	p := RandomPort(c)
	if p < 10000 || p > 65000 {
		t.Fatalf("port out of range: %d", p)
	}
}

func TestEnvConfigPath(t *testing.T) {
	// Make sure New() respects the ConfigPath variable at call time.
	orig := ConfigPath
	defer func() { ConfigPath = orig }()
	dir := t.TempDir()
	ConfigPath = filepath.Join(dir, "x.json")
	LockPath = filepath.Join(dir, "x.lock")
	m := New()
	if _, err := os.Stat(ConfigPath); err != nil {
		t.Fatalf("config not created: %v", err)
	}
	_ = m
}
