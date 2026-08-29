// Package config manages /etc/nginx-panel/config.json with file locking and atomic writes.
// Both the CLI and web UI share this file so changes made in one are visible in the other.
package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Default configuration paths (overridable via env).
var (
	ConfigPath = envOr("SHAHRAG_CONFIG", "/etc/nginx-panel/config.json")
	LockPath   = envOr("SHAHRAG_LOCK", "/tmp/shahrag-config.lock")
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// ── Data model ──────────────────────────────────────────────

type Domain struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
	// ACME describes how this domain's certificate was issued. Absent for a
	// certificate the operator supplied by hand, which is exactly how the
	// panel tells the two apart.
	ACME *CertMeta `json:"acme,omitempty"`
}

type Binding struct {
	Domain    string `json:"domain"`
	Subdomain string `json:"subdomain"`
}

type Service struct {
	LocalPort  int       `json:"local_port"`
	ListenPort int       `json:"listen_port"`
	Path       string    `json:"path"`
	PathOwned  bool      `json:"path_owned"`
	SSLBackend bool      `json:"ssl_backend"`
	Bindings   []Binding `json:"bindings"`
	// Target is the host the request is proxied to. Empty, "localhost" and
	// "127.0.0.1" all mean "a backend on this server" (the historical
	// behaviour); any other value proxies to that host instead, which is
	// what makes an off-server upstream possible.
	Target string `json:"target,omitempty"`

	// Gate puts a challenge in front of this service, so a scanner that
	// requests the URL never receives the backend's HTML, JavaScript or
	// login form — only a neutral page. It is off unless explicitly asked
	// for, so existing services keep behaving exactly as before.
	//
	//	""/"off"  — no gate (default)
	//	"js"      — a one-second interstitial: a tiny script sets a signed
	//	            cookie and reloads. Bots that do not run JavaScript never
	//	            get past it, and a real visitor barely notices.
	//	"secret"  — a form asking for GateSecret. Stronger, but the visitor
	//	            has to know the word.
	Gate string `json:"gate,omitempty"`
	// GateSecret is the word the "secret" mode asks for. Ignored otherwise.
	GateSecret string `json:"gate_secret,omitempty"`

	// ── Gate exceptions ────────────────────────────────────────────────
	// A challenge that blocks EVERYTHING also blocks the things that are
	// meant to be public (a sitemap) and the clients that cannot solve it
	// (a database host on a private link, a mobile app). These three lists
	// carve out the exceptions. All of them are ignored when Gate is off.

	// GateAllowPaths are URI paths served without any challenge, e.g.
	// "/sitemap.xml" or "/robots.txt". This is the ONLY exception that is
	// not spoofable, so it is the right tool for SEO.
	GateAllowPaths []string `json:"gate_allow_paths,omitempty"`

	// GateAllowIPs are addresses or CIDR blocks that skip the challenge —
	// a private VLAN range, an office IP, a monitoring probe. An IP cannot
	// be forged across a real TCP connection, so this is a strong rule.
	GateAllowIPs []string `json:"gate_allow_ips,omitempty"`

	// GateAllowBots lets well-known search-engine crawlers through by
	// User-Agent. Convenient, but a User-Agent is just a header the client
	// chooses: treat it as "let Google index me", never as a security
	// control. Prefer GateAllowPaths where it matters.
	GateAllowBots bool `json:"gate_allow_bots,omitempty"`
	// Legacy fields: configs written by older tooling sometimes stored the
	// domain/subdomain directly on the service instead of in bindings.
	// They are migrated into Bindings on read and never written back.
	Subdomain string `json:"subdomain,omitempty"`
	Domain    string `json:"domain,omitempty"`
}

// RealityService is one SNI routing rule. The panel calls these "SNI
// services" because routing is decided purely by the TLS SNI value; the JSON
// keys keep their historical names so existing configs load unchanged.
type RealityService struct {
	SNI       string `json:"sni"`
	LocalPort int    `json:"local_port"`
	Ports     []int  `json:"ports"`
	// Target is where matching traffic goes.
	//   ""/"localhost"/"127.0.0.1" → 127.0.0.1:<local_port> (a local backend)
	//   any hostname               → that host:<local_port>
	//   PassthroughTarget          → the client's own SNI on <local_port>,
	//                                i.e. a transparent SNI proxy to the
	//                                real site on the internet.
	Target string `json:"target,omitempty"`
}

