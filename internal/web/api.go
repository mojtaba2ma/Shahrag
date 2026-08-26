package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/installer"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/security"
	"shahrag/internal/stats"
	"shahrag/internal/systemd"
)

// Server holds all dependencies for the HTTP API.
type Server struct {
	cfg       *config.Manager
	gen       *nginxpkg.Generator
	installer *installer.Installer
	stats     *stats.Collector
	session   *security.Session
	limiter   *security.RateLimiter
	mux       *http.ServeMux
	// boundPort is the TCP port this server instance listens on. It is used
	// to decide whether a panel-port change requires a service restart.
	boundPort int
	// inactivity tracking for the auto-lock feature (per session token).
	sessMu     sync.Mutex
	lastActive map[string]time.Time
}

// hardSessionCap is the absolute maximum lifetime of a session token. The
// practical lifetime is governed by the sliding inactivity window
// (LockMinutes) — the hard cap only prevents a token from living forever
// when the lock is disabled.
const hardSessionCap = 7 * 24 * 60 // minutes

// cookieMaxAgeSeconds is the browser-side cookie lifetime. It is LONG and
// re-issued on every authenticated request, so a page refresh NEVER logs
// the user out; the actual logout is enforced by the inactivity lock
// (server-side lastActive + client idle timer).
const cookieMaxAgeSeconds = 30 * 24 * 3600

func NewServer(cfg *config.Manager, gen *nginxpkg.Generator, inst *installer.Installer,
	st *stats.Collector, boundPort int) *Server {
	c, _ := cfg.Read()
	secret := "shahrag-dev"
	if c != nil && c.Shahrag.Auth.SessionSecret != "" {
		secret = c.Shahrag.Auth.SessionSecret
	}
	s := &Server{
		cfg:        cfg,
		gen:        gen,
		installer:  inst,
		stats:      st,
		session:    security.NewSession(secret),
		limiter:    security.NewRateLimiter(30),
		mux:        http.NewServeMux(),
		boundPort:  boundPort,
		lastActive: map[string]time.Time{},
	}
	go s.sessionGC()
	s.refreshSessionSecret()
	s.routes()
	return s
}

// scheduleRestartIfPortChanged restarts the shahrag service after a short
// delay when the configured panel port no longer matches the port this
// server instance is bound to. The delay lets the current HTTP response
// reach the client before systemd kills the process.
func (s *Server) scheduleRestartIfPortChanged(newPort int) {
	if newPort <= 0 || newPort == s.boundPort {
		return
	}
	time.AfterFunc(2*time.Second, func() {
		_ = systemd.Restart(systemd.UnitName)
	})
}

