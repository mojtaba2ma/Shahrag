package web

import (
	"encoding/json"
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

// The file API must never let a broken configuration reach disk, because the
// panel is often the only way back in. These tests pin the two safety nets:
// JSON is parsed BEFORE writing, and an empty body is refused outright.
func TestFileAPIRefusesBadContent(t *testing.T) {
	srv, _ := newTestServer(t, "p")

	login := httptest.NewRecorder()
	srv.ServeHTTP(login, httptest.NewRequest("POST", "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"secret123"}`)))
	if login.Code != 200 {
		t.Fatalf("login failed: %d", login.Code)
	}
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == "shahrag_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	before, err := os.ReadFile(config.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	put := func(id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/files/"+id, strings.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}

	// Unparsable JSON.
	if w := put("config", `{"content":"{ not json ]","reload":false}`); w.Code != 400 {
		t.Errorf("invalid JSON must be rejected with 400, got %d: %s", w.Code, w.Body)
	}
	// Valid JSON that is not a Shahrag config.
	if w := put("config", `{"content":"{\"hello\":1}","reload":false}`); w.Code != 400 {
		t.Errorf("a non-Shahrag config must be rejected, got %d: %s", w.Code, w.Body)
	}
	// Empty content: almost always a mistake, and unrecoverable.
	if w := put("config", `{"content":"   ","reload":false}`); w.Code != 400 {
		t.Errorf("empty content must be rejected, got %d", w.Code)
	}
	// An unknown file id must not create anything.
	if w := put("../../etc/passwd", `{"content":"x","reload":false}`); w.Code == 200 {
		t.Error("an unknown file id must not be writable")
	}

	after, err := os.ReadFile(config.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a rejected write must leave the config file untouched")
	}

	// The listing must expose the files and mark the generated ones.
	r := httptest.NewRequest("GET", "/api/files", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("listing files failed: %d", w.Code)
	}
	var list []struct {
		ID        string `json:"id"`
		Generated bool   `json:"generated"`
		Editable  bool   `json:"editable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	gen := map[string]bool{}
	for _, f := range list {
		gen[f.ID] = f.Generated
		if !f.Editable {
			t.Errorf("file %q should be editable", f.ID)
		}
	}
	for _, id := range []string{"config", "gateway", "stream", "nginxconf"} {
		if _, ok := gen[id]; !ok {
			t.Errorf("file %q missing from the listing", id)
		}
	}
	if !gen["gateway"] || !gen["stream"] {
		t.Error("the generated files must be flagged as generated")
	}
	if gen["config"] {
		t.Error("config.json is not generated")
	}
}

// A stale asset cache made an upgrade invisible: assetETag was built from
// BuildTag at package-init time, but main assigned BuildTag afterwards, so
// every build shipped the identical validator "dev". A browser that had used
// an older panel sent If-None-Match: "dev", got 304 Not Modified, and kept
// running the OLD JavaScript — the new UI simply never appeared, even in a
// private window once it had been visited.
func TestAssetCacheBustsOnContentChange(t *testing.T) {
	srv, _ := newTestServer(t, "p")

	get := func(path string, inm string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if inm != "" {
			r.Header.Set("If-None-Match", inm)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}

	const asset = "/p/static/js/app.js"
	first := get(asset, "")
	if first.Code != 200 {
		t.Fatalf("asset not served: %d", first.Code)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("assets must carry an ETag so browsers can revalidate")
	}
	if strings.Contains(tag, "dev") {
		t.Errorf("the ETag must be derived from the CONTENT, not an unset build tag (got %s)", tag)
	}

	// The same content revalidates cheaply — that part must keep working.
	if again := get(asset, tag); again.Code != 304 {
		t.Errorf("unchanged content should revalidate as 304, got %d", again.Code)
	}

	// A validator from a previous build must NOT match.
	if old := get(asset, `"dev"`); old.Code != 200 {
		t.Errorf("a stale validator must be refused with a fresh 200, got %d", old.Code)
	}

	// Different assets must not share a validator, otherwise one changed file
	// would let every other file go stale.
	other := get("/p/static/js/pages/services.js", "")
	if other.Code == 200 && other.Header().Get("ETag") == tag {
		t.Error("different assets must have different ETags")
	}

	// The shell decides which assets to load, so it must never be cached.
	shell := get("/p/", "")
	cc := shell.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("the HTML shell must not be cached across an upgrade (Cache-Control: %q)", cc)
	}
	// It must also reference assets with a version, so a heuristically cached
	// shell still asks for URLs it has never seen.
	if !strings.Contains(shell.Body.String(), "app.js?v=") {
		t.Error("asset URLs in the shell must be versioned")
	}
	if strings.Contains(shell.Body.String(), "__SHAHRAG_V__") {
		t.Error("the version placeholder must be substituted before serving")
	}
}
