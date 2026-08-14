package web

import (
	"net/http"

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
	ttl := c.Shahrag.Security.SessionTimeoutMins
	if ttl == 0 {
		ttl = 60
	}
	token := s.session.Create(body.Username, ttl)
	http.SetCookie(w, &http.Cookie{
		Name:     "shahrag_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   ttl * 60,
	})
	writeJSON(w, 200, map[string]interface{}{"ok": true, "username": body.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   "shahrag_session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := userClaims(r)
	writeJSON(w, 200, map[string]interface{}{
		"authenticated": true,
		"username":      claims.User,
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
