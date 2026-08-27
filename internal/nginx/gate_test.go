package nginx

// Tests for the per-service bot shield.
//
// The property that matters is negative and easy to regress: a request
// WITHOUT the cookie must never reach proxy_pass. These tests assert on the
// generated config, and TestGateGeneratedConfigIsValid runs the real nginx
// binary over it when one is available.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
)

// writeTestCert emits a real self-signed certificate. A placeholder file is
// not enough: nginx parses the PEM during `nginx -t`, so a fake one fails for
// reasons that have nothing to do with the gate under test.
func writeTestCert(t *testing.T, certPath, keyPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cf.Close()
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	kf, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer kf.Close()
	if err := pem.Encode(kf, &pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		t.Fatal(err)
	}
}

func gateCfg(t *testing.T, mode, secret string) (*config.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "k.pem")
	writeTestCert(t, cert, key)
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	m := config.New()
	c, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	c.Domains = map[string]config.Domain{"example.com": {Cert: cert, Key: key}}
	c.Services = map[string]config.Service{
		"panel": {
			LocalPort: 3999, ListenPort: 8443, Path: "/", PathOwned: true,
			Bindings:   []config.Binding{{Domain: "example.com", Subdomain: "app"}},
			Gate:       mode,
			GateSecret: secret,
		},
	}
	c.ListenPorts = []int{8443}
	c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
	c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
	c.Nginx.FakeDir = filepath.Join(dir, "fake")
	if err := m.Write(c); err != nil {
		t.Fatal(err)
	}
	return m, c.Nginx.OutputPath
}

