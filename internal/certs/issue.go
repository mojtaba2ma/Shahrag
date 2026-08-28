package certs

// The issuance flow itself.
//
// The order of operations is deliberate and each step exists for a reason
// learned from a real failure mode:
//
//  1. register (or reuse) the account   — a new account per run hits the CA's
//                                          registration rate limit
//  2. authorize every name              — a wildcard and its apex are TWO
//                                          separate authorizations
//  3. publish the challenge             — and for DNS-01, WAIT for the record
//                                          to actually be visible; asking the
//                                          CA to check a record that has not
//                                          propagated is the #1 cause of a
//                                          "failed" issuance that would have
//                                          worked 30 seconds later
//  4. finalize with a fresh key         — never reuse a private key
//  5. write atomically, then verify     — a half-written cert would take nginx
//                                          down on the next reload

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// Logf receives human-readable progress. The CLI prints it; the web handler
// collects it so the dialog can show what happened.
type Logf func(format string, args ...interface{})

// Issuer performs ACME issuance.
type Issuer struct {
	DNS DNSProvider // required for DNS-01
	Log Logf
	// HTTPRoot is the directory an HTTP-01 token is written into. nginx must
	// serve it at /.well-known/acme-challenge/.
	HTTPRoot string
	// PropagationTimeout bounds the wait for a TXT record to become visible.
	PropagationTimeout time.Duration
	// Resolvers are queried when checking propagation. Public resolvers are
	// used on purpose: what matters is what the CA will see, not what this
	// machine's (possibly rewriting) local resolver says.
	Resolvers []string
}

func (i *Issuer) logf(f string, a ...interface{}) {
	if i.Log != nil {
		i.Log(f, a...)
	}
}

