// Package config manages /etc/nginx-panel/config.json with file locking and atomic writes.
// Both the CLI and web UI share this file so changes made in one are visible in the other.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	// Legacy fields: configs written by older tooling sometimes stored the
	// domain/subdomain directly on the service instead of in bindings.
	// They are migrated into Bindings on read and never written back.
	Subdomain string `json:"subdomain,omitempty"`
	Domain    string `json:"domain,omitempty"`
}

type RealityService struct {
	SNI       string `json:"sni"`
	LocalPort int    `json:"local_port"`
	Ports     []int  `json:"ports"`
}

type Reality struct {
	Enabled  bool                      `json:"enabled"`
	HTTPPort int                       `json:"http_port"`
	Services map[string]RealityService `json:"services"`
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
	// 1. A case-variant key WITH a certificate beats everything.
	for k := range domains {
		if strings.EqualFold(k, d) && domains[k].Cert != "" {
			return k
		}
	}
	// 2. Exact spelling (even without a certificate).
	if _, ok := domains[d]; ok {
		return d
	}
	// 3. Any case-variant key.
	for k := range domains {
		if strings.EqualFold(k, d) {
			return k
		}
	}
	return ""
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
	// Drop empty domain keys that duplicate an existing key by case and
	// that no service references.
	for k, d := range c.Domains {
		if d.Cert != "" || d.Key != "" {
			continue
		}
		used := false
		for _, svc := range c.Services {
			for _, b := range svc.Bindings {
				if b.Domain == k {
					used = true
				}
			}
		}
		if used {
			continue
		}
		for k2 := range c.Domains {
			if k2 != k && strings.EqualFold(k2, k) {
				delete(c.Domains, k)
				break
			}
		}
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
	return m.mutateVoid(func(c *Config) error {
		if _, ok := c.Services[name]; ok {
			return fmt.Errorf("service %s already exists", name)
		}
		if _, ok := c.Domains[domain]; !ok {
			return fmt.Errorf("domain %s not found", domain)
		}
		path = strings.Trim(path, "/")
		if path == "" {
			path = "/"
		}
		c.Services[name] = Service{
			LocalPort:  localPort,
			ListenPort: listenPort,
			Path:       path,
			PathOwned:  pathOwned,
			SSLBackend: sslBackend,
			Bindings:   []Binding{{Domain: domain, Subdomain: subdomain}},
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
