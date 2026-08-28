package certs

// Unit tests for the pieces that decide correctness. The live end-to-end
// issuance against Let's Encrypt staging lives in acme_live_test.go and is
// opt-in, because it needs a real domain and real DNS.

import (
	"context"
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
)

// A wildcard does NOT cover its own apex. Getting this wrong means a
// certificate that silently fails on the bare domain, so both names must be
// requested.
func TestNamesForAlwaysIncludesTheApexWithAWildcard(t *testing.T) {
	got := NamesFor("example.com", true)
	if len(got) != 2 || got[0] != "example.com" || got[1] != "*.example.com" {
		t.Errorf("NamesFor(wildcard) = %v, want [example.com *.example.com]", got)
	}
	if got := NamesFor("example.com", false); len(got) != 1 || got[0] != "example.com" {
		t.Errorf("NamesFor(single) = %v", got)
	}
	// An operator may type the wildcard form; it must not become "*.*.x".
	if got := NamesFor("*.example.com", true); len(got) != 2 || got[1] != "*.example.com" {
		t.Errorf("NamesFor already-wildcard = %v", got)
	}
	if got := NamesFor("  EXAMPLE.com ", false); len(got) != 1 || got[0] != "example.com" {
		t.Errorf("NamesFor did not normalise case/space: %v", got)
	}
}

// Wildcard semantics, the part everyone gets wrong.
func TestCoversImplementsWildcardRulesExactly(t *testing.T) {
	names := []string{"example.com", "*.example.com"}
	yes := []string{"example.com", "app.example.com", "APP.example.com"}
	no := []string{
		"a.b.example.com", // a wildcard matches ONE label only
		"example.org",
		"notexample.com",
		"", // empty host must never match
	}
	for _, h := range yes {
		if !Covers(names, h) {
			t.Errorf("Covers(%q) = false, want true", h)
		}
	}
	for _, h := range no {
		if Covers(names, h) {
			t.Errorf("Covers(%q) = true, want false", h)
		}
	}
	// Without the apex in the SAN list, the bare domain must NOT match.
	if Covers([]string{"*.example.com"}, "example.com") {
		t.Error("a bare wildcard must not cover the apex")
	}
}

// A wildcard over HTTP-01 is impossible per the ACME spec. Failing early
// with a clear message beats a confusing CA error minutes later.
func TestWildcardOverHTTPChallengeIsRefused(t *testing.T) {
	i := &Issuer{}
	_, err := i.Issue(context.Background(), Request{
		Domain: "example.com", Wildcard: true, Challenge: ChallengeHTTP,
	})
	if err == nil || !strings.Contains(err.Error(), "DNS challenge") {
		t.Errorf("expected a wildcard/HTTP-01 refusal, got %v", err)
	}
}

func TestDNSChallengeWithoutProviderIsRefused(t *testing.T) {
	i := &Issuer{}
	_, err := i.Issue(context.Background(), Request{
		Domain: "example.com", Wildcard: true, Challenge: ChallengeDNS,
	})
	if err == nil || !strings.Contains(err.Error(), "DNS provider") {
		t.Errorf("expected a missing-provider error, got %v", err)
	}
}