// refreshSessionSecret re-reads the secret from config (in case it changed).
func (s *Server) refreshSessionSecret() {
	c, err := s.cfg.Read()
	if err == nil && c.Shahrag.Auth.SessionSecret != "" {
		s.session = security.NewSession(c.Shahrag.Auth.SessionSecret)
		s.limiter.UpdateLimit(c.Shahrag.Security.RateLimitPerMinute)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The UI is always served from the same origin as the API (directly on
	// the panel port or through the nginx panel path), so no CORS headers
	// are needed — and a wildcard Access-Control-Allow-Origin would let any
	// website read responses, so we deliberately send none.
	s.mux.ServeHTTP(w, r)
}

// ── Helpers ─────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}

func readJSON(r *http.Request, dst interface{}) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// clientIP extracts real client IP.
func (s *Server) clientIP(r *http.Request) string {
	return security.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
}

// ── Security middleware ─────────────────────────────────────

func (s *Server) withSecurity(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.cfg.Read()
		if err == nil {
			auth := c.Shahrag.Auth
			if auth.IPWhitelistEnabled && len(auth.AllowedIPs) > 0 {
				ip := s.clientIP(r)
				if !security.IPInList(ip, auth.AllowedIPs) {
					writeErr(w, 403, "Access denied: IP not allowed")
					return
				}
			}
			if c.Shahrag.Security.RateLimitEnabled {
				if r.Method != "GET" && r.Method != "HEAD" {
					if ok, retry := s.limiter.Check(s.clientIP(r)); !ok {
						w.Header().Set("Retry-After", strconv.Itoa(retry))
						writeErr(w, 429, "Too many requests")
						return
					}
				}
			}
		}
		next(w, r)
	}
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return s.withSecurity(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("shahrag_session")
		if err != nil {
			writeErr(w, 401, "Authentication required")
			return
		}
		claims, err := s.session.Verify(cookie.Value)
		if err != nil {
			writeErr(w, 401, "Invalid or expired session")
			return
		}

		// Inactivity lock (sliding window). Background polls
		// (?_poll=1) do not count as activity, so the panel locks after
		// LockMinutes without REAL user interaction even if the page is
		// open and polling.
		lock := 0
		if c, err := s.cfg.Read(); err == nil {
			lock = c.Shahrag.Security.LockMinutes
		}
		isPoll := r.URL.Query().Get("_poll") == "1"
		if lock > 0 {
			s.sessMu.Lock()
			last, ok := s.lastActive[cookie.Value]
			s.sessMu.Unlock()
			if ok && time.Since(last) > time.Duration(lock)*time.Minute {
				s.sessMu.Lock()
				delete(s.lastActive, cookie.Value)
				s.sessMu.Unlock()
				writeErr(w, 401, "Session locked due to inactivity — please log in again")
				return
			}
		}
		if !isPoll {
			// Background polls do NOT count as user activity.
			s.sessMu.Lock()
			s.lastActive[cookie.Value] = time.Now()
			s.sessMu.Unlock()
		}
		// Re-issue the cookie on EVERY authenticated request (polls
		// included) with a long lifetime: refreshes and continuous use
		// must never drop the session. Locking is handled by lastActive
		// above, not by cookie expiry.
		http.SetCookie(w, &http.Cookie{
			Name:     "shahrag_session",
			Value:    cookie.Value,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   cookieMaxAgeSeconds,
		})

		r = r.WithContext(withUser(r.Context(), claims))
		next(w, r)
	})
}

// sessionGC periodically drops stale inactivity entries.
func (s *Server) sessionGC() {
	for {
		time.Sleep(10 * time.Minute)
		s.sessMu.Lock()
		cutoff := time.Now().Add(-48 * time.Hour)
		for k, v := range s.lastActive {
			if v.Before(cutoff) {
				delete(s.lastActive, k)
			}
		}
		s.sessMu.Unlock()
	}
}

type contextKey string

const userKey contextKey = "user"

func withUser(ctx interface{ Value(any) any }, claims *security.SessionClaims) interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(any) any
} {
	// In real code we'd use context.WithValue; using a simpler wrapper for brevity.
	return &userContext{Context: ctx.(interface {
		Deadline() (time.Time, bool)
		Done() <-chan struct{}
		Err() error
		Value(any) any
	}), claims: claims}
}

type userContext struct {
	Context interface {
		Deadline() (time.Time, bool)
		Done() <-chan struct{}
		Err() error
		Value(any) any
	}
	claims *security.SessionClaims
}

func (u *userContext) Value(key any) any {
	if key == userKey {
		return u.claims
	}
	return u.Context.Value(key)
}
func (u *userContext) Deadline() (time.Time, bool) { return u.Context.Deadline() }
func (u *userContext) Done() <-chan struct{}       { return u.Context.Done() }
func (u *userContext) Err() error                  { return u.Context.Err() }

// userClaims extracts claims from context (or nil).
func userClaims(r *http.Request) *security.SessionClaims {
	v := r.Context().Value(userKey)
	if c, ok := v.(*security.SessionClaims); ok {
		return c
	}
	return nil
}

// ── Routes ──────────────────────────────────────────────────

