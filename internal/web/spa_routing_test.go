package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
	"shahrag/internal/installer"
	nginxpkg "shahrag/internal/nginx"
	"shahrag/internal/security"
	"shahrag/internal/stats"
)

func newTestServer(t *testing.T, panelPath string) (*Server, *config.Manager) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	_ = os.Remove(config.ConfigPath)

	mgr := config.New()
	_, err := mgr.Mutate(func(c *config.Config) error {
		c.Shahrag.Panel.Installed = true
		c.Shahrag.Panel.Path = panelPath
		c.Shahrag.Panel.ServiceName = "Shahrag"
		c.Shahrag.Panel.LocalPort = 42159
		c.Shahrag.Auth.Username = "admin"
		c.Shahrag.Auth.PasswordHash = security.HashPassword("secret123")
		c.Shahrag.Auth.SessionSecret = "test-secret-value"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	gen := nginxpkg.NewGenerator(mgr)
	srv := NewServer(mgr, gen, installer.New(mgr), stats.NewCollector(), 42159)
	return srv, mgr
}

// The SPA shell must declare the panel's base path SERVER-SIDE.
//
// It used to be guessed in the browser with a regex over
// window.location.pathname that only matched a single path segment. On any
// deeper URL the guess collapsed to "/", every /api/... call then went to
// nginx's fake site (200 + HTML instead of JSON), the frontend could not
// read the answer and showed the login screen — the "every refresh logs me
// out" report.
func TestSPAInjectsBasePath(t *testing.T) {
	const panelPath = "Xp3IYReUB55CmT4J9RwS1t"
	srv, _ := newTestServer(t, panelPath)

	for _, url := range []string{
		"/" + panelPath + "/",
		"/" + panelPath + "/deep/link/that/is/nested",
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: expected 200, got %d", url, rec.Code)
		}
		body := rec.Body.String()
		want := `var basePath = "/` + panelPath + `/";`
		if !strings.Contains(body, want) {
			t.Errorf("%s: base path not injected (looking for %q)", url, want)
		}
		if strings.Contains(body, "__SHAHRAG_BASE__") {
			t.Errorf("%s: placeholder left unreplaced in the served HTML", url)
		}
		// The bootstrap must run BEFORE app.js, otherwise app.js issues its
		// first /api/auth/me through the unpatched fetch.
		bootIdx := strings.Index(body, "var basePath =")
		appIdx := strings.Index(body, "static/js/app.js")
		if bootIdx < 0 || appIdx < 0 || bootIdx > appIdx {
			t.Errorf("%s: base-path bootstrap must appear before app.js (boot=%d app=%d)",
				url, bootIdx, appIdx)
		}
	}
}

// API calls that arrive WITH the panel-path prefix must be handled as API
// routes for every method — never fall through to the SPA (which would
// answer HTML and break the frontend).
func TestPrefixedAPIRoutesAreDispatched(t *testing.T) {
	const panelPath = "secretpath"
	srv, _ := newTestServer(t, panelPath)

	cases := []struct {
		method, url string
		wantCode    int
	}{
		{"GET", "/" + panelPath + "/api/auth/me", 401},
		{"POST", "/" + panelPath + "/api/auth/login", 400}, // empty body
		{"GET", "/" + panelPath + "/api/health", 200},
		{"GET", "/api/auth/me", 401},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(c.method, c.url, strings.NewReader("")))
		if rec.Code != c.wantCode {
			t.Errorf("%s %s: got %d, want %d (body: %s)",
				c.method, c.url, rec.Code, c.wantCode, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
			t.Errorf("%s %s: API answered %q, not JSON — the SPA would treat this as a dead session",
				c.method, c.url, ct)
		}
	}
}

// A refresh must keep the session: the cookie is re-issued on every
// authenticated request with a long lifetime, and the inactivity lock is
// tracked separately on the server.
func TestSessionSurvivesRepeatedRequests(t *testing.T) {
	const panelPath = "p"
	srv, _ := newTestServer(t, panelPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"secret123"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "shahrag_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie issued")
	}
	if cookie.MaxAge < 24*3600 {
		t.Errorf("cookie MaxAge=%d is too short — a refresh could drop the session", cookie.MaxAge)
	}

	// Simulate 10 refreshes: each must stay authenticated and re-issue the
	// cookie with the full lifetime.
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest("GET", "/"+panelPath+"/api/auth/me", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("refresh %d: session lost (%d %s)", i+1, w.Code, w.Body.String())
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == "shahrag_session" && c.MaxAge < 24*3600 {
				t.Errorf("refresh %d: cookie re-issued with MaxAge=%d", i+1, c.MaxAge)
			}
		}
	}

	// Background polls must not be rejected either.
	r := httptest.NewRequest("GET", "/"+panelPath+"/api/settings/nginx?_poll=1", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("poll request rejected: %d %s", w.Code, w.Body.String())
	}
}