// The account key must persist. A fresh key on every run registers a new
// ACME account each time and hits the CA's registration rate limit.
func TestAccountKeyIsCreatedOnceAndReused(t *testing.T) {
	dir := t.TempDir()
	old := AccountKeyPath
	AccountKeyPath = filepath.Join(dir, "acme.key")
	defer func() { AccountKeyPath = old }()

	k1, err := accountKey()
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(AccountKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	// It is an authentication credential: it must not be world-readable.
	if m := st.Mode().Perm(); m != 0o600 {
		t.Errorf("account key mode = %o, want 600", m)
	}
	k2, err := accountKey()
	if err != nil {
		t.Fatal(err)
	}
	e1 := k1.(*ecdsa.PrivateKey)
	e2 := k2.(*ecdsa.PrivateKey)
	if e1.D.Cmp(e2.D) != 0 {
		t.Error("a second call generated a different account key")
	}
}

func writeCert(t *testing.T, dir string, notAfter time.Time, names []string, selfSigned bool) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	parent := tmpl
	if !selfSigned {
		parent.Subject = pkix.Name{CommonName: "Test CA"}
		parent.Issuer = pkix.Name{CommonName: "Test CA"}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &parent, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	cf, _ := os.Create(certPath)
	pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	cf.Close()
	kd, _ := x509.MarshalPKCS8PrivateKey(key)
	kf, _ := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	pem.Encode(kf, &pem.Block{Type: "PRIVATE KEY", Bytes: kd})
	kf.Close()
	return certPath, keyPath
}

func TestInspectReportsExpiryAndRenewalWindow(t *testing.T) {
	dir := t.TempDir()

	// Comfortably valid.
	c1, k1 := writeCert(t, t.TempDir(), time.Now().Add(80*24*time.Hour),
		[]string{"example.com", "*.example.com"}, false)
	in := Inspect("example.com", c1, k1)
	if in.Error != "" {
		t.Fatalf("unexpected error: %s", in.Error)
	}
	if !in.Wildcard {
		t.Error("a *.example.com SAN was not reported as a wildcard")
	}
	if in.Expired || in.DueRenew {
		t.Errorf("an 80-day cert should be neither expired nor due: %+v", in)
	}
	if in.DaysLeft < 78 || in.DaysLeft > 81 {
		t.Errorf("DaysLeft = %d, want ~80", in.DaysLeft)
	}

	// Inside the 30-day renewal window.
	c2, k2 := writeCert(t, t.TempDir(), time.Now().Add(10*24*time.Hour),
		[]string{"soon.example.com"}, false)
	if in := Inspect("soon.example.com", c2, k2); !in.DueRenew || in.Expired {
		t.Errorf("a 10-day cert should be due for renewal: %+v", in)
	}

	// Already expired.
	c3, k3 := writeCert(t, t.TempDir(), time.Now().Add(-24*time.Hour),
		[]string{"old.example.com"}, false)
	if in := Inspect("old.example.com", c3, k3); !in.Expired {
		t.Error("an expired certificate was not reported as expired")
	}

	// Self-signed must be flagged: browsers reject it, and the install
	// wizard generates one as a placeholder.
	c4, k4 := writeCert(t, t.TempDir(), time.Now().Add(80*24*time.Hour),
		[]string{"self.example.com"}, true)
	if in := Inspect("self.example.com", c4, k4); !in.SelfSuper {
		t.Error("a self-signed certificate was not flagged")
	}

	// Missing file: a clear sentence, not an errno.
	if in := Inspect("gone.example.com", filepath.Join(dir, "nope.pem"),
		filepath.Join(dir, "nope.key")); in.Error == "" ||
		!strings.Contains(in.Error, "does not exist") {
		t.Errorf("a missing certificate produced %q", in.Error)
	}

	// Not configured at all.
	if in := Inspect("blank.example.com", "", ""); in.Error == "" {
		t.Error("an unconfigured domain should report an error")
	}
}

// A cert/key mismatch surfaces as an nginx reload failure that reads like a
// syntax error. The panel must name the real problem.
func TestInspectDetectsMismatchedKey(t *testing.T) {
	c1, _ := writeCert(t, t.TempDir(), time.Now().Add(80*24*time.Hour),
		[]string{"a.example.com"}, false)
	_, k2 := writeCert(t, t.TempDir(), time.Now().Add(80*24*time.Hour),
		[]string{"b.example.com"}, false)

	in := Inspect("a.example.com", c1, k2)
	if in.Error == "" || !strings.Contains(in.Error, "do not match") {
		t.Errorf("a mismatched pair produced %q", in.Error)
	}
}

func TestSummariseGroupsByState(t *testing.T) {
	list := []Info{
		{Domain: "ok.com"},
		{Domain: "soon.com", DueRenew: true},
		{Domain: "dead.com", Expired: true, DueRenew: true},
		{Domain: "broken.com", Error: "boom"},
	}
	s := Summarise(list)
	if s.Total != 4 {
		t.Errorf("Total = %d", s.Total)
	}
	if len(s.Expired) != 1 || s.Expired[0] != "dead.com" {
		t.Errorf("Expired = %v", s.Expired)
	}
	if len(s.DueSoon) != 1 || s.DueSoon[0] != "soon.com" {
		t.Errorf("DueSoon = %v", s.DueSoon)
	}
	if len(s.Problems) != 1 || s.Problems[0] != "broken.com" {
		t.Errorf("Problems = %v", s.Problems)
	}
}

// Writing must be atomic: nginx reads these files on reload, and a truncated
// chain takes the site down.
func TestAtomicWriteLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.pem")
	if err := atomicWrite(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hello" {
		t.Errorf("content = %q", b)
	}
	st, _ := os.Stat(p)
	if m := st.Mode().Perm(); m != 0o600 {
		t.Errorf("mode = %o, want 600", m)
	}
	// No temp files may survive.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temp file survived: %s", e.Name())
		}
	}
}

