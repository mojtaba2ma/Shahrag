package nginx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// TestRealityMultiPortServicesAllEmitted reproduces the real-world outage:
// Reality owns ports 443 AND 2053, services exist on both. Both port groups
// remap to the Reality HTTP port (6038) and MUST each get their own server
// block — previously the second group was silently dropped and its traffic
// served the fake page from the default_server block.
func TestRealityMultiPortServicesAllEmitted(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, err := mgr.Mutate(func(c *config.Config) error {
		c.Domains["freeline.dpdns.org"] = config.Domain{Cert: "/root/c1.crt", Key: "/root/k1.key"}
		c.Domains["sugerdood.com"] = config.Domain{Cert: "/root/cert.crt", Key: "/root/private.key"}
		c.Domains["sugerdood.qzz.io"] = config.Domain{Cert: "/root/q1.crt", Key: "/root/q1.key"}
		c.ListenPorts = []int{80, 443, 2053}
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services = map[string]config.RealityService{
			"SugerDoodR": {SNI: "chess.com", LocalPort: 49026, Ports: []int{2053}},
			"Test2":      {SNI: "status.play.google.com", LocalPort: 40956, Ports: []int{443}},
		}
		c.Services = map[string]config.Service{
			"SugerdoodX": {LocalPort: 36766, ListenPort: 443, Path: "worldx", PathOwned: true, SSLBackend: false,
				Bindings: []config.Binding{{Domain: "freeline.dpdns.org", Subdomain: "linex"}}},
			"adguard": {LocalPort: 3000, ListenPort: 443, Path: "USwYAVQx4LyqCaicR5EXFU", PathOwned: false, SSLBackend: false,
				Bindings: []config.Binding{{Domain: "sugerdood.com", Subdomain: "dash"}}},
			"tls": {LocalPort: 50768, ListenPort: 2053, Path: "news", PathOwned: true, SSLBackend: true,
				Bindings: []config.Binding{{Domain: "sugerdood.com", Subdomain: "kian"}}},
			"xray": {LocalPort: 4628, ListenPort: 443, Path: "take", PathOwned: true, SSLBackend: false,
				Bindings: []config.Binding{
					{Domain: "sugerdood.com", Subdomain: "kannb"},
					{Domain: "sugerdood.qzz.io", Subdomain: "line"},
				}},
		}
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	gen := NewGenerator(mgr)
	gen.NginxConf = filepath.Join(dir, "nginx.conf") // fixture mode: no real nginx edits
	if err := os.WriteFile(gen.NginxConf, []byte("# fixture nginx.conf\nhttp {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := gen.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	data, _ := os.ReadFile(res.HTTPPath)
	s := string(data)

	// Every service must produce its location block — including the one on
	// the second Reality port (2053), which used to be dropped.
	for _, want := range []string{
		`location /worldx {`,                   // SugerdoodX (443)
		`location = /USwYAVQx4LyqCaicR5EXFU {`, // adguard (443)
		`location /news {`,                     // tls (2053) ← previously missing
		`location /take {`,                     // xray (443)
		`proxy_pass https://127.0.0.1:50768`,   // tls → TLS backend
		`proxy_pass http://127.0.0.1:3000`,     // adguard
		`proxy_pass http://127.0.0.1:36766`,    // SugerdoodX
		`proxy_pass http://127.0.0.1:4628`,     // xray
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in gateway.conf", want)
		}
	}
	// One server block per DOMAIN on the effective port — not one per
	// original port. Both 443 and 2053 remap to 6038, so sugerdood's
	// services from both ports must live in the SAME block; emitting two
	// blocks made nginx print
	//   conflicting server name "sugerdood.com" on 0.0.0.0:6038, ignored
	// and silently drop one of them.
	if n := strings.Count(s, "listen 6038 ssl http2"); n != 3 {
		t.Errorf("expected 3 server blocks on port 6038 (one per domain), got %d\n%s", n, s)
	}
	// Exactly one default_server on 6038.
	if n := strings.Count(s, "listen 6038 ssl http2 default_server;"); n != 1 {
		t.Errorf("expected exactly 1 default_server on 6038, got %d", n)
	}
	// kian must be in the server_name list of its own block.
	if !strings.Contains(s, "kian.sugerdood.com") {
		t.Error("kian.sugerdood.com missing from server_name")
	}
}

// TestNoConflictingServerNames reproduces the exact warnings the user saw:
//
//	nginx: [warn] conflicting server name "sugerdood.com" on 0.0.0.0:6038, ignored
//	nginx: [warn] conflicting server name "kian.sugerdood.com" on 0.0.0.0:6038, ignored
//
// They appeared because Reality owned BOTH 443 and 2053 (each remapping to
// 6038) and the config also held a case-variant domain key. nginx keeps only
// the FIRST block claiming a name and ignores the rest, so the services in
// the ignored blocks silently served the fake page. Every server_name token
// must therefore be unique per listen port.
func TestNoConflictingServerNames(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, err := mgr.Mutate(func(c *config.Config) error {
		c.Domains = map[string]config.Domain{
			"sugerdood.com":      {Cert: "/root/cert.crt", Key: "/root/private.key"},
			"freeline.dpdns.org": {Cert: "/root/c1.crt", Key: "/root/k1.key"},
		}
		c.ListenPorts = []int{80, 443, 2053}
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services = map[string]config.RealityService{
			"R1": {SNI: "chess.com", LocalPort: 49026, Ports: []int{2053}},
			"R2": {SNI: "www.samsung.com", LocalPort: 40956, Ports: []int{443}},
		}
		c.Services = map[string]config.Service{
			// Same domain, different Reality-owned ports.
			"xray": {LocalPort: 4628, ListenPort: 443, Path: "take", PathOwned: true,
				Bindings: []config.Binding{{Domain: "sugerdood.com", Subdomain: "kannb"}}},
			"tls": {LocalPort: 50768, ListenPort: 2053, Path: "news", PathOwned: true, SSLBackend: true,
				Bindings: []config.Binding{{Domain: "sugerdood.com", Subdomain: "kian"}}},
			"linex": {LocalPort: 36766, ListenPort: 443, Path: "worldx", PathOwned: true,
				Bindings: []config.Binding{{Domain: "freeline.dpdns.org", Subdomain: "linex"}}},
		}
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	gen := NewGenerator(mgr)
	gen.NginxConf = filepath.Join(dir, "nginx.conf")
	_ = os.WriteFile(gen.NginxConf, []byte("# fixture\nhttp {}\n"), 0o644)
	res, err := gen.Generate()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(res.HTTPPath)
	assertNoDuplicateServerNames(t, string(data))

	s := string(data)
	// Both services of the same domain must live in ONE block, so both
	// locations are present and reachable.
	if !strings.Contains(s, "location /take {") || !strings.Contains(s, "location /news {") {
		t.Errorf("both locations must be emitted:\n%s", s)
	}
	if n := strings.Count(s, "listen 6038 ssl http2"); n != 2 {
		t.Errorf("expected one block per domain (2), got %d\n%s", n, s)
	}
}

// TestCaseVariantDomainsShareOneBlock: a config that still holds both
// "Sugerdood.com" and "sugerdood.com" must not produce two competing server
// blocks for the same hostname.
func TestCaseVariantDomainsShareOneBlock(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_, err := mgr.Mutate(func(c *config.Config) error {
		c.Domains = map[string]config.Domain{
			"Sugerdood.com": {Cert: "/root/kannb.pem", Key: "/root/kannb.key"},
			"sugerdood.com": {Cert: "/root/cert.crt", Key: "/root/private.key"},
		}
		c.ListenPorts = []int{443}
		c.Services = map[string]config.Service{
			"a": {LocalPort: 4628, ListenPort: 443, Path: "take", PathOwned: true,
				Bindings: []config.Binding{{Domain: "sugerdood.com", Subdomain: "kannb"}}},
			"b": {LocalPort: 3000, ListenPort: 443, Path: "dash", PathOwned: true,
				Bindings: []config.Binding{{Domain: "Sugerdood.com", Subdomain: "dash"}}},
		}
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	gen := NewGenerator(mgr)
	gen.NginxConf = filepath.Join(dir, "nginx.conf")
	_ = os.WriteFile(gen.NginxConf, []byte("# fixture\nhttp {}\n"), 0o644)
	res, err := gen.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s := string(mustRead(t, res.HTTPPath))
	assertNoDuplicateServerNames(t, s)
	if n := strings.Count(s, "listen 443 ssl http2"); n != 1 {
		t.Errorf("case variants must share ONE server block, got %d\n%s", n, s)
	}
	if !strings.Contains(s, "location /take {") || !strings.Contains(s, "location /dash {") {
		t.Errorf("both services must be present:\n%s", s)
	}
	// Uppercase spellings must never leak into the generated config.
	if strings.Contains(s, "Sugerdood.com") {
		t.Errorf("server block must use the lowercase hostname:\n%s", s)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertNoDuplicateServerNames parses the generated config and fails when a
// hostname is claimed by more than one server block on the same listen port
// (exactly what nginx reports as "conflicting server name ... ignored").
func assertNoDuplicateServerNames(t *testing.T, conf string) {
	t.Helper()
	type key struct {
		port int
		name string
	}
	seen := map[key]int{}
	var curPorts []int
	inServer := false
	for _, line := range strings.Split(conf, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "server {"):
			inServer = true
			curPorts = nil
		case l == "}" && inServer:
			inServer = false
		case inServer && strings.HasPrefix(l, "listen "):
			f := strings.Fields(strings.TrimSuffix(l, ";"))
			if len(f) >= 2 {
				spec := f[1]
				if i := strings.LastIndex(spec, "]:"); i >= 0 {
					spec = spec[i+2:]
				}
				var p int
				if _, err := fmt.Sscanf(spec, "%d", &p); err == nil {
					dup := false
					for _, e := range curPorts {
						if e == p {
							dup = true
						}
					}
					if !dup {
						curPorts = append(curPorts, p)
					}
				}
			}
		case inServer && strings.HasPrefix(l, "server_name "):
			names := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(l, "server_name "), ";"))
			for _, p := range curPorts {
				for _, n := range names {
					if n == "_" {
						continue
					}
					k := key{p, n}
					seen[k]++
					if seen[k] > 1 {
						t.Errorf("conflicting server name %q on port %d (nginx would ignore the duplicate block)", n, p)
					}
				}
			}
		}
	}
}