func (s *Server) routes() {
	// Public
	s.mux.HandleFunc("/api/health", s.withSecurity(s.handleHealth))
	s.mux.HandleFunc("GET /api/install/status", s.withSecurity(s.handleInstallStatus))
	s.mux.HandleFunc("POST /api/install/run", s.withSecurity(s.handleInstallRun))

	// Auth
	s.mux.HandleFunc("POST /api/auth/login", s.withSecurity(s.handleLogin))
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	s.mux.HandleFunc("POST /api/auth/change-password", s.requireAuth(s.handleChangePassword))

	// Domains
	s.mux.HandleFunc("GET /api/domains", s.requireAuth(s.handleListDomains))
	s.mux.HandleFunc("POST /api/domains", s.requireAuth(s.handleCreateDomain))
	s.mux.HandleFunc("GET /api/domains/{name}", s.requireAuth(s.handleGetDomain))
	s.mux.HandleFunc("PUT /api/domains/{name}", s.requireAuth(s.handleUpdateDomain))
	s.mux.HandleFunc("DELETE /api/domains/{name}", s.requireAuth(s.handleDeleteDomain))

	// Services
	s.mux.HandleFunc("GET /api/services", s.requireAuth(s.handleListServices))
	s.mux.HandleFunc("POST /api/services", s.requireAuth(s.handleCreateService))
	s.mux.HandleFunc("GET /api/services/{name}", s.requireAuth(s.handleGetService))
	s.mux.HandleFunc("PUT /api/services/{name}", s.requireAuth(s.handleUpdateService))
	s.mux.HandleFunc("DELETE /api/services/{name}", s.requireAuth(s.handleDeleteService))
	s.mux.HandleFunc("POST /api/services/{name}/bindings", s.requireAuth(s.handleAddBinding))
	s.mux.HandleFunc("PUT /api/services/{name}/bindings", s.requireAuth(s.handleSetBindings))
	s.mux.HandleFunc("DELETE /api/services/{name}/bindings/{index}", s.requireAuth(s.handleRemoveBinding))

	// Ports
	s.mux.HandleFunc("GET /api/ports", s.requireAuth(s.handleListPorts))
	s.mux.HandleFunc("POST /api/ports", s.requireAuth(s.handleAddPort))
	s.mux.HandleFunc("DELETE /api/ports/{port}", s.requireAuth(s.handleDeletePort))

	// Fake site
	s.mux.HandleFunc("GET /api/fakesite", s.requireAuth(s.handleGetFakeSite))
	s.mux.HandleFunc("PUT /api/fakesite", s.requireAuth(s.handleSetFakeSite))

	// Reality
	s.mux.HandleFunc("GET /api/reality", s.requireAuth(s.handleGetReality))
	s.mux.HandleFunc("PUT /api/reality", s.requireAuth(s.handleUpdateReality))
	s.mux.HandleFunc("POST /api/reality/services", s.requireAuth(s.handleCreateRealityService))
	s.mux.HandleFunc("PUT /api/reality/services/{name}", s.requireAuth(s.handleUpdateRealityService))
	s.mux.HandleFunc("DELETE /api/reality/services/{name}", s.requireAuth(s.handleDeleteRealityService))
	s.mux.HandleFunc("POST /api/reality/services/{name}/ports", s.requireAuth(s.handleAddRealityPort))
	s.mux.HandleFunc("DELETE /api/reality/services/{name}/ports/{port}", s.requireAuth(s.handleRemoveRealityPort))

	// Settings
	s.mux.HandleFunc("GET /api/settings/panel", s.requireAuth(s.handleGetPanelSettings))
	s.mux.HandleFunc("PUT /api/settings/panel", s.requireAuth(s.handleUpdatePanelSettings))
	s.mux.HandleFunc("GET /api/settings/nginx", s.requireAuth(s.handleGetNginxSettings))
	s.mux.HandleFunc("PUT /api/settings/nginx/cache", s.requireAuth(s.handleSetCache))
	s.mux.HandleFunc("PUT /api/settings/nginx/connections", s.requireAuth(s.handleSetConnections))
	s.mux.HandleFunc("PUT /api/settings/nginx/log-level", s.requireAuth(s.handleSetLogLevel))
	s.mux.HandleFunc("POST /api/settings/nginx/reload", s.requireAuth(s.handleReloadNginx))
	s.mux.HandleFunc("POST /api/settings/generate", s.requireAuth(s.handleGenerate))
	s.mux.HandleFunc("POST /api/settings/generate-test", s.requireAuth(s.handleGenerateTest))
	s.mux.HandleFunc("GET /api/settings/ui", s.requireAuth(s.handleGetUI))
	s.mux.HandleFunc("PUT /api/settings/ui", s.requireAuth(s.handleSetUI))
	s.mux.HandleFunc("GET /api/settings/security", s.requireAuth(s.handleGetSecurity))
	s.mux.HandleFunc("PUT /api/settings/security", s.requireAuth(s.handleSetSecurity))
	s.mux.HandleFunc("GET /api/settings/backup", s.requireAuth(s.handleBackup))
	s.mux.HandleFunc("POST /api/settings/restore", s.requireAuth(s.handleRestore))
	s.mux.HandleFunc("GET /api/settings/raw", s.requireAuth(s.handleGetRaw))
	s.mux.HandleFunc("PUT /api/settings/raw", s.requireAuth(s.handleSetRaw))

	// Stats
	s.mux.HandleFunc("GET /api/stats/summary", s.requireAuth(s.handleStatsSummary))
	s.mux.HandleFunc("GET /api/stats/requests/timeseries", s.requireAuth(s.handleStatsRequests))
	s.mux.HandleFunc("GET /api/stats/connections/timeseries", s.requireAuth(s.handleStatsConnections))
	s.mux.HandleFunc("GET /api/stats/top/ips", s.requireAuth(s.handleStatsTopIPs))
	s.mux.HandleFunc("GET /api/stats/top/paths", s.requireAuth(s.handleStatsTopPaths))
	s.mux.HandleFunc("GET /api/stats/status-distribution", s.requireAuth(s.handleStatsStatus))
	s.mux.HandleFunc("GET /api/stats/topology", s.requireAuth(s.handleTopology))
	s.mux.HandleFunc("GET /api/stats/refresh", s.requireAuth(s.handleStatsRefresh))
	s.mux.HandleFunc("GET /api/stats/proto/timeseries", s.requireAuth(s.handleStatsProto))
	s.mux.HandleFunc("GET /api/stats/resources", s.requireAuth(s.handleStatsResources))

	// Logs
	s.mux.HandleFunc("GET /api/logs/http", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		s.handleLog(w, r, "/var/log/nginx/access.log")
	}))
	s.mux.HandleFunc("GET /api/logs/stream", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		s.handleLog(w, r, "/var/log/nginx/stream.log")
	}))
	s.mux.HandleFunc("GET /api/logs/error", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		s.handleLog(w, r, "/var/log/nginx/error.log")
	}))
	s.mux.HandleFunc("GET /api/logs/all", s.requireAuth(s.handleAllLogs))

	// Panel info
	s.mux.HandleFunc("GET /api/panel/info", s.requireAuth(s.handlePanelInfo))
	s.mux.HandleFunc("GET /api/panel/config", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.cfg.Read()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, c)
	}))
	s.mux.HandleFunc("POST /api/panel/sync", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.cfg.Read()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, c)
	}))

	// Static files
	staticRoot := StaticFS()
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticRoot))))

	// SPA fallback — must be last
	s.mux.HandleFunc("/", s.handleSPA)
}