// PassthroughTarget makes a rule forward to whatever host the client asked
// for (nginx: $ssl_preread_server_name). Used for "route this domain to the
// real internet through my server" rules.
const PassthroughTarget = "$passthrough"

// LocalHostAliases are the values that mean "this machine".
func IsLocalTarget(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}

// ResolveTarget turns a Target value into the host nginx should connect to.
// Local targets always collapse to 127.0.0.1 so generated configs keep the
// exact upstream spelling the CLI panel used.
func ResolveTarget(t string) string {
	if IsLocalTarget(t) {
		return "127.0.0.1"
	}
	return strings.TrimSpace(t)
}

// Gate modes. A scanner that walks the internet looking for known panels
// fingerprints the HTML, the JS bundle names or the login form it gets back.
// A gate means that response is never produced for a client that has not
// first proved it is a browser (GateJS) or knows a word (GateSecret).
const (
	GateOff    = ""       // no challenge — the historical behaviour
	GateJS     = "js"     // one-second interstitial, sets a signed cookie
	GateSecret = "secret" // ask for a shared word first
)

// NormalizeGate maps stored/user input onto a known mode. Anything
// unrecognised becomes GateOff: an unknown value must never silently lock a
// service that the operator believes is reachable.
func NormalizeGate(g string) string {
	switch strings.ToLower(strings.TrimSpace(g)) {
	case GateJS, "javascript":
		return GateJS
	case GateSecret, "password", "word":
		return GateSecret
	}
	return GateOff
}

// GateEnabled reports whether this service sits behind a challenge.
func (s Service) GateEnabled() bool { return NormalizeGate(s.Gate) != GateOff }

type Reality struct {
	Enabled  bool                      `json:"enabled"`
	HTTPPort int                       `json:"http_port"`
	Services map[string]RealityService `json:"services"`
	// Resolvers are the DNS servers nginx uses to resolve a passthrough
	// target at request time. nginx REQUIRES a `resolver` for any upstream
	// that comes from a variable; without it a passthrough rule fails at
	// runtime with "no resolver defined to resolve <host>" even though
	// `nginx -t` reports the config as valid.
	Resolvers []string `json:"resolvers,omitempty"`
}

// DefaultResolvers are used when the config names none. Two independent
// PUBLIC resolvers — see ValidateResolvers for why a local one is refused.
func DefaultResolvers() []string { return []string{"1.1.1.1", "8.8.8.8"} }

// NormalizePath prepares a service path for the generator.
//
// Only the LEADING slash is stripped, because the generator writes it back as
// "location /<path>". A TRAILING slash is deliberate: "/app/" and "/app" are
// different nginx locations, so silently deleting it changed what the
// operator asked for.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

// ── Resolver safety ─────────────────────────────────────────
//
// Pass-through rules resolve their upstream at request time, so nginx needs a
// `resolver`. WHICH resolver matters enormously:
//
//   * The REWRITING resolver (AdGuard, which answers "this domain = my
//     server" so clients arrive here) must never be used by nginx. nginx
//     would be told the real site IS this server and would connect to
//     itself. Reproduced against a real nginx: one request exhausted the
//     worker with "128 worker_connections are not enough".
//
//   * A TRUTH resolver on the same machine is not merely safe, it is the
//     best option. Unbound on 127.0.0.1:5335 returns the REAL address, so
//     there is no loop, lookups never leave the box, and its cache makes
//     repeats free (measured: 0.08 ms).
//
// Both live on 127.0.0.1, so the address alone cannot tell them apart. An
// earlier version guessed by port and assumed the rewriting DNS was always on
// 53 — wrong the moment port 53 is taken by something else and AdGuard is
// moved to 5353, which is a completely normal setup. The guard then called
// the loop-inducing resolver "safe".
//
// Guessing is the mistake. ProbeResolver ASKS the resolver about a domain the
// panel actually relays and looks at the answer: if it replies with an
// address belonging to this machine, it is the rewriting DNS and must not be
// used. That is correct on any port, on any host, with no list to maintain.

// LocalDNSPortHint is only used for the wording of a warning, never for the
// decision itself.
const LocalDNSPortHint = 53

