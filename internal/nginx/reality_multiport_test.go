package nginx

import (
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
	// Service blocks on the Reality HTTP port: freeline (443), sugerdood
	// (443), qzz (443) and sugerdood's 2053 group — four servers in total.
	if n := strings.Count(s, "listen 6038 ssl http2"); n != 4 {
		t.Errorf("expected 4 server blocks on port 6038, got %d\n%s", n, s)
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
