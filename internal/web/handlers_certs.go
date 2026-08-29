package web

// Certificate management endpoints.
//
// The important guarantee here is that an issued certificate is IMMEDIATELY
// usable: the handler writes the paths into the domain's config entry and
// regenerates nginx, so the operator never has to copy a path by hand. If
// that wiring is skipped the panel looks like it worked while nginx keeps
// serving the old (or a missing) certificate.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"shahrag/internal/certs"
	"shahrag/internal/cli"
	"shahrag/internal/config"
)

// issueJob tracks one in-flight issuance.
//
// Issuance takes minutes (DNS propagation alone can be a minute or two), far
// longer than a browser will hold a request open. So the POST starts a job
// and returns immediately; the UI polls for progress. The manual DNS flow
// needs this anyway, because it has to show the operator a record and wait
// for them to create it.
type issueJob struct {
	ID      string
	Domain  string
	State   string // running | waiting_dns | done | error
	Log     []string
	Error   string
	Record  *dnsRecord
	Started time.Time

	mu      sync.Mutex
	confirm chan struct{} // closed when the operator says the record exists
	cancel  context.CancelFunc
}

// dnsRecord is what the manual flow shows the operator.
type dnsRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type jobStore struct {
	mu   sync.Mutex
	jobs map[string]*issueJob
}

var certJobs = &jobStore{jobs: map[string]*issueJob{}}

func (s *jobStore) add(j *issueJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Drop finished jobs older than an hour so the map cannot grow forever.
	for id, old := range s.jobs {
		if old.State != "running" && old.State != "waiting_dns" &&
			time.Since(old.Started) > time.Hour {
			delete(s.jobs, id)
		}
	}
	s.jobs[j.ID] = j
}

func (s *jobStore) get(id string) *issueJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

func (j *issueJob) logf(format string, args ...interface{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Log = append(j.Log, fmt.Sprintf(format, args...))
}

// jobView is the wire representation. It is a SEPARATE type from issueJob on
// purpose: issueJob carries a mutex, and copying that (to return it, or to
// hand it to the JSON encoder) copies lock state — a real race that go vet
// flags.
type jobView struct {
	ID      string     `json:"id"`
	Domain  string     `json:"domain"`
	State   string     `json:"state"`
	Log     []string   `json:"log"`
	Error   string     `json:"error,omitempty"`
	Record  *dnsRecord `json:"record,omitempty"`
	Started time.Time  `json:"started"`
}

func (j *issueJob) snapshot() jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := jobView{
		ID: j.ID, Domain: j.Domain, State: j.State,
		Error: j.Error, Started: j.Started,
		Log: append([]string{}, j.Log...),
	}
	if j.Record != nil {
		r := *j.Record
		out.Record = &r
	}
	return out
}

// ── List ─────────────────────────────────────────────────────

// handleListCerts describes every configured domain's certificate.
func (s *Server) handleListCerts(w http.ResponseWriter, r *http.Request) {
	c, err := s.cfg.Read()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	names := make([]string, 0, len(c.Domains))
	for n := range c.Domains {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]map[string]interface{}, 0, len(names))
	for _, n := range names {
		d := c.Domains[n]
		info := certs.Inspect(n, d.Cert, d.Key)
		item := map[string]interface{}{
			"domain": n, "cert_path": d.Cert, "key_path": d.Key,
			"names": info.Names, "not_before": info.NotBefore,
			"not_after": info.NotAfter, "issuer": info.Issuer,
			"wildcard": info.Wildcard, "self_signed": info.SelfSuper,
			"days_left": info.DaysLeft, "expired": info.Expired,
			"due_renew": info.DueRenew, "error": info.Error,
		}
		// Managed comes from the config, not from a file on disk: it is the
		// panel's own record of having issued this certificate, and it is
		// what decides whether Renew can run unattended.
		item["managed"] = d.ACME != nil && d.ACME.Managed
		if d.ACME != nil {
			// Never ship the stored token to the browser; the UI only needs
			// to know that an override EXISTS so it can show the field as
			// already filled and offer a reset.
			item["acme"] = map[string]interface{}{
				"managed":     d.ACME.Managed,
				"wildcard":    d.ACME.Wildcard,
				"challenge":   d.ACME.Challenge,
				"issued":      d.ACME.Issued,
				"staging":     d.ACME.Staging,
				"email":       d.ACME.Email,
				"has_token":   strings.TrimSpace(d.ACME.CloudflareToken) != "",
				"extra_names": d.ACME.ExtraNames,
			}
		}
		out = append(out, item)
	}

	acme := c.Shahrag.ACME
	writeJSON(w, 200, map[string]interface{}{
		"certs": out,
		"acme": map[string]interface{}{
			"email":      acme.Email,
			"staging":    acme.Staging,
			"auto_renew": acme.AutoRenew,
			// The token itself is never sent to the browser; only whether
			// one is configured. Echoing a stored secret back into a form
			// is how secrets end up in logs and screenshots.
			"cloudflare_configured": strings.TrimSpace(acme.CloudflareToken) != "",
		},
	})
}