// ValidateResolvers performs the cheap, offline checks: syntax, and the one
// case that is unconditionally wrong (a resolver that is literally this
// server's DNS answering on the default port when nothing else is known).
//
// The authoritative check is ProbeResolver, which needs the network; callers
// that can afford a DNS round trip should use CheckResolverLoop instead.
func ValidateResolvers(list []string) error {
	for _, r := range list {
		host, _, err := splitResolver(r)
		if err != nil {
			return fmt.Errorf("resolver %q is not a valid address: %w", r, err)
		}
		_ = host
	}
	return nil
}

// splitResolver parses "host", "host:port" and "[v6]:port" into its parts.
func splitResolver(r string) (host string, port int, err error) {
	s := strings.TrimSpace(r)
	if s == "" {
		return "", 0, fmt.Errorf("empty")
	}
	port = 53
	if h, p, e := net.SplitHostPort(s); e == nil {
		s = h
		n, e2 := strconv.Atoi(p)
		if e2 != nil {
			return "", 0, fmt.Errorf("bad port %q", p)
		}
		port = n
	}
	s = strings.Trim(s, "[]")
	if s != "localhost" && net.ParseIP(s) == nil {
		return "", 0, fmt.Errorf("not an IP address")
	}
	return s, port, nil
}

// IsLoopbackResolver reports whether a resolver address points at this host.
func IsLoopbackResolver(r string) bool {
	host, _, err := splitResolver(r)
	if err != nil {
		return false
	}
	return isLoopbackOrLocal(host)
}

func isLoopbackOrLocal(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "::1":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}

// ProbeResolver asks `resolver` for `domain` and returns the addresses it
// answers with. It is the empirical way to find out whether a resolver
// carries the panel's own rewrite, on ANY port.
func ProbeResolver(resolver, domain string, timeout time.Duration) ([]string, error) {
	host, port, err := splitResolver(resolver)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", addr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := r.LookupHost(ctx, domain)
	if err != nil {
		return nil, err
	}
	return ips, nil
}

// CheckResolverLoop verifies that `resolver` does NOT answer with one of this
// machine's own addresses for a domain the panel relays — the definition of
// the loop, independent of which port anything runs on.
//
// relayedDomain should be a hostname that has a pass-through rule; when the
// panel has none there is nothing to check.
func CheckResolverLoop(resolver, relayedDomain string, selfIPs []string, timeout time.Duration) error {
	if relayedDomain == "" || len(selfIPs) == 0 {
		return nil
	}
	ips, err := ProbeResolver(resolver, relayedDomain, timeout)
	if err != nil {
		// A resolver that cannot answer is a separate problem; do not block
		// the configuration on a transient DNS failure.
		return nil
	}
	self := map[string]bool{}
	for _, s := range selfIPs {
		self[s] = true
	}
	for _, ip := range ips {
		if self[ip] {
			return fmt.Errorf(
				"resolver %s answers %q with %s, which is THIS server: it is the "+
					"rewriting DNS (AdGuard), so nginx would resolve the relayed "+
					"domain back to itself and loop until it runs out of "+
					"connections. Point the panel at a resolver that returns the "+
					"REAL address — a local Unbound (e.g. 127.0.0.1:5335) is ideal, "+
					"or a public resolver such as 1.1.1.1",
				resolver, relayedDomain, ip)
		}
	}
	return nil
}

// FirstPassthroughDomain returns a hostname that the panel relays, suitable
// for probing a resolver. Wildcards are turned into a concrete name.
func (c *Config) FirstPassthroughDomain() string {
	names := make([]string, 0, len(c.Reality.Services))
	for n := range c.Reality.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc := c.Reality.Services[n]
		if strings.TrimSpace(svc.Target) != PassthroughTarget {
			continue
		}
		sni := strings.TrimSpace(svc.SNI)
		if sni == "" || strings.HasPrefix(sni, "~") {
			continue // a hand-written regex has no single concrete name
		}
		return strings.TrimPrefix(sni, "*.")
	}
	return ""
}

type FakeSite struct {
	Mode       string `json:"mode"`
	Content    string `json:"content"`
	SourcePath string `json:"source_path"`
}

type NginxSettings struct {
	CacheEnabled      bool `json:"cache_enabled"`
	WorkerConnections int  `json:"worker_connections"`
}

