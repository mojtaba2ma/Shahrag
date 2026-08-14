package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

// ── Panel settings ──────────────────────────────────────────

type panelSettingsReq struct {
	Domain      *string `json:"domain"`
	Subdomain   *string `json:"subdomain"`
	LocalPort   *int    `json:"local_port"`
	ListenPort  *int    `json:"listen_port"`
	Path        *string `json:"path"`
	Cert        *string `json:"cert"`
	Key         *string `json:"key"`
}

func (s *Server) handleGetPanelSettings(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c.Shahrag.Panel)
}

func (s *Server) handleUpdatePanelSettings(w http.ResponseWriter, r *http.Request) {
	var body panelSettingsReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		p := &c.Shahrag.Panel
		svc := c.Services[p.ServiceName]
		if body.Domain != nil {
			p.Domain = *body.Domain
			if len(svc.Bindings) > 0 {
				svc.Bindings[0].Domain = *body.Domain
			}
		}
		if body.Subdomain != nil {
			p.Subdomain = *body.Subdomain
			if len(svc.Bindings) > 0 {
				svc.Bindings[0].Subdomain = *body.Subdomain
			}
		}
		if body.LocalPort != nil {
			p.LocalPort = *body.LocalPort
			svc.LocalPort = *body.LocalPort
		}
		if body.ListenPort != nil {
			p.ListenPort = *body.ListenPort
			svc.ListenPort = *body.ListenPort
			found := false
			for _, pp := range c.ListenPorts {
				if pp == *body.ListenPort {
					found = true
					break
				}
			}
			if !found {
				c.ListenPorts = append(c.ListenPorts, *body.ListenPort)
			}
		}
		if body.Path != nil {
			pp := strings.Trim(*body.Path, "/")
			p.Path = pp
			svc.Path = pp
		}
		if body.Cert != nil && p.Domain != "" {
			cert := strings.TrimRight(strings.TrimSpace(*body.Cert), "/")
			p.Cert = cert
			if d, ok := c.Domains[p.Domain]; ok {
				d.Cert = cert
				c.Domains[p.Domain] = d
			}
		}
		if body.Key != nil && p.Domain != "" {
			key := strings.TrimRight(strings.TrimSpace(*body.Key), "/")
			p.Key = key
			if d, ok := c.Domains[p.Domain]; ok {
				d.Key = key
				c.Domains[p.Domain] = d
			}
		}
		c.Services[p.ServiceName] = svc
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Regenerate nginx
	_, _ = s.gen.GenerateAndReload()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ── Nginx settings ──────────────────────────────────────────

func (s *Server) handleGetNginxSettings(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, map[string]interface{}{
		"cache_enabled":       nginxpkg.CacheEnabled(),
		"worker_connections":  nginxpkg.WorkerConnections(),
		"log_level":           nginxpkg.LogLevel(),
		"status": map[string]interface{}{
			"active":             nginxpkg.IsActive(),
			"version":            nginxpkg.Version(),
			"worker_connections": nginxpkg.WorkerConnections(),
			"cache_enabled":      nginxpkg.CacheEnabled(),
			"log_level":          nginxpkg.LogLevel(),
		},
	})
	_ = c
}

type cacheReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleSetCache(w http.ResponseWriter, r *http.Request) {
	var body cacheReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if err := nginxpkg.SetCache(body.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.cfg.Mutate(func(c *config.Config) error {
		c.NginxSettings.CacheEnabled = body.Enabled
		return nil
	})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type connectionsReq struct {
	WorkerConnections int `json:"worker_connections"`
}

func (s *Server) handleSetConnections(w http.ResponseWriter, r *http.Request) {
	var body connectionsReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if err := nginxpkg.SetWorkerConnections(body.WorkerConnections); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	_, _ = s.cfg.Mutate(func(c *config.Config) error {
		c.NginxSettings.WorkerConnections = body.WorkerConnections
		return nil
	})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type logLevelReq struct {
	Level string `json:"level"`
}

func (s *Server) handleSetLogLevel(w http.ResponseWriter, r *http.Request) {
	var body logLevelReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if err := nginxpkg.SetLogLevel(body.Level); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleReloadNginx(w http.ResponseWriter, r *http.Request) {
	res := s.gen.Reload()
	writeJSON(w, 200, res)
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	res, err := s.gen.GenerateAndReload()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleGenerateTest(w http.ResponseWriter, r *http.Request) {
	res := s.gen.Test()
	writeJSON(w, 200, res)
}

// ── UI preferences ──────────────────────────────────────────

type uiReq struct {
	Theme    *string `json:"theme"`
	Language *string `json:"language"`
	Density  *string `json:"density"`
}

func (s *Server) handleGetUI(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c.Shahrag.UI)
}

func (s *Server) handleSetUI(w http.ResponseWriter, r *http.Request) {
	var body uiReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if body.Theme != nil {
			c.Shahrag.UI.Theme = *body.Theme
		}
		if body.Language != nil {
			c.Shahrag.UI.Language = *body.Language
		}
		if body.Density != nil {
			c.Shahrag.UI.Density = *body.Density
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ── Security settings ───────────────────────────────────────

type securityReq struct {
	AllowedIPs         *[]string `json:"allowed_ips"`
	IPWhitelistEnabled *bool     `json:"ip_whitelist_enabled"`
	RateLimitEnabled   *bool     `json:"rate_limit_enabled"`
	RateLimitPerMinute *int      `json:"rate_limit_per_minute"`
	SessionTimeout     *int      `json:"session_timeout_minutes"`
	CSRFEnabled        *bool     `json:"csrf_enabled"`
}

func (s *Server) handleGetSecurity(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, map[string]interface{}{
		"auth":     c.Shahrag.Auth,
		"security": c.Shahrag.Security,
	})
}

func (s *Server) handleSetSecurity(w http.ResponseWriter, r *http.Request) {
	var body securityReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if body.AllowedIPs != nil {
			c.Shahrag.Auth.AllowedIPs = *body.AllowedIPs
		}
		if body.IPWhitelistEnabled != nil {
			c.Shahrag.Auth.IPWhitelistEnabled = *body.IPWhitelistEnabled
		}
		if body.RateLimitEnabled != nil {
			c.Shahrag.Security.RateLimitEnabled = *body.RateLimitEnabled
		}
		if body.RateLimitPerMinute != nil {
			c.Shahrag.Security.RateLimitPerMinute = *body.RateLimitPerMinute
		}
		if body.SessionTimeout != nil {
			c.Shahrag.Security.SessionTimeoutMins = *body.SessionTimeout
		}
		if body.CSRFEnabled != nil {
			c.Shahrag.Security.CSRFEnabled = *body.CSRFEnabled
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Refresh rate limiter
	if body.RateLimitPerMinute != nil {
		s.limiter.UpdateLimit(*body.RateLimitPerMinute)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ── Backup / Raw ────────────────────────────────────────────

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c)
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, 400, "Invalid config JSON: "+err.Error())
		return
	}
	if c.Domains == nil || c.Services == nil {
		writeErr(w, 400, "Invalid config format")
		return
	}
	if err := s.cfg.Write(&c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.gen.GenerateAndReload()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleGetRaw(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	writeJSON(w, 200, c)
}

func (s *Server) handleSetRaw(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, 400, "Invalid config JSON: "+err.Error())
		return
	}
	if err := s.cfg.Write(&c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = s.gen.GenerateAndReload()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
