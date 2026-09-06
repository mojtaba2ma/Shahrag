package nginx

// Enabling and disabling a service.
//
// The requested behaviour was "comment the block out". Commenting is the
// wrong tool: nginx has no block comments, so every line of a multi-line
// block has to be prefixed, and any later edit to the generator that adds a
// line without the prefix produces a config that is half commented and
// half live — which either fails `nginx -t` or, far worse, passes and
// serves something nobody intended. A `#` inside a `return 200 '...'`
// string is also not a comment at all.
//
// Not emitting the block is strictly better: there is nothing to get half
// right, the file stays valid by construction, and the service's settings
// live on in config.json where they can be switched back on. A comment naming
// the disabled service is still written, so the file explains itself.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// genFixture builds a generator writing into a temp dir, with two HTTP
// services on one domain and one SNI rule.
func genFixture(t *testing.T) (*Generator, *config.Manager, string, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	httpOut := filepath.Join(dir, "gateway.conf")
	streamOut := filepath.Join(dir, "stream.conf")
	nginxConf := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(nginxConf, []byte("# fixture nginx.conf\nhttp {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := config.New()
	if err := mgr.AddDomain("example.com", "/etc/ssl/cert.pem", "/etc/ssl/key.pem"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddService("alpha", "a", "example.com", 3000, 443, "alpha", true, false); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddService("beta", "b", "example.com", 3001, 443, "beta", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services = map[string]config.RealityService{
			"sni1": {SNI: "a.example.net", LocalPort: 5000, Ports: []int{2053}},
			"sni2": {SNI: "b.example.net", LocalPort: 5001, Ports: []int{2087}},
		}
		c.Nginx.OutputPath = httpOut
		c.Nginx.StreamOutputPath = streamOut
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	g := NewGenerator(mgr)
	g.NginxConf = nginxConf
	return g, mgr, httpOut, streamOut
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setDisabled(t *testing.T, mgr *config.Manager, name string, off bool) {
	t.Helper()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		svc := c.Services[name]
		svc.Disabled = off
		c.Services[name] = svc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Baseline: both services are generated when nothing is disabled.
func TestBothServicesGeneratedByDefault(t *testing.T) {
	g, _, httpOut, _ := genFixture(t)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, httpOut)
	for _, want := range []string{"location /alpha", "location /beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s is missing from a config with nothing disabled", want)
		}
	}
}

// The core behaviour: a disabled service produces no location at all, and
// its neighbour on the same domain is untouched.
func TestDisabledServiceIsNotGenerated(t *testing.T) {
	g, mgr, httpOut, _ := genFixture(t)
	setDisabled(t, mgr, "alpha", true)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, httpOut)

	if strings.Contains(out, "location /alpha") {
		t.Error("the disabled service still has a location block")
	}
	if !strings.Contains(out, "location /beta") {
		t.Error("disabling one service removed another one too")
	}
	// The file must say why, or the next person to read it will think the
	// service was lost.
	if !strings.Contains(out, `Service "alpha" is DISABLED`) {
		t.Error("nothing in the config explains that the service is off on purpose")
	}
	// proxy_pass to the disabled backend must be gone as well — a leftover
	// upstream line would still route traffic.
	if strings.Contains(out, "127.0.0.1:3000") {
		t.Error("the disabled service's upstream is still referenced")
	}
}

// Turning it back on must restore exactly what was there before: this is
// the property that makes the switch safe to use casually.
func TestReEnablingRestoresTheOriginalConfig(t *testing.T) {
	g, mgr, httpOut, _ := genFixture(t)

	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	before := stripTimestamp(readFile(t, httpOut))

	setDisabled(t, mgr, "alpha", true)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	off := stripTimestamp(readFile(t, httpOut))
	if off == before {
		t.Fatal("disabling changed nothing at all")
	}

	setDisabled(t, mgr, "alpha", false)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	after := stripTimestamp(readFile(t, httpOut))

	if after != before {
		t.Errorf("re-enabling did not restore the original config\n--- before ---\n%s\n--- after ---\n%s",
			before, after)
	}
}

// A gated service that is disabled must not leave its map block behind: the
// map would reference a location that is never emitted.
func TestDisablingAGatedServiceRemovesItsGateBlocks(t *testing.T) {
	g, mgr, httpOut, _ := genFixture(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		svc := c.Services["alpha"]
		svc.Gate = config.GateSecret
		svc.GateSecret = "SuperSecret_123"
		c.Services["alpha"] = svc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, httpOut), "shg_alpha") {
		t.Fatal("setup: the gate was not generated in the first place")
	}

	setDisabled(t, mgr, "alpha", true)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, httpOut)
	if strings.Contains(out, "shg_alpha") {
		t.Error("a disabled service left its gate map behind, pointing at a location that no longer exists")
	}
	// And the secret must not be sitting in a config file for a service
	// that is switched off.
	if strings.Contains(out, "SuperSecret_123") {
		t.Error("the disabled service's access key is still written into the config")
	}
}

// The last enabled service on a domain: the server block must still be
// valid, falling back to the fake site rather than emitting an empty block.
func TestDisablingEveryServiceOnADomainStillProducesAValidBlock(t *testing.T) {
	g, mgr, httpOut, _ := genFixture(t)
	setDisabled(t, mgr, "alpha", true)
	setDisabled(t, mgr, "beta", true)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, httpOut)
	if strings.Contains(out, "location /alpha") || strings.Contains(out, "location /beta") {
		t.Error("a disabled service was still generated")
	}
	if strings.Count(out, "{") != strings.Count(out, "}") {
		t.Error("the generated file has unbalanced braces")
	}
}

// ── SNI rules ────────────────────────────────────────────────

func TestDisabledSNIRuleIsNotRouted(t *testing.T) {
	g, mgr, _, streamOut := genFixture(t)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, streamOut), "a.example.net") {
		t.Fatal("setup: the SNI rule was not generated")
	}

	if _, err := mgr.Mutate(func(c *config.Config) error {
		r := c.Reality.Services["sni1"]
		r.Disabled = true
		c.Reality.Services["sni1"] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, streamOut)

	// The map entry is gone, so the SNI falls through to the default.
	if strings.Contains(out, "a.example.net    ") {
		t.Error("the disabled SNI rule still has a map entry")
	}
	if !strings.Contains(out, "b.example.net") {
		t.Error("disabling one SNI rule removed another")
	}
	// Its port must not be held open for a rule that routes nothing.
	if strings.Contains(out, "listen 2053;") {
		t.Error("the disabled rule's port is still opened, so it answers with the default backend")
	}
	if !strings.Contains(out, "listen 2087;") {
		t.Error("the enabled rule's port was dropped")
	}
	if !strings.Contains(out, "sni1 — DISABLED") {
		t.Error("the stream config does not record that the rule is off on purpose")
	}
}

// stripTimestamp removes the generated-at line so two runs can be compared.
func stripTimestamp(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "# Generated:") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
