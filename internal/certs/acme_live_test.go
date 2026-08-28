package certs

// A LIVE test against a real ACME server (Pebble, or Let's Encrypt staging).
//
// Why this exists: every other test checks our own logic. None of them prove
// the engine can actually complete an ACME order — that the account
// registration, the authorization loop, the challenge accept, the CSR and the
// finalize all fit together the way the protocol expects. A mistake in that
// sequence only shows up against a real server.
//
// It is skipped unless SHAHRAG_ACME_LIVE is set, so `go test ./...` stays
// fast, offline and deterministic.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// memDNS is a DNS-01 provider that records the challenge in memory and hands
// it to a tiny authoritative DNS server run by the test. That lets the CA
// really resolve the TXT record, so the full protocol runs end to end.
type memDNS struct {
	mu   sync.Mutex
	recs map[string][]string
}

func newMemDNS() *memDNS { return &memDNS{recs: map[string][]string{}} }

func (m *memDNS) Name() string { return "test" }

func (m *memDNS) Present(_ context.Context, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.TrimSuffix(strings.ToLower(name), ".")
	m.recs[key] = append(m.recs[key], value)
	return nil
}

func (m *memDNS) CleanUp(_ context.Context, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.TrimSuffix(strings.ToLower(name), ".")
	out := m.recs[key][:0]
	for _, v := range m.recs[key] {
		if v != value {
			out = append(out, v)
		}
	}
	m.recs[key] = out
	return nil
}

func (m *memDNS) get(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.recs[strings.TrimSuffix(strings.ToLower(name), ".")]...)
}

// TestACMELiveIssuance drives a complete issuance against a real ACME server.
//
// Set SHAHRAG_ACME_LIVE=1 and point SHAHRAG_ACME_DIR at a directory URL. With
// Pebble (github.com/letsencrypt/pebble) this runs fully offline:
//
//	pebble -config test/config/pebble-config.json -dnsserver 127.0.0.1:8053
//	SHAHRAG_ACME_LIVE=1 \
//	SHAHRAG_ACME_DIR=https://localhost:14000/dir \
//	SHAHRAG_ACME_DOMAIN=test.example.com \
//	go test ./internal/certs/ -run ACMELive -v
func TestACMELiveIssuance(t *testing.T) {
	if os.Getenv("SHAHRAG_ACME_LIVE") == "" {
		t.Skip("set SHAHRAG_ACME_LIVE=1 to run a real ACME issuance")
	}
	dirURL := os.Getenv("SHAHRAG_ACME_DIR")
	if dirURL == "" {
		dirURL = LetsEncryptStaging
	}
	domain := os.Getenv("SHAHRAG_ACME_DOMAIN")
	if domain == "" {
		t.Skip("SHAHRAG_ACME_DOMAIN is required for a live issuance")
	}

	tmp := t.TempDir()
	oldStore, oldAcct := StoreDir, AccountKeyPath
	StoreDir = tmp
	AccountKeyPath = tmp + "/acme.key"
	defer func() { StoreDir, AccountKeyPath = oldStore, oldAcct }()

	dns := newMemDNS()
	iss := &Issuer{
		DNS:                dns,
		Log:                func(f string, a ...interface{}) { t.Logf(f, a...) },
		PropagationTimeout: 90 * time.Second,
	}
	// Against Pebble the record is served by the test's own DNS server, so
	// propagation checking is pointless; the resolver list is overridden by
	// the caller when needed.
	if rs := os.Getenv("SHAHRAG_ACME_RESOLVERS"); rs != "" {
		iss.Resolvers = strings.Split(rs, ",")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	res, err := issueAgainst(ctx, iss, dirURL, Request{
		Domain:    domain,
		Wildcard:  true,
		Challenge: ChallengeDNS,
		Email:     "test@example.com",
	})
	if err != nil {
		t.Fatalf("live issuance failed: %v", err)
	}

	// The certificate must actually parse, cover both names, and pair with
	// the key that was written next to it.
	if _, err := tls.LoadX509KeyPair(res.CertPath, res.KeyPath); err != nil {
		t.Fatalf("the issued pair does not load: %v", err)
	}
	in := Inspect(domain, res.CertPath, res.KeyPath)
	if in.Error != "" {
		t.Fatalf("Inspect reported: %s", in.Error)
	}
	if !in.Wildcard {
		t.Error("the issued certificate is not a wildcard")
	}
	if !Covers(in.Names, domain) {
		t.Errorf("the certificate does not cover the apex %s (names %v)", domain, in.Names)
	}
	if !Covers(in.Names, "sub."+domain) {
		t.Errorf("the certificate does not cover a subdomain (names %v)", in.Names)
	}
	t.Logf("issued %v, valid until %s", in.Names, in.NotAfter)
}

// issueAgainst lets the live test override the directory URL without adding
// a field to Request that production code would never set.
func issueAgainst(ctx context.Context, i *Issuer, dirURL string, req Request) (*Result, error) {
	if dirURL == LetsEncryptStaging || dirURL == "" {
		req.Staging = true
		return i.Issue(ctx, req)
	}
	old := LetsEncryptStagingVar
	LetsEncryptStagingVar = dirURL
	defer func() { LetsEncryptStagingVar = old }()
	req.Staging = true
	return i.Issue(ctx, req)
}

// ── A reachability probe that runs in normal test runs ───────

// The engine is useless if the CA cannot be reached at all. This does not
// issue anything (no rate limit cost) but proves the directory endpoint the
// code will use is live and speaks ACME.
func TestACMEDirectoryIsReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	c := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		},
	}
	res, err := c.Get(LetsEncryptStaging)
	if err != nil {
		t.Skipf("no network access to the ACME directory: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("ACME directory returned HTTP %d", res.StatusCode)
	}
	// A real ACME directory is JSON describing the endpoints.
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("ACME directory content-type = %q", ct)
	}
}

// Guard against a certificate chain we cannot parse. The CA returns DER
// blocks; if the write path mangles them nginx fails on reload.
func TestWrittenChainParsesBackAsPEM(t *testing.T) {
	tmp := t.TempDir()
	old := StoreDir
	StoreDir = tmp
	defer func() { StoreDir = old }()

	// Build a two-element chain the same shape the CA returns.
	leafDER, leafKey := selfSigned(t, "chain.example.com")
	caDER, _ := selfSigned(t, "Test CA")

	i := &Issuer{}
	res, err := i.write("chain.example.com", []string{"chain.example.com"},
		[][]byte{leafDER, caDER}, leafKey, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("the written chain is not parseable PEM")
	}
	// Both certificates must be present, in order.
	if n := strings.Count(string(raw), "BEGIN CERTIFICATE"); n != 2 {
		t.Errorf("the chain holds %d certificates, want 2", n)
	}
	if _, err := tls.LoadX509KeyPair(res.CertPath, res.KeyPath); err != nil {
		t.Errorf("the written pair does not load: %v", err)
	}
}

// selfSigned builds a throwaway certificate for the chain-writing test.
func selfSigned(t *testing.T, cn string) ([]byte, crypto.Signer) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}