// Issue obtains a certificate and writes it under StoreDir.
func (i *Issuer) Issue(ctx context.Context, req Request) (*Result, error) {
	names := NamesFor(req.Domain, req.Wildcard)
	if len(names) == 0 {
		return nil, fmt.Errorf("no domain given")
	}
	if req.Wildcard && req.Challenge != ChallengeDNS {
		// Not a preference — the ACME spec forbids it. Saying so plainly
		// beats letting the CA return a confusing error later.
		return nil, fmt.Errorf("a wildcard certificate requires the DNS challenge")
	}
	if req.Challenge == ChallengeDNS && i.DNS == nil {
		return nil, fmt.Errorf("the DNS challenge needs a DNS provider (Cloudflare token, or use the manual flow)")
	}

	akey, err := accountKey()
	if err != nil {
		return nil, err
	}
	dir := LetsEncryptProd
	if req.Staging {
		dir = LetsEncryptStagingVar
	}
	client := &acme.Client{Key: akey, DirectoryURL: dir, HTTPClient: acmeHTTPClient()}

	i.logf("using %s", dir)
	acct := &acme.Account{}
	if req.Email != "" {
		acct.Contact = []string{"mailto:" + req.Email}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil {
		// An account that already exists is the normal case on every run
		// after the first, so it must not be treated as a failure.
		if err != acme.ErrAccountAlreadyExists {
			return nil, fmt.Errorf("ACME registration failed: %w", err)
		}
		i.logf("reusing the existing ACME account")
	} else {
		i.logf("registered a new ACME account")
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return nil, fmt.Errorf("could not create the order: %w", err)
	}
	i.logf("order created for %s", strings.Join(names, ", "))

	// Track what we published so cleanup can run even on failure.
	type published struct{ name, value string }
	var pubs []published
	defer func() {
		for _, p := range pubs {
			if err := i.DNS.CleanUp(context.Background(), p.name, p.value); err != nil {
				i.logf("warning: could not remove the TXT record %s: %v", p.name, err)
			}
		}
	}()

	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("could not read an authorization: %w", err)
		}
		if authz.Status == acme.StatusValid {
			i.logf("%s is already authorized", authz.Identifier.Value)
			continue
		}

		want := ChallengeDNS
		if req.Challenge == ChallengeHTTP {
			want = ChallengeHTTP
		}
		var chal *acme.Challenge
		for _, c := range authz.Challenges {
			if c.Type == want {
				chal = c
				break
			}
		}
		if chal == nil {
			return nil, fmt.Errorf("the CA offered no %s challenge for %s",
				want, authz.Identifier.Value)
		}

		switch want {
		case ChallengeDNS:
			val, err := client.DNS01ChallengeRecord(chal.Token)
			if err != nil {
				return nil, err
			}
			rec := "_acme-challenge." + authDomain(authz.Identifier.Value)
			i.logf("publishing %s", rec)
			if err := i.DNS.Present(ctx, rec, val); err != nil {
				return nil, fmt.Errorf("could not publish the TXT record: %w", err)
			}
			pubs = append(pubs, published{rec, val})

			// Waiting here is what makes this reliable. The CA queries
			// authoritative DNS; if we accept the challenge before the
			// record is live it fails permanently for this order.
			if err := i.waitForTXT(ctx, rec, val); err != nil {
				return nil, err
			}

		case ChallengeHTTP:
			body, err := client.HTTP01ChallengeResponse(chal.Token)
			if err != nil {
				return nil, err
			}
			p, err := i.writeHTTPToken(chal.Token, body)
			if err != nil {
				return nil, err
			}
			defer os.Remove(p)
			i.logf("serving the token at /.well-known/acme-challenge/%s", chal.Token)
		}

		if _, err := client.Accept(ctx, chal); err != nil {
			return nil, fmt.Errorf("the CA rejected the challenge: %w", err)
		}
		if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
			return nil, fmt.Errorf("validation of %s failed: %w",
				authz.Identifier.Value, err)
		}
		i.logf("%s validated", authz.Identifier.Value)
	}

	// Wait for the order to leave "pending" before finalizing.
	orderURI := order.URI
	order, err = client.WaitOrder(ctx, orderURI)
	if err != nil {
		return nil, fmt.Errorf("the order did not become ready: %w", err)
	}

	// A brand-new key for every issuance. Reusing one means a leaked key
	// stays useful to an attacker across renewals.
	certKey, err := newCertKey(req.KeyType)
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: names[0]},
		DNSNames: names,
	}, certKey)
	if err != nil {
		return nil, fmt.Errorf("could not build the CSR: %w", err)
	}

	// Finalize, then fetch the chain ourselves.
	//
	// CreateOrderCert would be the obvious call, but it re-polls using the
	// order URI taken from the finalize response's Location header — and
	// that header is optional, so with some CAs (Pebble omits it) the URI is
	// empty and the request fails with `Post "": unsupported protocol
	// scheme`. Polling the URI we already hold avoids depending on it. A
	// live test against a real ACME server caught this; a mocked one would
	// have passed.
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		if !strings.Contains(err.Error(), "unsupported protocol scheme") {
			return nil, fmt.Errorf("the CA did not issue the certificate: %w", err)
		}
		final, werr := client.WaitOrder(ctx, orderURI)
		if werr != nil {
			return nil, fmt.Errorf("the order did not complete: %w", werr)
		}
		if final.CertURL == "" {
			return nil, fmt.Errorf("the CA finalized the order but returned no certificate URL")
		}
		der, err = client.FetchCert(ctx, final.CertURL, true)
		if err != nil {
			return nil, fmt.Errorf("could not download the certificate: %w", err)
		}
	}
	i.logf("certificate issued")

	return i.write(req.Domain, names, der, certKey, req.Staging)
}

// waitForTXT polls public resolvers until the record is visible, so the CA
// is only asked to look once the answer is actually there.
func (i *Issuer) waitForTXT(ctx context.Context, name, want string) error {
	timeout := i.PropagationTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	resolvers := i.Resolvers
	if len(resolvers) == 0 {
		resolvers = DefaultPropagationResolvers()
	}

	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		seen := 0
		for _, rs := range resolvers {
			if txtHas(ctx, rs, name, want) {
				seen++
			}
		}
		if seen == len(resolvers) {
			i.logf("the TXT record is visible on all %d resolvers", len(resolvers))
			return nil
		}
		if attempt == 1 || attempt%6 == 0 {
			i.logf("waiting for DNS propagation (%d/%d resolvers see it)...",
				seen, len(resolvers))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("the TXT record %s did not become visible within %s — "+
		"check that the record really exists in your DNS zone", name, timeout)
}

func txtHas(ctx context.Context, resolver, name, want string) bool {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", resolver)
		},
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	vals, err := r.LookupTXT(c, name)
	if err != nil {
		return false
	}
	for _, v := range vals {
		if strings.TrimSpace(v) == want {
			return true
		}
	}
	return false
}

