package web

// The certificate feature is only real if an issued certificate is actually
// USED. These tests cover the wiring between "a certificate exists" and
// "nginx serves it", which is exactly where a plausible-looking
// implementation quietly does nothing.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

func writeTestCertPair(t *testing.T, dir, cn string, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn, "*." + cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "fullchain.pem")
	kp := filepath.Join(dir, "privkey.pem")
	cf, _ := os.Create(cp)
	_ = pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	cf.Close()
	kd, _ := x509.MarshalPKCS8PrivateKey(key)
	kf, _ := os.OpenFile(kp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	_ = pem.Encode(kf, &pem.Block{Type: "PRIVATE KEY", Bytes: kd})
	kf.Close()
	return cp, kp
}

// The whole point of the feature: a certificate recorded on a domain must
// end up in the generated nginx config, so it is actually served.
func TestIssuedCertificateReachesNginx(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	certDir := filepath.Join(dir, "issued")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cp, kp := writeTestCertPair(t, certDir, "example.com", time.Now().Add(80*24*time.Hour))

	mgr := config.New()
	c, err := mgr.Read()
	if err != nil {
		t.Fatal(err)
	}
	// A domain WITH the freshly issued certificate, and a service that
	// binds it — nginx only emits a server block for a bound domain.
	c.Domains = map[string]config.Domain{
		"example.com": {Cert: cp, Key: kp, ACME: &config.CertMeta{
			Managed: true, Wildcard: true, Challenge: "dns-01"}},
	}
	c.Services = map[string]config.Service{
		"web": {LocalPort: 3000, ListenPort: 8443, Path: "/", PathOwned: true,
			Bindings: []config.Binding{{Domain: "example.com", Subdomain: "app"}}},
	}
	c.ListenPorts = []int{8443}
	c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
	c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
	c.Nginx.FakeDir = filepath.Join(dir, "fake")
	if err := mgr.Write(c); err != nil {
		t.Fatal(err)
	}

	gen := nginxpkg.NewGenerator(mgr)
	if _, err := gen.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	out, err := os.ReadFile(c.Nginx.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	if !strings.Contains(got, "ssl_certificate "+cp+";") {
		t.Errorf("the issued certificate is not referenced by nginx:\n%s", tail(got, 500))
	}
	if !strings.Contains(got, "ssl_certificate_key "+kp+";") {
		t.Errorf("the issued key is not referenced by nginx:\n%s", tail(got, 500))
	}
}

// A domain with no certificate must be SKIPPED with an explanation, not
// emitted as a broken server block that fails nginx -t and can take the
// whole server down on the next restart.
func TestDomainWithoutCertificateIsSkippedSafely(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	mgr := config.New()
	c, _ := mgr.Read()
	c.Domains = map[string]config.Domain{"nocert.example": {}}
	c.Services = map[string]config.Service{
		"web": {LocalPort: 3000, ListenPort: 8443, Path: "/", PathOwned: true,
			Bindings: []config.Binding{{Domain: "nocert.example", Subdomain: "app"}}},
	}
	c.ListenPorts = []int{8443}
	c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
	c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
	c.Nginx.FakeDir = filepath.Join(dir, "fake")
	if err := mgr.Write(c); err != nil {
		t.Fatal(err)
	}
	gen := nginxpkg.NewGenerator(mgr)
	if _, err := gen.Generate(); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(c.Nginx.OutputPath)
	got := string(out)

	if strings.Contains(got, "ssl_certificate ;") {
		t.Error("an empty certificate path was emitted, which nginx would reject")
	}
	if !strings.Contains(got, "SKIPPED") {
		t.Errorf("the domain was not skipped with an explanation:\n%s", tail(got, 400))
	}
}

// The list endpoint must describe reality, including the states an operator
// most needs to see.
func TestCertListReportsEveryState(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	okDir := filepath.Join(dir, "ok")
	oldDir := filepath.Join(dir, "old")
	_ = os.MkdirAll(okDir, 0o755)
	_ = os.MkdirAll(oldDir, 0o755)
	okC, okK := writeTestCertPair(t, okDir, "good.example", time.Now().Add(80*24*time.Hour))
	oldC, oldK := writeTestCertPair(t, oldDir, "soon.example", time.Now().Add(5*24*time.Hour))

	mgr := config.New()
	c, _ := mgr.Read()
	c.Domains = map[string]config.Domain{
		"good.example": {Cert: okC, Key: okK,
			ACME: &config.CertMeta{Managed: true, Wildcard: true}},
		"soon.example": {Cert: oldC, Key: oldK},
		"none.example": {},
	}
	c.Shahrag.ACME.Email = "ops@example.com"
	c.Shahrag.ACME.CloudflareToken = "secret-token-value"
	if err := mgr.Write(c); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: mgr}
	rec := httptest.NewRecorder()
	s.handleListCerts(rec, httptest.NewRequest(http.MethodGet, "/api/certs", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var resp struct {
		Certs []map[string]interface{} `json:"certs"`
		ACME  map[string]interface{}   `json:"acme"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	by := map[string]map[string]interface{}{}
	for _, c := range resp.Certs {
		by[c["domain"].(string)] = c
	}

	if g := by["good.example"]; g["due_renew"] != false || g["managed"] != true {
		t.Errorf("good.example = %v", g)
	}
	if s := by["soon.example"]; s["due_renew"] != true {
		t.Errorf("a 5-day certificate must be due for renewal: %v", s)
	}
	if s := by["soon.example"]; s["managed"] != false {
		t.Error("a hand-installed certificate must not be reported as managed")
	}
	if n := by["none.example"]; n["error"] == "" {
		t.Error("a domain with no certificate must report an error")
	}

	// The token must NEVER be echoed back to the browser: that is how
	// secrets end up in screenshots and logs.
	raw := rec.Body.String()
	if strings.Contains(raw, "secret-token-value") {
		t.Error("the Cloudflare token was sent to the client")
	}
	if resp.ACME["cloudflare_configured"] != true {
		t.Error("the client should still learn that a token IS configured")
	}
}

// Detaching must not delete the files. Removing the only copy of a
// certificate because someone clicked the wrong row is unrecoverable.
func TestDetachKeepsTheFilesOnDisk(t *testing.T) {
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	cdir := filepath.Join(dir, "c")
	_ = os.MkdirAll(cdir, 0o755)
	cp, kp := writeTestCertPair(t, cdir, "gone.example", time.Now().Add(80*24*time.Hour))

	mgr := config.New()
	c, _ := mgr.Read()
	c.Domains = map[string]config.Domain{
		"gone.example": {Cert: cp, Key: kp, ACME: &config.CertMeta{Managed: true}},
	}
	if err := mgr.Write(c); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: mgr}
	req := httptest.NewRequest(http.MethodDelete, "/api/certs/gone.example", nil)
	req.SetPathValue("domain", "gone.example")
	rec := httptest.NewRecorder()
	s.handleDeleteCert(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	after, _ := mgr.Read()
	d := after.Domains["gone.example"]
	if d.Cert != "" || d.Key != "" || d.ACME != nil {
		t.Errorf("the domain still references a certificate: %+v", d)
	}
	for _, p := range []string{cp, kp} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("detach deleted %s — it must only unlink, never destroy", p)
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
