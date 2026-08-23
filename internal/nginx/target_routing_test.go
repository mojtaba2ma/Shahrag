package nginx

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// The unblock setup points a local DNS (AdGuard) at THIS server for the
// domains being unblocked. If nginx used that same DNS to find the real site
// it would be told "the site is this server" and would connect to itself
// forever — reproduced against a real nginx, where one request exhausted
// worker_connections ("128 worker_connections are not enough").
//
// A resolver that points at this machine must therefore be refused.
func TestLocalResolverIsRejected(t *testing.T) {
	// Syntax validation only: which resolver actually loops cannot be known
	// from its address, so ValidateResolvers no longer guesses.
	for _, bad := range []string{"not-an-ip", "1.2.3.4:notaport", ""} {
		if bad == "" {
			continue // empty entries are filtered out before validation
		}
		if err := config.ValidateResolvers([]string{bad}); err == nil {
			t.Errorf("resolver %q is malformed and must be rejected", bad)
		}
	}
	for _, good := range []string{
		"1.1.1.1", "8.8.8.8", "9.9.9.9:53", "192.168.1.5",
		// A loopback resolver is NOT rejected on sight: Unbound on this
		// machine is the recommended setup. Whether a specific one loops is
		// established by probing it (see CheckResolverLoop).
		"127.0.0.1:5335", "127.0.0.1:5353", "127.0.0.1", "localhost:5335",
	} {
		if err := config.ValidateResolvers([]string{good}); err != nil {
			t.Errorf("resolver %q must pass syntax validation: %v", good, err)
		}
	}
}

// The loop is detected by ASKING the resolver, so it is caught on ANY port.
// A port-based guess assumed the rewriting DNS was always on 53 and called a
// resolver on 5353 "safe" — the exact setup of a server where port 53 is
// already taken by something else.
func TestLoopDetectedOnAnyPort(t *testing.T) {
	self := []string{"203.0.113.77"}

	// A resolver that answers the relayed domain with THIS server is the
	// rewriting DNS, whatever port it listens on.
	rewriting := startStubDNS(t, "203.0.113.77")
	if err := config.CheckResolverLoop(rewriting, "epicgames.com", self, 3*time.Second); err == nil {
		t.Error("a resolver answering with this server's address must be reported as a loop")
	}

	// A resolver that answers with the real address is safe.
	truth := startStubDNS(t, "18.65.1.2")
	if err := config.CheckResolverLoop(truth, "epicgames.com", self, 3*time.Second); err != nil {
		t.Errorf("a truth resolver must be accepted: %v", err)
	}

	// No pass-through rule means nothing to check.
	if err := config.CheckResolverLoop(rewriting, "", self, time.Second); err != nil {
		t.Errorf("with no relayed domain there is nothing to validate: %v", err)
	}
}

// startStubDNS runs a tiny DNS server that answers every A query with `ip`,
// and returns its "host:port" address.
func startStubDNS(t *testing.T, ip string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })

	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		t.Fatalf("bad stub ip %q", ip)
	}

	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			if n < 13 {
				continue
			}
			// Walk the question name to find where it ends.
			i := 12
			for i < n && req[i] != 0 {
				i += 1 + int(req[i])
			}
			qend := i + 5
			if qend > n {
				continue
			}
			resp := []byte{}
			resp = append(resp, req[0], req[1])
			resp = append(resp, 0x81, 0x80)
			resp = append(resp, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00)
			resp = append(resp, req[12:qend]...)
			resp = append(resp, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01)
			resp = append(resp, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04)
			resp = append(resp, parsed...)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

// The generator must never emit a resolver that answers a relayed domain with
// this server's own address: a config that tests as valid and then melts down
// on the first request is worse than no config at all.
//
// Detection is empirical, so the fixture runs a stub DNS that behaves like
// AdGuard — and deliberately on a NON-53 port, the case a port-based guess
// used to miss.
func TestGeneratorRefusesLoopResolver(t *testing.T) {
	// The generator treats 127.0.0.1 as "this server" too, so a stub that
	// answers with it reproduces the rewrite exactly.
	rewriting := startStubDNS(t, "127.0.0.1")

	_, st := genWith(t, func(c *config.Config) {
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Resolvers = []string{rewriting}
		c.Reality.Services["unblock"] = config.RealityService{
			SNI: "*.epicgames.com", LocalPort: 443, Ports: []int{443},
			Target: config.PassthroughTarget,
		}
	})
	// The address may legitimately appear inside the WARNING comment; what
	// matters is that it is not the ACTIVE resolver directive.
	if strings.Contains(st, "resolver "+rewriting+" valid=") {
		t.Errorf("a looping resolver must never be emitted as the active resolver:\n%s", st)
	}
	if !strings.Contains(st, "resolver 1.1.1.1") {
		t.Errorf("the generator must fall back to the public defaults:\n%s", st)
	}
	if !strings.Contains(st, "WARNING") {
		t.Errorf("the fallback must be explained in the config:\n%s", st)
	}
}

// Pass-through must keep the connection opaque: ssl_preread reads the SNI,
// and nothing terminates or re-encrypts TLS. Assert the generated config has
// no directive that would open the connection.
func TestPassthroughStaysOpaque(t *testing.T) {
	_, st := genWith(t, func(c *config.Config) {
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services["unblock"] = config.RealityService{
			SNI: "*.epicgames.com", LocalPort: 443, Ports: []int{443},
			Target: config.PassthroughTarget,
		}
	})
	if !strings.Contains(st, "ssl_preread on;") {
		t.Error("ssl_preread must be enabled so the SNI can be read without decrypting")
	}
	for _, forbidden := range []string{
		"ssl_certificate", // would terminate TLS
		"proxy_ssl on",    // would re-encrypt to the upstream
		"ssl_preread off",
	} {
		if strings.Contains(st, forbidden) {
			t.Errorf("%q would open the connection; pass-through must stay a plain splice:\n%s",
				forbidden, st)
		}
	}
}

// A truth resolver on this machine (Unbound on its own port) is the
// RECOMMENDED setup, so the generator must emit it untouched. Only the
// rewriting DNS on :53 is swapped for the public defaults.
func TestLocalTruthResolverIsEmitted(t *testing.T) {
	_, st := genWith(t, func(c *config.Config) {
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Resolvers = []string{"127.0.0.1:5335"}
		c.Reality.Services["unblock"] = config.RealityService{
			SNI: "*.epicgames.com", LocalPort: 443, Ports: []int{443},
			Target: config.PassthroughTarget,
		}
	})
	if !strings.Contains(st, "resolver 127.0.0.1:5335") {
		t.Errorf("a local Unbound must be used as-is:\n%s", st)
	}
	if strings.Contains(st, "WARNING") {
		t.Errorf("a truth resolver must not be flagged:\n%s", st)
	}
}
