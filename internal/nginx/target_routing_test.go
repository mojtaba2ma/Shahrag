package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

func genWith(t *testing.T, mutate func(c *config.Config)) (gateway, stream string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		mutate(c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(mgr)
	g.NginxConf = filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(g.NginxConf, []byte("# fixture\nhttp {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := g.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	gw, _ := os.ReadFile(res.HTTPPath)
	st, _ := os.ReadFile(res.StreamPath)
	return string(gw), string(st)
}

// A local-only configuration must produce exactly what it always produced:
// adding the target feature may not change a single byte for existing users.
func TestLocalTargetsUnchanged(t *testing.T) {
	gw, st := genWith(t, func(c *config.Config) {
		c.Domains["example.com"] = config.Domain{Cert: "/c.pem", Key: "/k.pem"}
		c.ListenPorts = []int{80, 443}
		c.Services["app"] = config.Service{
			LocalPort: 3000, ListenPort: 443, Path: "app", PathOwned: true,
			Bindings: []config.Binding{{Domain: "example.com", Subdomain: "a"}},
		}
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services["r1"] = config.RealityService{SNI: "chess.com", LocalPort: 49026, Ports: []int{2053}}
	})
	if !strings.Contains(gw, "proxy_pass http://127.0.0.1:3000;") {
		t.Errorf("local service must still proxy to 127.0.0.1:\n%s", gw)
	}
	// No resolver in the HTTP config when nothing points off-box.
	if strings.Contains(gw, "resolver ") {
		t.Error("a purely local HTTP config must not emit a resolver")
	}
	if !strings.Contains(st, "chess.com    127.0.0.1:49026;") {
		t.Errorf("local SNI rule must map to 127.0.0.1:\n%s", st)
	}
}

// Passthrough is the unblocker/exit feature: matching SNI goes straight to
// the real site on the internet, without terminating TLS.
func TestPassthroughSNIRouting(t *testing.T) {
	_, st := genWith(t, func(c *config.Config) {
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services["epic"] = config.RealityService{
			SNI: "*.epicgames.com", LocalPort: 443, Ports: []int{443},
			Target: config.PassthroughTarget,
		}
		c.Reality.Services["local"] = config.RealityService{
			SNI: "chess.com", LocalPort: 49026, Ports: []int{443},
		}
	})
	if !strings.Contains(st, "$ssl_preread_server_name:443;") {
		t.Errorf("passthrough must forward to the client's own SNI:\n%s", st)
	}
	// A wildcard must become a regex, otherwise nginx treats "*.x" literally
	// and the rule silently never matches.
	if !strings.Contains(st, "~*^(.+\\.)?epicgames\\.com$") {
		t.Errorf("wildcard SNI must be emitted as a regex:\n%s", st)
	}
	// nginx REQUIRES a resolver for a variable upstream; without it every
	// passthrough connection fails at runtime while `nginx -t` passes.
	if !strings.Contains(st, "resolver ") {
		t.Errorf("a resolver is mandatory for passthrough:\n%s", st)
	}
	// The local rule must be untouched.
	if !strings.Contains(st, "chess.com    127.0.0.1:49026;") {
		t.Errorf("local rule broken by the passthrough feature:\n%s", st)
	}
}

// An explicit remote host routes SNI traffic to another machine.
func TestRemoteHostSNIRouting(t *testing.T) {
	_, st := genWith(t, func(c *config.Config) {
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services["far"] = config.RealityService{
			SNI: "game.example.net", LocalPort: 8443, Ports: []int{443},
			Target: "203.0.113.10",
		}
	})
	if !strings.Contains(st, "game.example.net    203.0.113.10:8443;") {
		t.Errorf("remote SNI target not honoured:\n%s", st)
	}
}

// An HTTP service may point at another server; the resolver appears only
// then, and the upstream host replaces 127.0.0.1 everywhere.
func TestRemoteHTTPServiceTarget(t *testing.T) {
	gw, _ := genWith(t, func(c *config.Config) {
		c.Domains["example.com"] = config.Domain{Cert: "/c.pem", Key: "/k.pem"}
		c.ListenPorts = []int{443}
		c.Services["remote"] = config.Service{
			LocalPort: 8080, ListenPort: 443, Path: "r", PathOwned: true,
			Target:   "backend.internal",
			Bindings: []config.Binding{{Domain: "example.com", Subdomain: "r"}},
		}
		c.Services["stripped"] = config.Service{
			LocalPort: 9090, ListenPort: 443, Path: "s", PathOwned: false,
			Target:   "10.0.0.5",
			Bindings: []config.Binding{{Domain: "example.com", Subdomain: "s"}},
		}
	})
	for _, want := range []string{
		"proxy_pass http://backend.internal:8080;",
		"proxy_pass http://10.0.0.5:9090/;",
		"proxy_redirect http://10.0.0.5:9090/ /s/;",
		"resolver ",
	} {
		if !strings.Contains(gw, want) {
			t.Errorf("missing %q in gateway.conf:\n%s", want, gw)
		}
	}
	if strings.Contains(gw, "proxy_pass http://127.0.0.1:8080") {
		t.Error("the remote target must replace 127.0.0.1")
	}
}

// localhost / 127.0.0.1 / empty must all mean the same thing, so the UI can
// default the field to "localhost" without changing generated output.
func TestLocalTargetAliases(t *testing.T) {
	for _, alias := range []string{"", "localhost", "LocalHost", "127.0.0.1", " 127.0.0.1 "} {
		if !config.IsLocalTarget(alias) {
			t.Errorf("%q must count as a local target", alias)
		}
		if got := config.ResolveTarget(alias); got != "127.0.0.1" {
			t.Errorf("ResolveTarget(%q) = %q, want 127.0.0.1", alias, got)
		}
	}
	for _, remote := range []string{"example.com", "10.0.0.5", "backend.internal"} {
		if config.IsLocalTarget(remote) {
			t.Errorf("%q must NOT count as local", remote)
		}
		if got := config.ResolveTarget(remote); got != remote {
			t.Errorf("ResolveTarget(%q) = %q", remote, got)
		}
	}
}

// A hand-written regex SNI must pass through untouched.
func TestRegexSNIPassedThrough(t *testing.T) {
	if got := mapKeyForSNI("~*\\.pubg\\.com$"); got != "~*\\.pubg\\.com$" {
		t.Errorf("an explicit regex must not be rewritten, got %q", got)
	}
	if got := mapKeyForSNI("plain.example.com"); got != "plain.example.com" {
		t.Errorf("a plain hostname must stay literal, got %q", got)
	}
}
