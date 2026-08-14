// Package installer implements the first-run setup wizard.
package installer

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"shahrag/internal/config"
	"shahrag/internal/security"
)

// InstallTokenPath is the file holding the one-time install token. install.sh
// creates it (mode 0600) and prints it to the admin; the web wizard must
// present it before POST /api/install/run is accepted. The file is deleted
// when installation completes. It can be overridden for tests.
var InstallTokenPath = envOr("SHAHRAG_INSTALL_TOKEN", "/etc/nginx-panel/.install-token")

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type Params struct {
	HasDomain     bool   `json:"has_domain"`
	Domain        string `json:"domain"`
	Subdomain     string `json:"subdomain"`
	UseCustomCert bool   `json:"use_custom_cert"`
	CertPath      string `json:"cert_path"`
	KeyPath       string `json:"key_path"`
	LocalPort     int    `json:"local_port"`
	PanelPath     string `json:"panel_path"`
	ListenPort    int    `json:"listen_port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
}

type Result struct {
	OK          bool   `json:"ok"`
	ServiceName string `json:"service_name"`
	Domain      string `json:"domain"`
	Subdomain   string `json:"subdomain"`
	LocalPort   int    `json:"local_port"`
	ListenPort  int    `json:"listen_port"`
	Path        string `json:"path"`
	URL         string `json:"url"`
	// Warning carries non-fatal notices (e.g. "no certificate for the panel
	// domain yet") so the wizard can surface them on the success screen.
	Warning string `json:"warning,omitempty"`
}

type Installer struct {
	cfg *config.Manager
}

func New(cfg *config.Manager) *Installer {
	return &Installer{cfg: cfg}
}

func (in *Installer) IsInstalled() bool {
	c, err := in.cfg.Read()
	if err != nil {
		return false
	}
	return c.Shahrag.Panel.Installed
}

// DefaultPanelPort is the port the panel service falls back to when the
// config holds no panel port yet and no random port was requested.
const DefaultPanelPort = 8080

// Defaults returns pre-filled random values for the wizard.
// The local port is RANDOM in the high range (10000–65000) and excludes
// every port already used by the config (services, listen ports, Reality),
// so the panel can never collide with an existing service's port.
func (in *Installer) Defaults() (map[string]interface{}, error) {
	c, err := in.cfg.Read()
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"has_domain":      true,
		"domain":          "",
		"subdomain":       "",
		"use_custom_cert": false,
		"cert_path":       "",
		"key_path":        "",
		"local_port":      config.RandomPort(c),
		"panel_path":      config.RandomPath(22),
		"listen_port":     443,
		"username":        "admin",
	}, nil
}

// TokenRequired reports whether a one-time install token exists (the wizard
// must ask for it).
func TokenRequired() bool {
	_, err := os.Stat(InstallTokenPath)
	return err == nil
}

// VerifyToken compares t against the stored one-time token in constant time.
// It returns an error when the token file is missing (install.sh must create
// it) or the token does not match.
func VerifyToken(t string) error {
	stored, err := os.ReadFile(InstallTokenPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("install token not found — re-run install.sh to provision it")
		}
		return fmt.Errorf("cannot read install token: %w", err)
	}
	stored = []byte(strings.TrimSpace(string(stored)))
	if len(stored) == 0 {
		return fmt.Errorf("install token is empty")
	}
	got := []byte(strings.TrimSpace(t))
	if len(got) == 0 {
		return fmt.Errorf("install token required")
	}
	if subtle.ConstantTimeCompare(got, stored) != 1 {
		return fmt.Errorf("invalid install token")
	}
	return nil
}

// ConsumeToken removes the one-time token file after a successful install.
func ConsumeToken() {
	_ = os.Remove(InstallTokenPath)
}

// WriteToken creates (or refreshes) the one-time install token and returns it.
func WriteToken() (string, error) {
	b := make([]byte, 18) // 36 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(InstallTokenPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(InstallTokenPath, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// PortFree reports whether the given TCP port can be bound on the given host
// ("" = all interfaces). The probe socket is closed immediately.
func PortFree(host string, port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// FindFreePort returns a free port starting at `start` (scanning upward).
func FindFreePort(start int) int {
	for p := start; p < start+1000; p++ {
		if PortFree("0.0.0.0", p) && PortFree("127.0.0.1", p) {
			return p
		}
	}
	return 0
}

// pickFreePort finds a free random high port (10000–65000), preferring
// candidates that are also unused by the config.
func (in *Installer) pickFreePort() int {
	c, _ := in.cfg.Read()
	for i := 0; i < 50; i++ {
		cand := config.RandomPort(c)
		if PortFree("0.0.0.0", cand) && PortFree("127.0.0.1", cand) {
			return cand
		}
	}
	return 0
}

func (in *Installer) Install(p Params) (*Result, error) {
	if in.IsInstalled() {
		return nil, fmt.Errorf("already installed")
	}
	certKept := false
	if !p.HasDomain {
		p.Domain = ""
		p.Subdomain = ""
		p.CertPath = ""
		p.KeyPath = ""
	} else {
		// Hostnames are case-insensitive. Phones often auto-capitalise the
		// first letter ("Sugerdood.com" instead of "sugerdood.com"), which
		// previously created a duplicate domain key without a certificate —
		// the generator then skipped it and the panel URL served the fake
		// page. Normalise to lowercase and reuse the existing domain key.
		p.Domain = strings.ToLower(strings.TrimSpace(p.Domain))
		p.Subdomain = strings.ToLower(strings.TrimSpace(p.Subdomain))
		// Strip trailing slash that some mobile keyboards append
		// (e.g. "/root/cert/full.pem/" -> "/root/cert/full.pem").
		// Never strip the leading slash — it is an absolute path.
		p.CertPath = strings.TrimRight(strings.TrimSpace(p.CertPath), "/")
		p.KeyPath = strings.TrimRight(strings.TrimSpace(p.KeyPath), "/")
		if p.Domain == "" || p.Subdomain == "" {
			return nil, fmt.Errorf("domain and subdomain are required")
		}
		if !p.UseCustomCert && p.CertPath == "" && p.KeyPath == "" {
			// Remain empty — the admin adds a certificate later via the panel.
			// The nginx generator skips domains without a certificate instead
			// of emitting an invalid server block.
		}
	}
	if p.LocalPort <= 0 || p.LocalPort > 65535 {
		return nil, fmt.Errorf("invalid local port")
	}
	// The panel must be able to bind this port. When the requested port is
	// taken by ANOTHER process, do not fail the wizard — pick a free random
	// port in the high range instead (the config keeps the final value, so
	// the nginx generator and the service always agree).
	if !PortFree("0.0.0.0", p.LocalPort) && !portBoundByShahrag(p.LocalPort) {
		if free := in.pickFreePort(); free > 0 {
			p.LocalPort = free
		} else {
			return nil, fmt.Errorf("port %d is in use and no free port could be found", p.LocalPort)
		}
	}
	p.PanelPath = strings.Trim(p.PanelPath, "/")
	if p.PanelPath == "" {
		p.PanelPath = config.RandomPath(22)
	}
	if p.Username == "" {
		p.Username = "admin"
	}
	if len(p.Password) < 6 {
		return nil, fmt.Errorf("password must be at least 6 characters")
	}
	if p.ListenPort == 0 {
		p.ListenPort = 443
	}
	if p.ListenPort < 1 || p.ListenPort > 65535 {
		return nil, fmt.Errorf("invalid listen port")
	}

	_, err := in.cfg.Mutate(func(c *config.Config) error {
		// Register domain if new. The lookup is case-insensitive so a
		// differently-cased input can never create a duplicate domain key.
		// When the domain already exists with certificates, they are KEPT:
		// the wizard's cert step often holds a single-subdomain certificate
		// (e.g. for the panel host) while the existing domain cert covers
		// ALL of that domain's services. Replacing it silently takes every
		// other subdomain down with TLS errors. Wizard-provided paths only
		// fill gaps (empty cert/key).
		if p.Domain != "" {
			canonical := p.Domain
			for k := range c.Domains {
				if strings.EqualFold(k, p.Domain) {
					canonical = k
					break
				}
			}
			p.Domain = canonical
			if d, ok := c.Domains[p.Domain]; ok {
				if p.CertPath == "" {
					p.CertPath = d.Cert
				} else if d.Cert != "" && d.Cert != p.CertPath {
					certKept = true
					p.CertPath = d.Cert
				}
				if p.KeyPath == "" {
					p.KeyPath = d.Key
				} else if d.Key != "" && d.Key != p.KeyPath {
					certKept = true
					p.KeyPath = d.Key
				}
				c.Domains[p.Domain] = config.Domain{Cert: p.CertPath, Key: p.KeyPath}
			} else {
				c.Domains[p.Domain] = config.Domain{Cert: p.CertPath, Key: p.KeyPath}
			}
		}

		// Create Shahrag service (path_owned always true)
		var bindings []config.Binding
		if p.Domain != "" {
			bindings = []config.Binding{{Domain: p.Domain, Subdomain: p.Subdomain}}
		}
		c.Services["Shahrag"] = config.Service{
			LocalPort:  p.LocalPort,
			ListenPort: p.ListenPort,
			Path:       p.PanelPath,
			PathOwned:  true,
			SSLBackend: false,
			Bindings:   bindings,
		}
		if !containsInt(c.ListenPorts, p.ListenPort) {
			c.ListenPorts = append(c.ListenPorts, p.ListenPort)
		}

		// Panel metadata
		c.Shahrag.Panel = config.PanelSettings{
			Enabled:     true,
			Domain:      p.Domain,
			Subdomain:   p.Subdomain,
			Cert:        p.CertPath,
			Key:         p.KeyPath,
			LocalPort:   p.LocalPort,
			ListenPort:  p.ListenPort,
			Path:        p.PanelPath,
			ServiceName: "Shahrag",
			Installed:   true,
		}

		// Auth
		c.Shahrag.Auth.Username = p.Username
		c.Shahrag.Auth.PasswordHash = security.HashPassword(p.Password)
		if c.Shahrag.Auth.SessionSecret == "" {
			c.Shahrag.Auth.SessionSecret = security.GenerateSecret()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	res := &Result{
		OK:          true,
		ServiceName: "Shahrag",
		Domain:      p.Domain,
		Subdomain:   p.Subdomain,
		LocalPort:   p.LocalPort,
		ListenPort:  p.ListenPort,
		Path:        p.PanelPath,
	}
	if certKept {
		res.Warning = "the domain " + p.Domain + " already had a certificate configured; " +
			"the existing certificate was KEPT (the wizard must not replace a domain-wide " +
			"certificate with a single-subdomain one — that would break every other " +
			"subdomain's TLS). To change it, use panel → Domains after installation."
	} else if p.Domain != "" && (p.CertPath == "" || p.KeyPath == "") {
		res.Warning = "the panel domain has no TLS certificate yet — the panel " +
			"will not be reachable through https://" + p.Subdomain + "." + p.Domain +
			" until you add a certificate (panel → Domains). Use the direct port meanwhile."
	}
	if p.Domain != "" {
		res.URL = fmt.Sprintf("https://%s.%s/%s/", p.Subdomain, p.Domain, p.PanelPath)
	} else {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "SERVER_IP"
		}
		res.URL = fmt.Sprintf("http://%s:%d/%s/", hostname, p.LocalPort, p.PanelPath)
	}
	return res, nil
}

// portBoundByShahrag reports whether the port is held by THIS process
// (the panel itself). It matches the listen socket's inode from
// /proc/net/tcp{6} against this process's file descriptors. Go usually
// creates an IPv6 dual-stack socket for wildcard binds, so both tables are
// checked. This lets the wizard accept the port the running panel is
// already bound to without a false "port in use" error.
// Best-effort: any failure means "no".
func portBoundByShahrag(port int) bool {
	hexPort := fmt.Sprintf(":%04X", port)
	var inodes []string
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, hexPort) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// state is the 4th field (0A = TCP_LISTEN), inode the 10th.
			if fields[3] != "0A" {
				continue
			}
			inodes = append(inodes, fields[9])
		}
	}
	if len(inodes) == 0 {
		return false
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return false
	}
	owned := map[string]bool{}
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue
		}
		// links look like "socket:[12345]"
		if strings.HasPrefix(link, "socket:[") {
			owned[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	for _, ino := range inodes {
		if owned[ino] {
			return true
		}
	}
	return false
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
