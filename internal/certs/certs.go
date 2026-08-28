// Package certs issues and renews TLS certificates from an ACME provider
// (Let's Encrypt by default).
//
// Two challenge types, because they solve different problems:
//
//   - HTTP-01 proves control of ONE hostname by serving a token at
//     /.well-known/acme-challenge/. It needs port 80 reachable from the
//     internet and cannot produce a wildcard.
//   - DNS-01 proves control of the whole ZONE by publishing a TXT record.
//     It is the ONLY way to get a wildcard (*.example.com), and it works
//     even when port 80 is firewalled or behind Cloudflare's proxy.
//
// The panel defaults to wildcard/DNS-01 because the typical Shahrag setup
// puts several subdomains behind one domain and sits behind Cloudflare,
// where HTTP-01 to the origin is often not reachable at all.
package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Directory URLs. Staging has far looser rate limits and issues untrusted
// certificates; it exists so a misconfiguration does not burn the real
// quota (5 failures per account per hostname per hour).
const (
	LetsEncryptProd    = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// LetsEncryptStagingVar is the directory used when Request.Staging is set.
//
// It defaults to the real Let's Encrypt staging CA. SHAHRAG_ACME_DIRECTORY
// overrides it, which exists so the project can run its end-to-end tests
// against a local ACME server (Pebble) instead of hammering a public CA.
// Nothing in the product sets that variable.
var LetsEncryptStagingVar = func() string {
	if v := strings.TrimSpace(os.Getenv("SHAHRAG_ACME_DIRECTORY")); v != "" {
		return v
	}
	return LetsEncryptStaging
}()

// StoreDir is where issued material lives. One directory per domain:
//
//	/etc/nginx-panel/certs/<domain>/{fullchain.pem,privkey.pem,meta.json}
var StoreDir = "/etc/nginx-panel/certs"

// AccountKeyPath is the ACME account key. It identifies us to the CA and
// must survive across issuances, or every run registers a new account and
// walks straight into the per-account rate limit.
var AccountKeyPath = "/etc/nginx-panel/acme-account.key"

// RenewBefore is how long before expiry a certificate is considered due.
// Let's Encrypt issues for 90 days and recommends renewing at 30.
const RenewBefore = 30 * 24 * time.Hour

// Challenge kinds, as stored in the config and shown in the UI.
const (
	ChallengeDNS  = "dns-01"
	ChallengeHTTP = "http-01"
)

// DNSProvider publishes and removes the TXT record that proves zone control.
//
// An interface rather than a hard-coded Cloudflare call, because the manual
// flow is the same algorithm with a human in the middle — and because a
// future provider (or a test) only has to satisfy these three methods.
type DNSProvider interface {
	// Present publishes a TXT record for name (already the full
	// _acme-challenge.<domain> form) with the given value.
	Present(ctx context.Context, name, value string) error
	// CleanUp removes it again. Failures here are logged, never fatal: the
	// certificate is already issued by then.
	CleanUp(ctx context.Context, name, value string) error
	// Name identifies the provider in logs and in the UI.
	Name() string
}

// Request describes one issuance.
type Request struct {
	Domain string // apex or hostname, e.g. "example.com" or "app.example.com"
	// Wildcard asks for *.<Domain> IN ADDITION to <Domain>. Requires DNS-01;
	// the ACME protocol has no other way to validate a wildcard.
	Wildcard  bool
	Challenge string // ChallengeDNS or ChallengeHTTP
	Email     string // contact for expiry warnings; optional but recommended
	Staging   bool   // use the staging CA
	// KeyType selects the certificate key. EC is smaller and faster and is
	// what modern clients prefer; RSA exists for very old clients.
	KeyType string // "ec" (default) or "rsa"
}

// Result reports what was written.
type Result struct {
	Domain    string    `json:"domain"`
	Names     []string  `json:"names"`
	CertPath  string    `json:"cert_path"`
	KeyPath   string    `json:"key_path"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Issuer    string    `json:"issuer"`
	Staging   bool      `json:"staging"`
}

// Info describes a certificate already on disk.
type Info struct {
	Domain    string    `json:"domain"`
	Names     []string  `json:"names"`
	CertPath  string    `json:"cert_path"`
	KeyPath   string    `json:"key_path"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Issuer    string    `json:"issuer"`
	Wildcard  bool      `json:"wildcard"`
	SelfSuper bool      `json:"self_signed"`
	DaysLeft  int       `json:"days_left"`
	Expired   bool      `json:"expired"`
	DueRenew  bool      `json:"due_renew"`
	Managed   bool      `json:"managed"` // issued by this panel
	Error     string    `json:"error,omitempty"`
}

// ── Account key ──────────────────────────────────────────────

// accountKey loads the ACME account key, creating it on first use.
//
// Reusing the key matters: a new key means a new account, and Let's Encrypt
// caps new registrations per IP. Losing it is not fatal but wastes quota.
func accountKey() (crypto.Signer, error) {
	if b, err := os.ReadFile(AccountKeyPath); err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("account key %s is not valid PEM", AccountKeyPath)
		}
		k, err := x509.ParseECPrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("account key %s is unreadable: %w", AccountKeyPath, err)
		}
		return k, nil
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(AccountKeyPath), 0o700); err != nil {
		return nil, err
	}
	// 0600: this key is an authentication credential.
	if err := os.WriteFile(AccountKeyPath, pem.EncodeToMemory(
		&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// ── Naming ───────────────────────────────────────────────────

// NamesFor returns the SAN list for a request, deduplicated and ordered.
func NamesFor(domain string, wildcard bool) []string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, "*.")
	if d == "" {
		return nil
	}
	if !wildcard {
		return []string{d}
	}
	// Both names go on ONE certificate, in ONE order.
	//
	// A wildcard SAN does not match the apex: *.example.com is valid for
	// app.example.com but NOT for example.com. So asking only for the
	// wildcard would leave the bare domain serving a name-mismatch error.
	// Requesting both together is also strictly better than two separate
	// certificates: one order, one renewal, one file for nginx to load.
	//
	// Note the remaining limit, which no certificate can avoid: a wildcard
	// covers exactly one label, so a.b.example.com needs its own name.
	return []string{d, "*." + d}
}

// authDomain is the zone a TXT record must be published in. For a wildcard
// the challenge is still on the base domain.
func authDomain(name string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "*.")
}

// CertDir is where a domain's material lives.
func CertDir(domain string) string {
	return filepath.Join(StoreDir, strings.ToLower(strings.TrimSpace(domain)))
}

// Paths returns the certificate and key paths for a domain.
func Paths(domain string) (cert, key string) {
	d := CertDir(domain)
	return filepath.Join(d, "fullchain.pem"), filepath.Join(d, "privkey.pem")
}