// ── SPA handler ─────────────────────────────────────────────

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	panelPath := ""
	installed := false
	if c != nil {
		panelPath = c.Shahrag.Panel.Path
		installed = c.Shahrag.Panel.Installed
	}

	reqPath := strings.TrimPrefix(r.URL.Path, "/")

	// API calls may arrive with the panel path prefix (direct-port access,
	// where the page's fetch wrapper prefixes /api/ calls with the panel
	// path). Re-dispatch them to the mux for ANY method so they are
	// handled as real API routes instead of falling through to the SPA
	// (which would return HTML — or 404 for POST — and break the
	// frontend). This check must come BEFORE the method gate below.
	if panelPath != "" && strings.HasPrefix(reqPath, panelPath+"/") {
		rest := strings.TrimPrefix(reqPath, panelPath+"/")
		if strings.HasPrefix(rest, "api/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + rest
			s.mux.ServeHTTP(w, r2)
			return
		}
	}

	if r.Method != "GET" && r.Method != "HEAD" {
		http.NotFound(w, r)
		return
	}

	if !installed || panelPath == "" {
		// Serve install page
		s.serveTemplate(w, "install.html")
		return
	}

	if reqPath == panelPath || reqPath == panelPath+"/" {
		if reqPath == panelPath {
			http.Redirect(w, r, "/"+panelPath+"/", http.StatusFound)
			return
		}
		s.serveTemplate(w, "index.html")
		return
	}

	if strings.HasPrefix(reqPath, panelPath+"/") {
		rest := strings.TrimPrefix(reqPath, panelPath+"/")
		// Serve any static asset that exists (nested paths included, e.g.
		// "static/css/app.css", "static/js/i18n/fa.js", "js/pages/...").
		if rest != "" {
			if f, err := StaticFS().Open(rest); err == nil {
				f.Close()
				setAssetCache(w)
				http.ServeFileFS(w, r, StaticFS(), rest)
				return
			}
			// Also try without the "static/" prefix for convenience.
			alt := strings.TrimPrefix(rest, "static/")
			if f, err := StaticFS().Open(alt); err == nil {
				f.Close()
				setAssetCache(w)
				http.ServeFileFS(w, r, StaticFS(), alt)
				return
			}
		}
		// Everything else under /<path>/ is the SPA.
		s.serveTemplate(w, "index.html")
		return
	}

	// Unknown path → redirect to panel
	http.Redirect(w, r, "/"+panelPath+"/", http.StatusFound)
}

