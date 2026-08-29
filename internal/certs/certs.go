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
	"regexp"
	"sort"
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

	// ExtraNames are additional SANs. A wildcard covers ONE label, so a
	// host like panel.app.example.com needs either its own name here or a
	// nested wildcard (*.app.example.com). Every entry must be inside
	// Domain; ValidateExtraNames enforces that before the order is placed.
	ExtraNames []string
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
//
// extra holds names the operator added by hand. They exist because a
// wildcard matches exactly ONE label: *.example.com covers app.example.com
// but not panel.app.example.com. A deeper host therefore needs either its
// own name or its own nested wildcard (*.app.example.com), and only the
// operator knows which levels they actually use — there is no way to infer
// it, and asking for every possible depth would be absurd.
func NamesFor(domain string, wildcard bool, extra ...string) []string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, "*.")
	if d == "" {
		return nil
	}

	out := []string{}
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(n, ".")))
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}

	add(d)
	if wildcard {
		// Both names go on ONE certificate, in ONE order. A wildcard SAN
		// does not match the apex, so asking only for the wildcard would
		// leave the bare domain serving a name-mismatch error.
		add("*." + d)
	}
	for _, e := range extra {
		add(e)
	}
	return out
}

// ExtraNameError describes why an additional name was refused.
type ExtraNameError struct {
	Name   string
	Reason string
}

func (e ExtraNameError) Error() string { return e.Name + ": " + e.Reason }

// ValidateExtraNames checks operator-supplied SANs against the rules the CA
// will apply anyway, so a mistake is reported in the form instead of failing
// the order minutes later.
//
// Every name must sit inside the domain being issued: a CA will not put
// other-example.com on a certificate authorised only for example.com, and
// silently dropping it would produce a certificate that does not cover what
// the operator asked for.
func ValidateExtraNames(domain string, extra []string) ([]string, error) {
	base := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "*.")))
	if base == "" {
		return nil, ExtraNameError{Name: domain, Reason: "no domain given"}
	}

	clean := []string{}
	seen := map[string]bool{}
	for _, raw := range extra {
		n := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
		if n == "" {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true

		body := strings.TrimPrefix(n, "*.")
		if body == "" {
			return nil, ExtraNameError{Name: n, Reason: "not a hostname"}
		}
		// A wildcard is only meaningful as the FIRST label. "a.*.b" is not
		// a thing, and a CA rejects it.
		if strings.Contains(body, "*") {
			return nil, ExtraNameError{Name: n,
				Reason: "a * is only allowed as the first label, e.g. *.app." + base}
		}
		if !hostRe.MatchString(body) {
			return nil, ExtraNameError{Name: n, Reason: "not a valid hostname"}
		}
		// Must be the domain itself or something under it.
		if body != base && !strings.HasSuffix(body, "."+base) {
			return nil, ExtraNameError{Name: n,
				Reason: "must be " + base + " or a subdomain of it"}
		}
		clean = append(clean, n)
	}
	sort.Strings(clean)
	return clean, nil
}

// hostRe accepts a DNS hostname: labels of letters, digits and hyphens,
// hyphen not at either end of a label.
var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CoveredBy reports whether the names already on a certificate make an
// additional name redundant. Used to tell the operator "you do not need
// this one" instead of quietly enlarging the order.
func CoveredBy(names []string, host string) bool { return Covers(names, host) }

// SuggestParentWildcard returns the wildcard that would cover host, or "" if
// host is already at wildcard depth for base.
//
// This is what turns the one-label rule from a wall into a next step: an
// operator who types panel.app.example.com is told they can add
// *.app.example.com and cover every sibling at once.
func SuggestParentWildcard(base, host string) string {
	base = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(base, "*.")))
	host = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(host, "*.")))
	if base == "" || host == "" || host == base || !strings.HasSuffix(host, "."+base) {
		return ""
	}
	prefix := strings.TrimSuffix(host, "."+base)
	labels := strings.Split(prefix, ".")
	if len(labels) < 2 {
		return "" // already covered by *.base
	}
	// Drop the leftmost label and wildcard the rest.
	return "*." + strings.Join(labels[1:], ".") + "." + base
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
