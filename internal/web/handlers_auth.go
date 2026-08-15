package web

import (
	"net/http"
	"strings"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/security"
)

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type changePassReq struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body loginReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	c, err := s.cfg.Read()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	auth := c.Shahrag.Auth
	if !c.Shahrag.Panel.Installed || auth.PasswordHash == "" {
		writeErr(w, 403, "Panel not configured. Run installation first.")
		return
	}
	if body.Username != auth.Username {
		writeErr(w, 401, "Invalid credentials")
		return
	}
	if !security.VerifyPassword(body.Password, auth.PasswordHash) {
		writeErr(w, 401, "Invalid credentials")
		return
	}
	// The token itself carries only a long hard cap; the practical lifetime
	// is a sliding window governed by the inactivity lock, refreshed on
	// every real request. This keeps refreshes logged in while still
	// locking the panel after LockMinutes of inactivity.
	token := s.session.Create(body.Username, hardSessionCap)

	http.SetCookie(w, &http.Cookie{
		Name:     "shahrag_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   cookieMaxAgeSeconds,
	})
	s.sessMu.Lock()
	s.lastActive[token] = time.Now()
	s.sessMu.Unlock()
	writeJSON(w, 200, map[string]interface{}{"ok": true, "username": body.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("shahrag_session"); err == nil {
		s.sessMu.Lock()
		delete(s.lastActive, cookie.Value)
		s.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "shahrag_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := userClaims(r)
	lock := 0
	timeout := 60
	if c, err := s.cfg.Read(); err == nil {
		lock = c.Shahrag.Security.LockMinutes
		if c.Shahrag.Security.SessionTimeoutMins > 0 {
			timeout = c.Shahrag.Security.SessionTimeoutMins
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"authenticated":           true,
		"username":                claims.User,
		"lock_minutes":            lock,
		"session_timeout_minutes": timeout,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body changePassReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if len(body.NewPassword) < 6 {
		writeErr(w, 400, "New password must be at least 6 characters")
		return
	}
	c, err := s.cfg.Read()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !security.VerifyPassword(body.CurrentPassword, c.Shahrag.Auth.PasswordHash) {
		writeErr(w, 400, "Current password is incorrect")
		return
	}
	_, err = s.cfg.Mutate(func(cfg *config.Config) error {
		cfg.Shahrag.Auth.PasswordHash = security.HashPassword(body.NewPassword)
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