// serveTemplate writes an embedded template. For index.html the panel's
// base path is injected server-side.
//
// The base path used to be GUESSED in the browser with a regex over
// window.location.pathname, which only matched a single-segment path
// ("/xxx/"). On any deeper URL under the panel path the guess produced "/",
// every /api/... call then went to nginx's fake site (200 + HTML), the SPA
// could not parse the answer and showed the login screen — the reported
// "I have to log in again" behaviour. The server knows the real path, so it
// states it instead of letting the client guess.
// setAssetCache tags static assets with the build so an upgrade cannot leave
// a browser running the PREVIOUS JavaScript.
//
// The assets were served with `max-age=300` and no validator, so after an
// upgrade the browser kept using its cached copy for up to five minutes —
// old page code against a new API. Worse, a hard-cached module can silently
// fail to register and the panel then looks broken for no visible reason.
// ETag lets the browser revalidate cheaply (304, no body) while still
// avoiding a re-download when nothing changed.
func setAssetCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", assetETag)
}

// assetETag changes with every build, which is exactly when the embedded
// assets can change.
var assetETag = fmt.Sprintf(`"%s"`, BuildTag)

// BuildTag is set from main so cache tags follow the installed build.
var BuildTag = "dev"

func (s *Server) serveTemplate(w http.ResponseWriter, name string) {
	data, err := fs.ReadFile(TemplateFS(), name)
	if err != nil {
		http.Error(w, "Template not found", 500)
		return
	}
	if name == "index.html" || name == "install.html" {
		base := "/"
		if c, err := s.cfg.Read(); err == nil && c.Shahrag.Panel.Path != "" && c.Shahrag.Panel.Installed {
			base = "/" + strings.Trim(c.Shahrag.Panel.Path, "/") + "/"
		}
		data = bytes.ReplaceAll(data, []byte("__SHAHRAG_BASE__"), []byte(base))
	}
	ext := path.Ext(name)
	switch ext {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	}
	w.Write(data)
}

// ── Health ──────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"status": "ok", "time": time.Now().Unix()})
}

// ── Log helper ──────────────────────────────────────────────

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request, path string) {
	n := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 1000 {
			n = parsed
		}
	}
	writeJSON(w, 200, map[string]string{"content": nginxpkg.TailLog(path, n)})
}

func (s *Server) handleAllLogs(w http.ResponseWriter, r *http.Request) {
	n := 50
	if v := r.URL.Query().Get("lines"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 500 {
			n = parsed
		}
	}
	writeJSON(w, 200, map[string]string{
		"http":   nginxpkg.TailLog("/var/log/nginx/access.log", n),
		"stream": nginxpkg.TailLog("/var/log/nginx/stream.log", n),
		"error":  nginxpkg.TailLog("/var/log/nginx/error.log", n),
	})
}

// ── Utilities for net import ────────────────────────────────
var _ = net.ParseIP
var _ = os.Getenv