type NginxPaths struct {
	OutputPath       string `json:"output_path"`
	StreamOutputPath string `json:"stream_output_path"`
	SSLProtocols     string `json:"ssl_protocols"`
	SSLCiphers       string `json:"ssl_ciphers"`
	FakeDir          string `json:"fake_dir"`
}

type PanelSettings struct {
	Enabled     bool   `json:"enabled"`
	Domain      string `json:"domain"`
	Subdomain   string `json:"subdomain"`
	Cert        string `json:"cert"`
	Key         string `json:"key"`
	LocalPort   int    `json:"local_port"`
	ListenPort  int    `json:"listen_port"`
	Path        string `json:"path"`
	ServiceName string `json:"service_name"`
	Installed   bool   `json:"installed"`
}

type AuthSettings struct {
	Username           string   `json:"username"`
	PasswordHash       string   `json:"password_hash"`
	SessionSecret      string   `json:"session_secret"`
	AllowedIPs         []string `json:"allowed_ips"`
	IPWhitelistEnabled bool     `json:"ip_whitelist_enabled"`
}

type UISettings struct {
	Theme    string `json:"theme"`
	Language string `json:"language"`
	Density  string `json:"density"`
}

type SecuritySettings struct {
	RateLimitEnabled   bool `json:"rate_limit_enabled"`
	RateLimitPerMinute int  `json:"rate_limit_per_minute"`
	SessionTimeoutMins int  `json:"session_timeout_minutes"`
	CSRFEnabled        bool `json:"csrf_enabled"`
	// LockMinutes is the inactivity lock window: after this many minutes
	// without user activity the panel locks and requires a fresh login.
	// -1 = disabled (no lock).
	LockMinutes int `json:"lock_minutes"`
}

type ShahragSection struct {
	Panel    PanelSettings    `json:"panel"`
	Auth     AuthSettings     `json:"auth"`
	UI       UISettings       `json:"ui"`
	Security SecuritySettings `json:"security"`
	ACME     ACMESettings     `json:"acme"`
}

// ACMESettings holds the account-wide certificate options. Per-domain
// choices (wildcard or not, which challenge) live on the Domain itself,
// because a server can legitimately mix them.
type ACMESettings struct {
	// Email receives the CA's expiry warnings. Optional, but without it a
	// forgotten renewal has no second line of defence.
	Email string `json:"email,omitempty"`
	// CloudflareToken enables the automatic DNS-01 flow. It needs exactly
	// one permission, Zone:DNS:Edit, which the UI states explicitly: a
	// Global API Key would hand the panel the whole account.
	CloudflareToken string `json:"cloudflare_token,omitempty"`
	// Staging points issuance at Let's Encrypt's staging CA. Certificates
	// are untrusted by browsers but the rate limits are far looser, so a
	// misconfiguration does not burn the real quota.
	Staging bool `json:"staging,omitempty"`
	// AutoRenew turns on the periodic renewal check.
	AutoRenew bool `json:"auto_renew,omitempty"`
}

// CertMeta records how a domain's certificate was obtained, so "Renew" can
// repeat the same request without asking again.
type CertMeta struct {
	Managed   bool   `json:"managed,omitempty"`   // issued by this panel
	Wildcard  bool   `json:"wildcard,omitempty"`  // *.domain was requested too
	Challenge string `json:"challenge,omitempty"` // dns-01 or http-01
	Issued    string `json:"issued,omitempty"`    // RFC3339, informational
	Staging   bool   `json:"staging,omitempty"`

	// Per-domain credential overrides. Several domains can live in
	// DIFFERENT Cloudflare accounts, so the account-wide values in
	// ShahragSection.ACME are only defaults. Empty means "use the default",
	// which keeps existing configs working untouched.
	Email           string `json:"email,omitempty"`
	CloudflareToken string `json:"cloudflare_token,omitempty"`
}

type Config struct {
	Domains       map[string]Domain  `json:"domains"`
	Services      map[string]Service `json:"services"`
	ListenPorts   []int              `json:"listen_ports"`
	FakeSite      FakeSite           `json:"fake_site"`
	Reality       Reality            `json:"reality"`
	NginxSettings NginxSettings      `json:"nginx_settings"`
	Nginx         NginxPaths         `json:"nginx"`
	Shahrag       ShahragSection     `json:"shahrag"`
}

// ── Manager ─────────────────────────────────────────────────