// ── ACME settings ────────────────────────────────────────────

type acmeSettingsReq struct {
	Email           *string `json:"email"`
	CloudflareToken *string `json:"cloudflare_token"`
	Staging         *bool   `json:"staging"`
	AutoRenew       *bool   `json:"auto_renew"`
}

func (s *Server) handleUpdateACME(w http.ResponseWriter, r *http.Request) {
	var body acmeSettingsReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}

	// Verify a new token BEFORE storing it, so a typo is reported here
	// rather than three minutes into an issuance.
	if body.CloudflareToken != nil {
		tok := strings.TrimSpace(*body.CloudflareToken)
		if tok != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			if err := certs.NewCloudflareProvider(tok).VerifyToken(ctx); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		}
	}

	if _, err := s.cfg.Mutate(func(c *config.Config) error {
		if body.Email != nil {
			c.Shahrag.ACME.Email = strings.TrimSpace(*body.Email)
		}
		if body.CloudflareToken != nil {
			c.Shahrag.ACME.CloudflareToken = strings.TrimSpace(*body.CloudflareToken)
		}
		if body.Staging != nil {
			c.Shahrag.ACME.Staging = *body.Staging
		}
		if body.AutoRenew != nil {
			c.Shahrag.ACME.AutoRenew = *body.AutoRenew
			// Install the timer the moment it is switched on, so the
			// setting is not a promise the system never keeps.
			if *body.AutoRenew {
				if err := cli.InstallRenewTimer(shahragBinary()); err != nil {
					// Not fatal: the preference is still saved, and the
					// operator can run `shahrag renew-certs` by hand.
					log.Printf("[shahrag] could not install the renewal timer: %v", err)
				}
			}
		}
		return nil
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// ── Issue / renew ────────────────────────────────────────────

type issueReq struct {
	Domain    string `json:"domain"`
	Wildcard  bool   `json:"wildcard"`
	Challenge string `json:"challenge"` // dns-01 | http-01
	Method    string `json:"method"`    // cloudflare | manual
	Staging   *bool  `json:"staging"`

	// Per-request overrides. Someone can easily hold several domains in
	// DIFFERENT Cloudflare accounts, so the account-wide token in Settings
	// is only a default: the issue dialog may supply its own. Empty means
	// "use the stored value", which is also why the token is a pointer —
	// an empty string is a meaningful "leave it alone", not "clear it".
	Email           *string `json:"email"`
	CloudflareToken *string `json:"cloudflare_token"`
	// Remember stores the overrides on the domain so a later renewal (and
	// the unattended timer) repeats them without asking again.
	Remember bool `json:"remember"`

	// ExtraNames are additional SANs. A wildcard covers ONE label, so
	// panel.app.example.com needs its own name or a nested wildcard
	// (*.app.example.com). Only the operator knows which levels they use.
	ExtraNames []string `json:"extra_names"`
}

func (s *Server) handleIssueCert(w http.ResponseWriter, r *http.Request) {
	var body issueReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(body.Domain))
	domain = strings.TrimPrefix(domain, "*.")
	if domain == "" {
		writeErr(w, 400, "a domain is required")
		return
	}

	c, err := s.cfg.Read()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, ok := c.Domains[domain]; !ok {
		writeErr(w, 404, "unknown domain: add it on the Domains page first")
		return
	}

	challenge := body.Challenge
	if challenge == "" {
		challenge = certs.ChallengeDNS
	}
	if body.Wildcard && challenge != certs.ChallengeDNS {
		writeErr(w, 400, "a wildcard certificate requires the DNS challenge")
		return
	}

	// Validate the extra names up front. A CA refuses a SAN outside the
	// authorised domain, and discovering that after several DNS challenges
	// wastes minutes and rate-limit budget.
	extraNames, err := certs.ValidateExtraNames(domain, body.ExtraNames)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Any extra name that is itself a wildcard needs DNS validation.
	for _, n := range extraNames {
		if strings.HasPrefix(n, "*.") && challenge != certs.ChallengeDNS {
			writeErr(w, 400, "the wildcard "+n+" requires the DNS challenge")
			return
		}
	}

	staging := c.Shahrag.ACME.Staging
	if body.Staging != nil {
		staging = *body.Staging
	}

	// Resolve the credentials for THIS issuance.
	//
	// Precedence: what the dialog sent > what was saved on the domain from a
	// previous issuance > the account-wide default in Settings. The middle
	// step is what lets the renewal timer keep working for a domain that
	// lives in a different Cloudflare account.
	dom := c.Domains[domain]
	email := c.Shahrag.ACME.Email
	token := c.Shahrag.ACME.CloudflareToken
	if dom.ACME != nil {
		if dom.ACME.Email != "" {
			email = dom.ACME.Email
		}
		if dom.ACME.CloudflareToken != "" {
			token = dom.ACME.CloudflareToken
		}
	}
	if body.Email != nil && strings.TrimSpace(*body.Email) != "" {
		email = strings.TrimSpace(*body.Email)
	}
	if body.CloudflareToken != nil && strings.TrimSpace(*body.CloudflareToken) != "" {
		token = strings.TrimSpace(*body.CloudflareToken)
	}

	job := &issueJob{
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Domain:  domain,
		State:   "running",
		Started: time.Now(),
		confirm: make(chan struct{}),
	}

	// Pick the DNS provider. Cloudflare needs a token; the manual flow needs
	// somewhere to publish the record for the operator to read.
	var provider certs.DNSProvider
	if challenge == certs.ChallengeDNS {
		switch body.Method {
		case "manual":
			provider = &certs.ManualProvider{
				Instruct: func(name, value string) error {
					job.mu.Lock()
					job.Record = &dnsRecord{Name: name, Type: "TXT", Value: value}
					job.State = "waiting_dns"
					job.mu.Unlock()
					job.logf("create this TXT record, then press Continue")
					// Block until the operator confirms, or the job is
					// cancelled. Without a timeout the goroutine would leak
					// if the browser is simply closed.
					select {
					case <-job.confirm:
						job.mu.Lock()
						job.State = "running"
						job.mu.Unlock()
						return nil
					case <-time.After(30 * time.Minute):
						return fmt.Errorf("timed out waiting for the DNS record to be created")
					}
				},
				Cleanup: func(name, value string) {
					job.logf("you may now delete the TXT record %s", name)
				},
			}
		default:
			if strings.TrimSpace(token) == "" {
				writeErr(w, 400, "no Cloudflare token configured — add one here or in Settings, or choose the manual method")
				return
			}
			provider = certs.NewCloudflareProvider(token)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	job.cancel = cancel
	certJobs.add(job)

	// Only persist an override that actually differs from the account-wide
	// default; storing a copy of the same value would quietly go stale when
	// the operator later updates Settings.
	keep := certOverrides{}
	if body.Remember {
		if email != c.Shahrag.ACME.Email {
			keep.Email = email
		}
		if token != c.Shahrag.ACME.CloudflareToken {
			keep.Token = token
		}
	}

	go s.runIssue(ctx, cancel, job, certs.Request{
		Domain:     domain,
		Wildcard:   body.Wildcard,
		Challenge:  challenge,
		Email:      email,
		Staging:    staging,
		ExtraNames: extraNames,
	}, provider, keep)

	writeJSON(w, 200, map[string]interface{}{"ok": true, "job": job.ID})
}

// runIssue performs the issuance and, on success, WIRES THE RESULT IN.
//
// The wiring is the part that makes the feature real: the certificate paths
// are written into the domain's config entry and nginx is regenerated, so
// the new certificate is actually served. Skipping it would leave a valid
// certificate on disk that nothing uses.
// certOverrides are the per-domain credentials to persist on success.
type certOverrides struct {
	Email string
	Token string
}

func (s *Server) runIssue(ctx context.Context, cancel context.CancelFunc,
	job *issueJob, req certs.Request, provider certs.DNSProvider,
	keep certOverrides) {

	defer cancel()

	iss := &certs.Issuer{
		DNS:      provider,
		Log:      job.logf,
		HTTPRoot: s.acmeWebroot(),
	}
	res, err := iss.Issue(ctx, req)
	if err != nil {
		job.mu.Lock()
		job.State = "error"
		job.Error = err.Error()
		job.mu.Unlock()
		job.logf("failed: %v", err)
		return
	}

	// Install into the config so nginx uses it.
	if _, err := s.cfg.Mutate(func(c *config.Config) error {
		d, ok := c.Domains[job.Domain]
		if !ok {
			return fmt.Errorf("the domain disappeared while the certificate was being issued")
		}
		d.Cert = res.CertPath
		d.Key = res.KeyPath
		meta := &config.CertMeta{
			Managed:   true,
			Wildcard:  req.Wildcard,
			Challenge: req.Challenge,
			Issued:    time.Now().Format(time.RFC3339),
			Staging:   req.Staging,
		}
		// Carry forward any override this domain already had, unless this
		// run supplied a new one.
		if d.ACME != nil {
			meta.Email, meta.CloudflareToken = d.ACME.Email, d.ACME.CloudflareToken
		}
		if keep.Email != "" {
			meta.Email = keep.Email
		}
		if keep.Token != "" {
			meta.CloudflareToken = keep.Token
		}
		// Persist the extra names so a renewal — including the unattended
		// timer — produces a certificate covering the same hosts. Losing
		// them would silently shrink the certificate at renewal time.
		meta.ExtraNames = req.ExtraNames
		d.ACME = meta
		c.Domains[job.Domain] = d
		return nil
	}); err != nil {
		job.mu.Lock()
		job.State = "error"
		job.Error = "the certificate was issued but could not be saved: " + err.Error()
		job.mu.Unlock()
		return
	}
	job.logf("installed for %s", job.Domain)

	// Regenerate so the new paths reach nginx. A failure here is reported
	// but does NOT mark the job failed: the certificate is valid and saved,
	// and the generator already rolls back a bad config on its own.
	if s.gen != nil {
		if _, err := s.gen.GenerateAndReload(); err != nil {
			job.logf("warning: nginx could not be reloaded: %v", err)
			job.logf("the certificate is saved — fix the error and press Generate")
		} else {
			job.logf("nginx regenerated and reloaded")
		}
	}

	job.mu.Lock()
	job.State = "done"
	job.mu.Unlock()
}

// acmeWebroot is where an HTTP-01 token is written. It has to be a directory
// nginx already serves, which is the fake-site root.
func (s *Server) acmeWebroot() string {
	c, err := s.cfg.Read()
	if err != nil || c.Nginx.FakeDir == "" {
		return "/var/www/mysite"
	}
	return c.Nginx.FakeDir
}

func (s *Server) handleCertJob(w http.ResponseWriter, r *http.Request) {
	job := certJobs.get(r.PathValue("id"))
	if job == nil {
		writeErr(w, 404, "unknown job")
		return
	}
	snap := job.snapshot()
	writeJSON(w, 200, snap)
}

// handleConfirmDNS unblocks a manual issuance once the operator has created
// the TXT record.
func (s *Server) handleConfirmDNS(w http.ResponseWriter, r *http.Request) {
	job := certJobs.get(r.PathValue("id"))
	if job == nil {
		writeErr(w, 404, "unknown job")
		return
	}
	job.mu.Lock()
	state := job.State
	job.mu.Unlock()
	if state != "waiting_dns" {
		writeErr(w, 400, "this job is not waiting for a DNS record")
		return
	}
	// close() is safe once; a double confirm would panic, so guard it.
	select {
	case <-job.confirm:
	default:
		close(job.confirm)
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handleDeleteCert detaches a certificate from a domain.
//
// The files are deliberately left on disk: deleting the only copy of a
// certificate because someone clicked the wrong row is not recoverable,
// whereas an unreferenced file costs nothing.
func (s *Server) handleDeleteCert(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.PathValue("domain"))
	if _, err := s.cfg.Mutate(func(c *config.Config) error {
		d, ok := c.Domains[name]
		if !ok {
			return errNotFound
		}
		d.Cert, d.Key, d.ACME = "", "", nil
		c.Domains[name] = d
		return nil
	}); err != nil {
		if err == errNotFound {
			writeErr(w, 404, "unknown domain")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// shahragBinary is the path the renewal timer should invoke.
func shahragBinary() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "/usr/local/bin/shahrag"
}