func genGate(t *testing.T, mode, secret string) string {
	t.Helper()
	m, out := gateCfg(t, mode, secret)
	g := NewGenerator(m)
	c, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := g.generateHTTP(c, out); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The whole point of the feature: an unauthenticated request is diverted
// BEFORE proxy_pass, so the backend is never contacted.
func TestGateDivertsBeforeProxyPass(t *testing.T) {
	for _, mode := range []string{config.GateJS, config.GateSecret} {
		t.Run(mode, func(t *testing.T) {
			got := genGate(t, mode, "TestKey_1234")

			if !strings.Contains(got, "map $cookie_shg_panel $shg_panel_ok") {
				t.Error("no cookie map was emitted")
			}
			guard := "if ($shg_panel_ok = 0) { rewrite ^ /__shg_gate_panel last; }"
			if !strings.Contains(got, guard) {
				t.Errorf("missing guard %q", guard)
			}
			if !strings.Contains(got, "location = /__shg_gate_panel") {
				t.Error("no challenge location was emitted")
			}
			// The challenge location must be internal, or the URI itself
			// becomes a fingerprint that a scanner can request directly.
			idx := strings.Index(got, "location = /__shg_gate_panel")
			if idx < 0 || !strings.Contains(got[idx:idx+200], "internal;") {
				t.Error("the challenge location is not marked internal")
			}

			// Order matters: the guard has to appear before the proxy_pass
			// of the location it protects.
			gi := strings.Index(got, guard)
			pi := strings.Index(got, "proxy_pass")
			if gi < 0 || pi < 0 || gi > pi {
				t.Errorf("guard at %d must come before proxy_pass at %d", gi, pi)
			}
		})
	}
}

// A secret-mode page must never contain the secret: the visitor types it, the
// page does not know it. Getting this wrong silently defeats the mode.
func TestSecretGatePageDoesNotLeakTheKey(t *testing.T) {
	const key = "SuperSecret_42"
	got := genGate(t, config.GateSecret, key)

	start := strings.Index(got, "location = /__shg_gate_panel")
	if start < 0 {
		t.Fatal("no challenge location")
	}
	end := strings.Index(got[start:], "\n    }")
	page := got[start : start+end]

	if strings.Contains(page, key) {
		t.Error("the challenge page contains the access key in clear text")
	}
	// The map still has to know it — that is where the comparison happens.
	if !strings.Contains(got, `"`+key+`" 1;`) {
		t.Error("the cookie map does not accept the configured key")
	}
}

// JS mode is the opposite: the token IS in the page (only a client that runs
// the script can find it), and it must match what the map accepts.
func TestJSGatePageCarriesTheToken(t *testing.T) {
	const tok = "abc123def456"
	got := genGate(t, config.GateJS, tok)
	if !strings.Contains(got, `sc("`+tok+`")`) {
		t.Error("the challenge script does not set the expected token")
	}
	if !strings.Contains(got, `"`+tok+`" 1;`) {
		t.Error("the cookie map does not accept the token the page sets")
	}
	if !strings.Contains(got, "<noscript>") {
		t.Error("a JS gate must still say something to a client without JS")
	}
}

// Regression guard for the whole feature: a config with no gates must produce
// exactly what it produced before this feature existed. Anything else would
// mean every existing user's nginx file changes on upgrade.
func TestNoGateMeansNoExtraOutput(t *testing.T) {
	got := genGate(t, config.GateOff, "")
	for _, marker := range []string{"shg_", "__shg_gate", "Service gates"} {
		if strings.Contains(got, marker) {
			t.Errorf("an ungated config emitted %q", marker)
		}
	}
}

// `return 200 '...'` breaks on a single quote and expands $variables. If a
// future edit to the page introduces one, the gate must be dropped rather
// than writing a config nginx cannot load.
func TestGatePageIsSafeForNginxStringLiteral(t *testing.T) {
	for _, mode := range []string{config.GateJS, config.GateSecret} {
		page := buildGatePage(mode, "shg_x", "tok")
		if strings.Contains(page, "'") {
			t.Errorf("[%s] page contains a single quote", mode)
		}
		if strings.Contains(page, "$") {
			t.Errorf("[%s] page contains $, which nginx would expand", mode)
		}
		if !gateSafeHTML(page) {
			t.Errorf("[%s] gateSafeHTML rejects the page we generate", mode)
		}
	}
	// And the guard actually refuses a bad page.
	if gateSafeHTML("has a ' quote") || gateSafeHTML("has a $var") {
		t.Error("gateSafeHTML accepted unsafe content")
	}
}

// Service names are free-form but nginx variable names are not.
func TestGateSlugIsAValidNginxIdentifier(t *testing.T) {
	cases := map[string]string{
		"panel":     "panel",
		"My-Panel":  "my_panel",
		"x ui 2":    "x_ui_2",
		"--weird--": "weird",
		"":          "svc",
		"سرویس":     "svc",
		"a.b.c":     "a_b_c",
	}
	for in, want := range cases {
		if got := GateSlug(in); got != want {
			t.Errorf("GateSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// Two names that collapse onto the same slug must not produce two nginx maps
// with the same variable, which nginx rejects outright.
func TestGateSlugsAreUnique(t *testing.T) {
	c := &config.Config{Services: map[string]config.Service{
		"my-panel": {Gate: config.GateJS, GateSecret: "a"},
		"my_panel": {Gate: config.GateJS, GateSecret: "b"},
		"my.panel": {Gate: config.GateJS, GateSecret: "c"},
		"ungated":  {},
	}}
	got := gateSlugs(c)
	if len(got) != 3 {
		t.Fatalf("expected 3 gated services, got %d (%v)", len(got), got)
	}
	seen := map[string]bool{}
	for name, slug := range got {
		if seen[slug] {
			t.Errorf("slug %q reused for %q", slug, name)
		}
		seen[slug] = true
	}
	if _, ok := got["ungated"]; ok {
		t.Error("an ungated service was assigned a slug")
	}
}

func TestValidGateSecret(t *testing.T) {
	ok := []string{"abcd", "MyKey_2024", "a-b-c-d", strings.Repeat("x", 64)}
	bad := []string{"", "abc", "has space", "quote'", "semi;colon",
		"dollar$", strings.Repeat("x", 65), "utf8_ک"}
	for _, s := range ok {
		if !ValidGateSecret(s) {
			t.Errorf("ValidGateSecret(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidGateSecret(s) {
			t.Errorf("ValidGateSecret(%q) = true, want false", s)
		}
	}
}

func TestNormalizeGate(t *testing.T) {
	cases := map[string]string{
		"js": config.GateJS, "JS": config.GateJS, "javascript": config.GateJS,
		"secret": config.GateSecret, "password": config.GateSecret,
		"": config.GateOff, "off": config.GateOff, "nonsense": config.GateOff,
	}
	for in, want := range cases {
		if got := config.NormalizeGate(in); got != want {
			t.Errorf("NormalizeGate(%q) = %q, want %q", in, got, want)
		}
	}
}

// The real proof: hand the generated file to the actual nginx binary.
func TestGateGeneratedConfigIsValid(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("nginx not installed")
	}
	for _, mode := range []string{config.GateJS, config.GateSecret} {
		t.Run(mode, func(t *testing.T) {
			body := genGate(t, mode, "TestKey_1234")

			dir := t.TempDir()
			conf := filepath.Join(dir, "nginx.conf")
			// A minimal wrapper: only the http context this snippet needs.
			full := "events { worker_connections 64; }\nhttp {\n" +
				"    client_body_temp_path " + dir + "/cb;\n" +
				"    proxy_temp_path " + dir + "/pt;\n" +
				"    fastcgi_temp_path " + dir + "/ft;\n" +
				"    uwsgi_temp_path " + dir + "/ut;\n" +
				"    scgi_temp_path " + dir + "/st;\n" +
				"    access_log off;\n" + body + "\n}\n"
			if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, "-t", "-c", conf,
				"-p", dir, "-e", filepath.Join(dir, "err.log")).CombinedOutput()
			s := string(out)
			// An unprivileged test run cannot bind or write the pid file;
			// those failures are about the sandbox, not the snippet.
			if err != nil && !strings.Contains(s, "syntax is ok") {
				if strings.Contains(s, "Permission denied") {
					t.Skipf("nginx cannot run unprivileged here: %s", s)
				}
				t.Fatalf("nginx rejected the generated gate config:\n%s", s)
			}
		})
	}
}