// ── Cloudflare provider, against a fake API ──────────────────

func cfServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*CloudflareProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	p := NewCloudflareProvider("test-token")
	p.Client = srv.Client()
	// Point the provider at the fake server.
	oldAPI := cfAPIBase
	cfAPIBase = srv.URL
	t.Cleanup(func() { cfAPIBase = oldAPI; srv.Close() })
	return p, srv
}

// The zone is not simply "the last two labels": example.co.uk and delegated
// subdomains both break that guess, so the provider must walk up the labels.
func TestCloudflareFindsTheZoneByWalkingUpLabels(t *testing.T) {
	var asked []string
	p, _ := cfServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/zones") && r.Method == http.MethodGet {
			name := r.URL.Query().Get("name")
			asked = append(asked, name)
			if name == "example.co.uk" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"result":  []map[string]string{{"id": "ZONE1", "name": name}},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true, "result": []map[string]string{},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "result": []map[string]string{}})
	})

	id, err := p.zoneID(context.Background(), "_acme-challenge.a.b.example.co.uk")
	if err != nil {
		t.Fatal(err)
	}
	if id != "ZONE1" {
		t.Errorf("zoneID = %q, want ZONE1", id)
	}
	// It must have tried the longer names first, not jumped to co.uk.
	if len(asked) < 2 || asked[0] != "_acme-challenge.a.b.example.co.uk" {
		t.Errorf("unexpected lookup order: %v", asked)
	}
}

// A previous failed run can leave a stale TXT record; two conflicting values
// make the CA read the wrong one, so Present must clear first.
func TestCloudflarePresentRemovesStaleRecordsFirst(t *testing.T) {
	var deleted, created bool
	var order []string
	p, _ := cfServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/zones") && r.URL.Query().Get("name") != "" && !strings.Contains(r.URL.Path, "dns_records"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"result":  []map[string]string{{"id": "Z", "name": "example.com"}},
			})
		case strings.Contains(r.URL.Path, "dns_records") && r.Method == http.MethodGet:
			order = append(order, "list")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"result":  []map[string]string{{"id": "OLD", "content": "stale"}},
			})
		case strings.Contains(r.URL.Path, "dns_records") && r.Method == http.MethodDelete:
			deleted = true
			order = append(order, "delete")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "result": map[string]string{}})
		case strings.Contains(r.URL.Path, "dns_records") && r.Method == http.MethodPost:
			created = true
			order = append(order, "create")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "result": map[string]string{}})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "result": map[string]string{}})
		}
	})

	if err := p.Present(context.Background(), "_acme-challenge.example.com", "newvalue"); err != nil {
		t.Fatal(err)
	}
	if !deleted || !created {
		t.Fatalf("deleted=%v created=%v, want both", deleted, created)
	}
	// The delete must come BEFORE the create.
	di, ci := indexOf(order, "delete"), indexOf(order, "create")
	if di < 0 || ci < 0 || di > ci {
		t.Errorf("wrong order %v: the stale record must go before the new one", order)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// A Cloudflare error must reach the operator as words, not as a bare failure.
func TestCloudflareSurfacesAPIErrors(t *testing.T) {
	p, _ := cfServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"errors":  []map[string]interface{}{{"code": 9109, "message": "Invalid access token"}},
		})
	})
	err := p.VerifyToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Invalid access token") {
		t.Errorf("error = %v, want the Cloudflare message", err)
	}
}

// The manual provider is what makes non-Cloudflare DNS work at all.
func TestManualProviderHandsTheRecordToTheOperator(t *testing.T) {
	var gotName, gotValue string
	m := &ManualProvider{
		Instruct: func(name, value string) error {
			gotName, gotValue = name, value
			return nil
		},
	}
	if err := m.Present(context.Background(), "_acme-challenge.example.com", "abc"); err != nil {
		t.Fatal(err)
	}
	if gotName != "_acme-challenge.example.com" || gotValue != "abc" {
		t.Errorf("Instruct got (%q,%q)", gotName, gotValue)
	}
	// With no way to reach a human it must fail loudly rather than hang.
	empty := &ManualProvider{}
	if err := empty.Present(context.Background(), "x", "y"); err == nil {
		t.Error("a manual provider with no Instruct should error")
	}
}