type Manager struct {
	mu sync.Mutex
}

func New() *Manager {
	m := &Manager{}
	m.ensure()
	return m
}

func (m *Manager) ensure() {
	if err := os.MkdirAll(filepath.Dir(ConfigPath), 0o755); err != nil {
		// Best-effort; operations will fail later with a clear error.
		_ = err
	}
	if _, err := os.Stat(ConfigPath); errors.Is(err, os.ErrNotExist) {
		_ = m.Write(Default())
	}
}

func Default() *Config {
	return &Config{
		Domains:     map[string]Domain{},
		Services:    map[string]Service{},
		ListenPorts: []int{80, 443},
		FakeSite:    FakeSite{Mode: "default"},
		Reality: Reality{
			Enabled:  false,
			HTTPPort: 6038,
			Services: map[string]RealityService{},
		},
		NginxSettings: NginxSettings{CacheEnabled: false, WorkerConnections: 0},
		Nginx: NginxPaths{
			OutputPath:       "/etc/nginx/conf.d/gateway.conf",
			StreamOutputPath: "/etc/nginx/stream-gateway.conf",
			SSLProtocols:     "TLSv1.2 TLSv1.3",
			SSLCiphers:       "HIGH:!aNULL:!MD5",
			FakeDir:          "/var/www/mysite",
		},
		Shahrag: ShahragSection{
			Panel: PanelSettings{ServiceName: "Shahrag"},
			Auth: AuthSettings{
				Username:   "admin",
				AllowedIPs: []string{},
			},
			UI:       UISettings{Theme: "midnight", Language: "fa", Density: "comfortable"},
			Security: SecuritySettings{RateLimitEnabled: true, RateLimitPerMinute: 30, SessionTimeoutMins: 60, CSRFEnabled: true, LockMinutes: 60},
		},
	}
}

// ── File locking ────────────────────────────────────────────

type lockFile struct{ f *os.File }

