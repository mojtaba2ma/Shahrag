// Package config manages /etc/nginx-panel/config.json with file locking and atomic writes.
// Both the CLI and web UI share this file so changes made in one are visible in the other.
package config

import (
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
	LocalPort   int       `json:"local_port"`
	ListenPort  int       `json:"listen_port"`
	Path        string    `json:"path"`
	PathOwned   bool      `json:"path_owned"`
	SSLBackend  bool      `json:"ssl_backend"`
	Bindings    []Binding `json:"bindings"`
}

type RealityService struct {
	SNI       string `json:"sni"`
	LocalPort int    `json:"local_port"`
	Ports     []int  `json:"ports"`
}

type Reality struct {
	Enabled    bool                      `json:"enabled"`
	HTTPPort   int                       `json:"http_port"`
	Services   map[string]RealityService `json:"services"`
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
	Enabled      bool   `json:"enabled"`
	Domain       string `json:"domain"`
	Subdomain    string `json:"subdomain"`
	Cert         string `json:"cert"`
	Key          string `json:"key"`
	LocalPort    int    `json:"local_port"`
	ListenPort   int    `json:"listen_port"`
	Path         string `json:"path"`
	ServiceName  string `json:"service_name"`
	Installed    bool   `json:"installed"`
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
	RateLimitEnabled    bool `json:"rate_limit_enabled"`
	RateLimitPerMinute  int  `json:"rate_limit_per_minute"`
	SessionTimeoutMins  int  `json:"session_timeout_minutes"`
	CSRFEnabled         bool `json:"csrf_enabled"`
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
			Security: SecuritySettings{RateLimitEnabled: true, RateLimitPerMinute: 30, SessionTimeoutMins: 60, CSRFEnabled: true},
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
		return nil, err
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
	m.migrate(&c)
	return &c, nil
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
	// Atomic write: temp file + rename.
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
	if c.Nginx.OutputPath == "" {
		c.Nginx = def.Nginx
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
