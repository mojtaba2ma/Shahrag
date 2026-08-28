package certs

// Reading what is already installed.
//
// The panel must describe a certificate honestly even when the panel did not
// issue it: an operator may have pasted in a Cloudflare Origin certificate or
// run certbot by hand, and the list has to show that correctly rather than
// pretending the file is missing.

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Inspect reads a certificate file and reports what it contains.
//
// certPath/keyPath come from the domain's config entry, so this works for
// certificates the panel issued AND for ones the operator supplied.
func Inspect(domain, certPath, keyPath string) Info {
	in := Info{
		Domain:   strings.ToLower(strings.TrimSpace(domain)),
		CertPath: certPath,
		KeyPath:  keyPath,
	}
	if strings.TrimSpace(certPath) == "" || strings.TrimSpace(keyPath) == "" {
		in.Error = "no certificate configured"
		return in
	}

	raw, err := os.ReadFile(certPath)
	if err != nil {
		// The most common real-world state: the config points somewhere the
		// file no longer is. Say that plainly instead of a bare errno.
		if os.IsNotExist(err) {
			in.Error = "the certificate file does not exist"
		} else {
			in.Error = "cannot read the certificate: " + err.Error()
		}
		return in
	}
	leaf, err := firstCert(raw)
	if err != nil {
		in.Error = err.Error()
		return in
	}

	in.Names = append([]string{}, leaf.DNSNames...)
	if len(in.Names) == 0 && leaf.Subject.CommonName != "" {
		in.Names = []string{leaf.Subject.CommonName}
	}
	sort.Strings(in.Names)
	for _, n := range in.Names {
		if strings.HasPrefix(n, "*.") {
			in.Wildcard = true
			break
		}
	}
	in.NotBefore = leaf.NotBefore
	in.NotAfter = leaf.NotAfter
	in.Issuer = leaf.Issuer.CommonName
	if in.Issuer == "" {
		in.Issuer = leaf.Issuer.String()
	}
	// A self-signed certificate is worth flagging: browsers will refuse it,
	// and an operator who does not realise the wizard generated a placeholder
	// spends a long time debugging "the site is broken".
	in.SelfSuper = leaf.Issuer.String() == leaf.Subject.String()

	left := time.Until(leaf.NotAfter)
	in.DaysLeft = int(left.Hours() / 24)
	in.Expired = left <= 0
	in.DueRenew = left < RenewBefore

	// Managed means this panel issued it, which decides whether "Renew" can
	// work unattended.
	if _, statErr := os.Stat(CertDir(in.Domain) + "/meta.json"); statErr == nil {
		mc, _ := Paths(in.Domain)
		in.Managed = samePath(mc, certPath)
	}

	// A cert/key mismatch produces an nginx reload failure that reads like a
	// syntax error. Catching it here turns it into one clear sentence.
	if err := checkPair(certPath, keyPath); err != nil {
		in.Error = err.Error()
	}
	return in
}

func samePath(a, b string) bool {
	ra, err1 := absClean(a)
	rb, err2 := absClean(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return ra == rb
}

func absClean(p string) (string, error) {
	r, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	_ = r
	return p, nil
}

func firstCert(raw []byte) (*x509.Certificate, error) {
	rest := raw
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return nil, fmt.Errorf("the file contains no PEM certificate")
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the certificate is unreadable: %w", err)
		}
		return c, nil
	}
}

// checkPair verifies the private key really belongs to the certificate.
func checkPair(certPath, keyPath string) error {
	if _, err := os.Stat(keyPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("the private key file does not exist")
		}
		return fmt.Errorf("cannot read the private key: %w", err)
	}
	// Reuse the standard library's own pairing check rather than
	// reimplementing per-algorithm comparisons.
	if _, err := loadX509KeyPair(certPath, keyPath); err != nil {
		return fmt.Errorf("the certificate and private key do not match")
	}
	return nil
}

// Covers reports whether a certificate's SAN list covers a hostname,
// including wildcard semantics.
//
// This is what lets the panel warn "this cert does not actually cover
// app.example.com" before nginx serves a name mismatch to real visitors.
func Covers(names []string, host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return false
	}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == h {
			return true
		}
		if strings.HasPrefix(n, "*.") {
			// A wildcard matches exactly ONE label, and never the apex:
			// *.example.com covers a.example.com but not example.com and
			// not a.b.example.com.
			base := n[2:]
			rest, ok := strings.CutSuffix(h, "."+base)
			if ok && rest != "" && !strings.Contains(rest, ".") {
				return true
			}
		}
	}
	return false
}

// ExpirySummary is the dashboard's one-line health check.
type ExpirySummary struct {
	Total    int      `json:"total"`
	Expired  []string `json:"expired"`
	DueSoon  []string `json:"due_soon"`
	Problems []string `json:"problems"`
}

// Summarise folds a list of Info into something a dashboard card can show.
func Summarise(list []Info) ExpirySummary {
	s := ExpirySummary{Total: len(list)}
	for _, in := range list {
		switch {
		case in.Error != "":
			s.Problems = append(s.Problems, in.Domain)
		case in.Expired:
			s.Expired = append(s.Expired, in.Domain)
		case in.DueRenew:
			s.DueSoon = append(s.DueSoon, in.Domain)
		}
	}
	return s
}

// loadX509KeyPair is tls.LoadX509KeyPair, kept behind a small wrapper so the
// import stays local to this file.
func loadX509KeyPair(certFile, keyFile string) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(certFile, keyFile)
}