func (m *Manager) lock() (*lockFile, error) {
	if err := os.MkdirAll(filepath.Dir(LockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		// Mixed-permission environments (e.g. the lock file was created by
		// root and the current process runs unprivileged, or vice versa):
		// fall back to a per-user lock file next to the main one.
		alt := fmt.Sprintf("%s.%d.lock", LockPath, os.Getuid())
		f, err = os.OpenFile(alt, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &lockFile{f: f}, nil
}

func (l *lockFile) unlock() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// ── Read / Write ────────────────────────────────────────────

func (m *Manager) Read() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lf, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer lf.unlock()
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// Persist migrations only when they actually changed the config (e.g.
	// case-variant domain keys are repaired once and never reappear in
	// doctor output or wizard defaults). Plain formatting differences
	// (jq-written files) must NOT trigger a rewrite.
	before, _ := json.MarshalIndent(&c, "", "  ")
	m.migrate(&c)
	if after, err := json.MarshalIndent(&c, "", "  "); err == nil && !bytes.Equal(after, before) {
		_ = m.writeData(after)
	}
	return &c, nil
}

// writeData atomically replaces the config file. The caller must hold the
// file lock (Read/Write do).
func (m *Manager) writeData(data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(ConfigPath), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, ConfigPath)
}

func (m *Manager) Write(c *Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	lf, err := m.lock()
	if err != nil {
		return err
	}
	defer lf.unlock()
	if err := os.MkdirAll(filepath.Dir(ConfigPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: temp file + rename (see writeData).
	return m.writeData(data)
}

// Mutate atomically reads, applies fn, writes back and returns the new config.
func (m *Manager) Mutate(fn func(*Config) error) (*Config, error) {
	c, err := m.Read()
	if err != nil {
		return nil, err
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	if err := m.Write(c); err != nil {
		return nil, err
	}
	return c, nil
}

// mutateVoid is like Mutate but discards the returned config (used by helpers
// that only need to return an error).
func (m *Manager) mutateVoid(fn func(*Config) error) error {
	_, err := m.Mutate(fn)
	return err
}

// migrate fills missing fields for forward-compatibility.
func (m *Manager) migrate(c *Config) {
	def := Default()
	if c.Domains == nil {
		c.Domains = map[string]Domain{}
	}
	if c.Services == nil {
		c.Services = map[string]Service{}
	}
	if c.Reality.Services == nil {
		c.Reality.Services = map[string]RealityService{}
	}
	if c.ListenPorts == nil || len(c.ListenPorts) == 0 {
		c.ListenPorts = def.ListenPorts
	}
	// Fill each nginx path individually: older configs may have only some
	// of them empty (e.g. stream_output_path), and the whole block must
	// not be clobbered just because one field is missing.
	if c.Nginx.OutputPath == "" {
		c.Nginx.OutputPath = def.Nginx.OutputPath
	}
	if c.Nginx.StreamOutputPath == "" {
		c.Nginx.StreamOutputPath = def.Nginx.StreamOutputPath
	}
	if c.Nginx.FakeDir == "" {
		c.Nginx.FakeDir = def.Nginx.FakeDir
	}
	if c.Nginx.SSLProtocols == "" {
		c.Nginx.SSLProtocols = def.Nginx.SSLProtocols
	}
	if c.Nginx.SSLCiphers == "" {
		c.Nginx.SSLCiphers = def.Nginx.SSLCiphers
	}
	if c.Shahrag.Panel.ServiceName == "" {
		c.Shahrag.Panel.ServiceName = "Shahrag"
	}
	if c.Shahrag.UI.Theme == "" {
		c.Shahrag.UI = def.Shahrag.UI
	}
	if c.Shahrag.Auth.AllowedIPs == nil {
		c.Shahrag.Auth.AllowedIPs = []string{}
	}
	if c.Shahrag.Security.RateLimitPerMinute == 0 {
		c.Shahrag.Security = def.Shahrag.Security
	}
	// Old configs lack lock_minutes (0 in JSON). 0 is not a valid setting
	// (-1 = disabled, >= 1 = minutes), so fill the default.
	if c.Shahrag.Security.LockMinutes == 0 {
		c.Shahrag.Security.LockMinutes = def.Shahrag.Security.LockMinutes
	}
	// Legacy service format: domain/subdomain stored directly on the
	// service. Promote them into bindings so the nginx generator sees them
	// (otherwise the service silently produces no server block and the
	// domain serves the fake page instead of the service).
	for name, svc := range c.Services {
		if len(svc.Bindings) == 0 && (svc.Domain != "" || svc.Subdomain != "") {
			svc.Bindings = []Binding{{Domain: svc.Domain, Subdomain: svc.Subdomain}}
		}
		svc.Domain = ""
		svc.Subdomain = ""
		c.Services[name] = svc
	}
	migrateCanonicalDomains(c)
}

// canonicalDomain returns the existing domain key that matches d
// case-insensitively ("" when none exists). Among case-variant matches the
// key with a certificate wins — the empty one is usually a phone
// auto-capitalised duplicate created by the wizard.
func canonicalDomain(domains map[string]Domain, d string) string {
	if d == "" {
		return ""
	}
	// Collect the case-variant keys and sort them, because ranging over a Go
	// map yields a RANDOM order: with two cert-bearing variants
	// ("Sugerdood.com" and "sugerdood.com" both carrying a certificate) the
	// winner changed on every read, so the panel domain and the generated
	// ssl_certificate flapped between two different certificates from one
	// run to the next.
	var variants []string
	for k := range domains {
		if strings.EqualFold(k, d) {
			variants = append(variants, k)
		}
	}
	if len(variants) == 0 {
		return ""
	}
	sort.Strings(variants)

	// 1. The all-lowercase spelling is the canonical hostname (DNS is
	//    case-insensitive and its certificate is the domain-wide one).
	lower := strings.ToLower(d)
	for _, k := range variants {
		if k == lower && domains[k].Cert != "" {
			return k
		}
	}
	// 2. Otherwise the first cert-bearing variant, deterministically.
	for _, k := range variants {
		if domains[k].Cert != "" {
			return k
		}
	}
	// 3. Exact spelling (even without a certificate).
	if _, ok := domains[d]; ok {
		return d
	}
	// 4. Any case-variant key.
	return variants[0]
}

// migrateCanonicalDomains repairs the damage caused by case-variant domain
// keys: a wizard input like "Sugerdood.com" (phone auto-capitalise) used to
// create a second domain key without a certificate. Every service binding
// and the panel domain are rewritten to the canonical key, and empty
// duplicate keys that nothing references are dropped. The generator would
// otherwise skip the cert-less duplicate and serve the fake page.
func migrateCanonicalDomains(c *Config) {
	for name, svc := range c.Services {
		changed := false
		for i, b := range svc.Bindings {
			can := canonicalDomain(c.Domains, b.Domain)
			if can != "" && can != b.Domain {
				svc.Bindings[i].Domain = can
				changed = true
			}
		}
		if changed {
			c.Services[name] = svc
		}
	}
	if c.Shahrag.Panel.Domain != "" {
		if can := canonicalDomain(c.Domains, c.Shahrag.Panel.Domain); can != "" && can != c.Shahrag.Panel.Domain {
			c.Shahrag.Panel.Domain = can
			if c.Shahrag.Panel.Cert == "" {
				c.Shahrag.Panel.Cert = c.Domains[can].Cert
			}
			if c.Shahrag.Panel.Key == "" {
				c.Shahrag.Panel.Key = c.Domains[can].Key
			}
		}
	}
	// Collapse case-variant duplicates onto the canonical key.
	//
	// DNS hostnames are case-insensitive, so "Sugerdood.com" and
	// "sugerdood.com" are the SAME host — keeping both as separate entries
	// is what produced two competing nginx server blocks (nginx then warns
	// "conflicting server name ... ignored" and drops one, taking the
	// services in it offline). Nothing is thrown away: a certificate that
	// only the duplicate carried is moved to the canonical key when that
	// key has none.
	var keys []string
	for k := range c.Domains {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic: map order is random in Go
	for _, k := range keys {
		d, ok := c.Domains[k]
		if !ok {
			continue
		}
		can := canonicalDomain(c.Domains, k)
		if can == "" || can == k {
			continue
		}
		// Preserve a certificate the canonical key is missing.
		cd := c.Domains[can]
		if cd.Cert == "" && d.Cert != "" {
			cd.Cert = d.Cert
			cd.Key = d.Key
			c.Domains[can] = cd
		}
		// Move every reference over, then drop the duplicate.
		for name, svc := range c.Services {
			changed := false
			for i, b := range svc.Bindings {
				if b.Domain == k {
					svc.Bindings[i].Domain = can
					changed = true
				}
			}
			if changed {
				c.Services[name] = svc
			}
		}
		if c.Shahrag.Panel.Domain == k {
			c.Shahrag.Panel.Domain = can
		}
		delete(c.Domains, k)
	}
}

// ── Domain helpers ──────────────────────────────────────────

func (m *Manager) AddDomain(name, cert, key string) error {
	return m.mutateVoid(func(c *Config) error {
		if _, ok := c.Domains[name]; ok {
			return fmt.Errorf("domain %s already exists", name)
		}
		c.Domains[name] = Domain{Cert: cert, Key: key}
		return nil
	})
}

func (m *Manager) DeleteDomain(name string) error {
	return m.mutateVoid(func(c *Config) error {
		if _, ok := c.Domains[name]; !ok {
			return os.ErrNotExist
		}
		for _, svc := range c.Services {
			for _, b := range svc.Bindings {
				if b.Domain == name {
					return fmt.Errorf("domain used by service binding")
				}
			}
		}
		delete(c.Domains, name)
		return nil
	})
}

// ── Service helpers ─────────────────────────────────────────

func (m *Manager) AddService(name, subdomain, domain string, localPort, listenPort int, path string, pathOwned, sslBackend bool) error {
	return m.AddServiceTarget(name, subdomain, domain, localPort, listenPort, path, pathOwned, sslBackend, "")
}

// AddServiceTarget is AddService plus the upstream host. An empty target (or
// localhost/127.0.0.1) keeps the historical local-backend behaviour.
func (m *Manager) AddServiceTarget(name, subdomain, domain string, localPort, listenPort int, path string, pathOwned, sslBackend bool, target string) error {
	return m.mutateVoid(func(c *Config) error {
		if _, ok := c.Services[name]; ok {
			return fmt.Errorf("service %s already exists", name)
		}
		if _, ok := c.Domains[domain]; !ok {
			return fmt.Errorf("domain %s not found", domain)
		}
		path = NormalizePath(path)
		if IsLocalTarget(target) {
			target = "" // store the default compactly
		}
		c.Services[name] = Service{
			LocalPort:  localPort,
			ListenPort: listenPort,
			Path:       path,
			PathOwned:  pathOwned,
			SSLBackend: sslBackend,
			Bindings:   []Binding{{Domain: domain, Subdomain: subdomain}},
			Target:     strings.TrimSpace(target),
		}
		if !containsInt(c.ListenPorts, listenPort) {
			c.ListenPorts = append(c.ListenPorts, listenPort)
		}
		return nil
	})
}

func (m *Manager) DeleteService(name string) error {
	return m.mutateVoid(func(c *Config) error {
		if _, ok := c.Services[name]; !ok {
			return os.ErrNotExist
		}
		delete(c.Services, name)
		return nil
	})
}

func (m *Manager) AddBinding(service, subdomain, domain string) error {
	return m.mutateVoid(func(c *Config) error {
		svc, ok := c.Services[service]
		if !ok {
			return os.ErrNotExist
		}
		if _, ok := c.Domains[domain]; !ok {
			return fmt.Errorf("domain %s not found", domain)
		}
		for _, b := range svc.Bindings {
			if b.Domain == domain && b.Subdomain == subdomain {
				return fmt.Errorf("binding already exists")
			}
		}
		svc.Bindings = append(svc.Bindings, Binding{Domain: domain, Subdomain: subdomain})
		c.Services[service] = svc
		return nil
	})
}

func (m *Manager) RemoveBinding(service string, idx int) error {
	return m.mutateVoid(func(c *Config) error {
		svc, ok := c.Services[service]
		if !ok {
			return os.ErrNotExist
		}
		if idx < 0 || idx >= len(svc.Bindings) {
			return os.ErrNotExist
		}
		svc.Bindings = append(svc.Bindings[:idx], svc.Bindings[idx+1:]...)
		c.Services[service] = svc
		return nil
	})
}

// ── Port helpers ────────────────────────────────────────────

func (m *Manager) AddPort(port int) error {
	return m.mutateVoid(func(c *Config) error {
		if containsInt(c.ListenPorts, port) {
			return fmt.Errorf("port already exists")
		}
		c.ListenPorts = append(c.ListenPorts, port)
		return nil
	})
}

func (m *Manager) DeletePort(port int) error {
	return m.mutateVoid(func(c *Config) error {
		if port == 80 {
			return fmt.Errorf("cannot delete port 80")
		}
		if len(c.ListenPorts) <= 1 {
			return fmt.Errorf("at least one port required")
		}
		for _, s := range c.Services {
			if s.ListenPort == port {
				return fmt.Errorf("port in use by a service")
			}
		}
		out := c.ListenPorts[:0]
		for _, p := range c.ListenPorts {
			if p != port {
				out = append(out, p)
			}
		}
		c.ListenPorts = out
		return nil
	})
}

// ── Random generators ───────────────────────────────────────

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// RandomPath generates a mixed-case alphanumeric token of given length (default 22).
func RandomPath(length int) string {
	if length <= 0 {
		length = 22
	}
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// RandomPort returns a random unused high port in the dynamic range.
func RandomPort(c *Config) int {
	used := map[int]bool{}
	for _, p := range c.ListenPorts {
		used[p] = true
	}
	for _, s := range c.Services {
		used[s.LocalPort] = true
		used[s.ListenPort] = true
	}
	for _, r := range c.Reality.Services {
		used[r.LocalPort] = true
		for _, p := range r.Ports {
			used[p] = true
		}
	}
	used[22] = true
	for i := 0; i < 1000; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(55000))
		p := int(n.Int64()) + 10000
		if !used[p] {
			return p
		}
	}
	return 10000 + int(timeNow().UnixNano()%55000)
}

// SortedPorts returns a sorted copy of listen ports.
func (c *Config) SortedPorts() []int {
	out := append([]int{}, c.ListenPorts...)
	sort.Ints(out)
	return out
}

// IsRealityPort reports whether the given listen port belongs to a Reality service.
func (c *Config) IsRealityPort(port int) bool {
	if !c.Reality.Enabled {
		return false
	}
	for _, r := range c.Reality.Services {
		for _, p := range r.Ports {
			if p == port {
				return true
			}
		}
	}
	return false
}

// EffectivePort returns the actual HTTP listen port (Reality ports map to http_port).
func (c *Config) EffectivePort(port int) int {
	if c.IsRealityPort(port) {
		return c.Reality.HTTPPort
	}
	return port
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
