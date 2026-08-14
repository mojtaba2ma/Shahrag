// Package installer implements the first-run setup wizard.
package installer

import (
	"fmt"
	"os"
	"strings"

	"shahrag/internal/config"
	"shahrag/internal/security"
)

type Params struct {
	HasDomain      bool   `json:"has_domain"`
	Domain         string `json:"domain"`
	Subdomain      string `json:"subdomain"`
	UseCustomCert  bool   `json:"use_custom_cert"`
	CertPath       string `json:"cert_path"`
	KeyPath        string `json:"key_path"`
	LocalPort      int    `json:"local_port"`
	PanelPath      string `json:"panel_path"`
	ListenPort     int    `json:"listen_port"`
	Username       string `json:"username"`
	Password       string `json:"password"`
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

// Defaults returns pre-filled random values for the wizard.
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

func (in *Installer) Install(p Params) (*Result, error) {
	if in.IsInstalled() {
		return nil, fmt.Errorf("already installed")
	}
	if !p.HasDomain {
		p.Domain = ""
		p.Subdomain = ""
		p.CertPath = ""
		p.KeyPath = ""
	} else {
		p.Domain = strings.TrimSpace(p.Domain)
		p.Subdomain = strings.TrimSpace(p.Subdomain)
		// Strip leading/trailing whitespace and stray slashes that
		// mobile keyboards may append (e.g. "/root/cert/full.pem/").
		// Strip trailing slash that some mobile keyboards append
		// (e.g. "/root/cert/full.pem/" -> "/root/cert/full.pem").
		// Never strip the leading slash — it is an absolute path.
		p.CertPath = strings.TrimRight(strings.TrimSpace(p.CertPath), "/")
		p.KeyPath = strings.TrimRight(strings.TrimSpace(p.KeyPath), "/")
		if p.Domain == "" || p.Subdomain == "" {
			return nil, fmt.Errorf("domain and subdomain are required")
		}
		// If the user supplied cert/key paths explicitly, honor them
		// regardless of the UseCustomCert checkbox.
		if !p.UseCustomCert && p.CertPath == "" && p.KeyPath == "" {
			// Nothing — remain empty (user will get cert later via Let's Encrypt
			// or set paths in the panel).
		}
	}
	if p.LocalPort <= 0 || p.LocalPort > 65535 {
		return nil, fmt.Errorf("invalid local port")
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

	_, err := in.cfg.Mutate(func(c *config.Config) error {
		// Register domain if new
		if p.Domain != "" {
			if _, ok := c.Domains[p.Domain]; !ok {
				c.Domains[p.Domain] = config.Domain{Cert: p.CertPath, Key: p.KeyPath}
			} else if p.CertPath != "" || p.KeyPath != "" {
				d := c.Domains[p.Domain]
				if p.CertPath != "" {
					d.Cert = p.CertPath
				}
				if p.KeyPath != "" {
					d.Key = p.KeyPath
				}
				c.Domains[p.Domain] = d
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

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