func (i *Issuer) writeHTTPToken(token, body string) (string, error) {
	root := i.HTTPRoot
	if root == "" {
		return "", fmt.Errorf("no webroot configured for the HTTP challenge")
	}
	dir := filepath.Join(root, ".well-known", "acme-challenge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, token)
	// World-readable on purpose: nginx serves it as an ordinary file.
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func newCertKey(kind string) (crypto.Signer, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "rsa":
		return rsa.GenerateKey(rand.Reader, 2048)
	default:
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
}

// write stores the chain and key atomically.
//
// Atomicity is not academic here: nginx reads these files on reload, and a
// truncated chain means the reload fails and the site is down. Everything is
// written to a temp file in the same directory and renamed into place.
func (i *Issuer) write(domain string, names []string, der [][]byte,
	key crypto.Signer, staging bool) (*Result, error) {

	dir := CertDir(domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	certPath, keyPath := Paths(domain)

	var chain strings.Builder
	for _, b := range der {
		if err := pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: b}); err != nil {
			return nil, err
		}
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	// Parse before writing: refusing to install something unreadable is far
	// better than discovering it when nginx will not start.
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, fmt.Errorf("the CA returned an unreadable certificate: %w", err)
	}

	if err := atomicWrite(certPath, []byte(chain.String()), 0o644); err != nil {
		return nil, err
	}
	// 0600: the private key must not be world-readable.
	if err := atomicWrite(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	res := &Result{
		Domain:    strings.ToLower(domain),
		Names:     names,
		CertPath:  certPath,
		KeyPath:   keyPath,
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
		Issuer:    leaf.Issuer.CommonName,
		Staging:   staging,
	}
	meta, _ := json.MarshalIndent(res, "", "  ")
	_ = atomicWrite(filepath.Join(dir, "meta.json"), meta, 0o644)

	i.logf("written to %s (valid until %s)", certPath,
		leaf.NotAfter.Format("2006-01-02"))
	return res, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// fsync before rename, or a crash can leave an empty file behind.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// FingerprintSHA256 is shown in the UI so an operator can confirm which
// certificate a service is actually serving.
func FingerprintSHA256(der []byte) string {
	sum := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// acmeHTTPClient returns the client used to talk to the CA.
//
// Normally nil, which makes x/crypto/acme use its own default with full
// certificate verification. SHAHRAG_ACME_INSECURE exists ONLY so the
// project's end-to-end tests can talk to a local Pebble instance, which
// serves its API under a self-signed certificate. It is never set in
// production, and skipping verification against a real CA would be a
// serious downgrade.
func acmeHTTPClient() *http.Client {
	if os.Getenv("SHAHRAG_ACME_INSECURE") != "1" {
		return nil
	}
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// DefaultPropagationResolvers are the DNS servers queried while waiting for a
// challenge record to appear.
//
// PUBLIC resolvers on purpose: what matters is what the CA will see, not what
// this machine's own (possibly rewriting) resolver says. A Shahrag box often
// runs AdGuard answering "this domain = me", which would report success for a
// record that does not exist anywhere.
//
// SHAHRAG_ACME_RESOLVERS overrides the list for the project's end-to-end
// tests, which use a private domain no public resolver can see.
func DefaultPropagationResolvers() []string {
	if v := strings.TrimSpace(os.Getenv("SHAHRAG_ACME_RESOLVERS")); v != "" {
		out := []string{}
		for _, r := range strings.Split(v, ",") {
			if r = strings.TrimSpace(r); r != "" {
				if !strings.Contains(r, ":") {
					r += ":53"
				}
				out = append(out, r)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"1.1.1.1:53", "8.8.8.8:53"}
}
