package web

// Tests for the r46 UI change: the honeypot and automatic banning moved
// behind one "Security" menu entry with two tabs, and both features now
// default to RECORDING what they catch the first time they are configured.
//
// The backend behaviour of either feature is deliberately unchanged, and
// several of these assertions exist specifically to prove that.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// ── The "log on first, then obey the operator" default ───────

func TestHoneypotLoggingDefaultsOnBeforeTheFormIsEverSaved(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	var got map[string]interface{}
	getJSON(t, s, tok, "/api/honeypot", &got)

	if got["log_hits"] != true {
		t.Fatalf("a never-configured honeypot reports log_hits=%v, want true —\n"+
			"switching a detector on and keeping nothing it detects is not a useful state",
			got["log_hits"])
	}
}

// ...but once the operator has saved the form with logging OFF, it must stay
// off for ever. This is the assertion that makes the `Configured` flag worth
// its existence: without it a stored false is indistinguishable from a field
// that was never set, and the panel would keep switching logging back on
// under somebody who deliberately turned it off.
func TestHoneypotLoggingOffSurvivesAReload(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/honeypot", map[string]interface{}{
		"enabled": true, "mode": "throttle", "log_hits": false,
	})

	c, _ := mgr.Read()
	if !c.Honeypot.Configured {
		t.Fatal("saving the form did not mark the honeypot as configured")
	}
	if c.Honeypot.LogHits {
		t.Fatal("log_hits was stored as true after the operator turned it off")
	}

	var got map[string]interface{}
	getJSON(t, s, tok, "/api/honeypot", &got)
	if got["log_hits"] != false {
		t.Fatalf("the API re-enabled logging the operator switched off: %v", got["log_hits"])
	}
}

func TestAutoBanLoggingDefaultsOnBeforeTheFormIsEverSaved(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	var got map[string]interface{}
	getJSON(t, s, tok, "/api/autoban", &got)
	if got["log_bans"] != true {
		t.Fatalf("a never-configured ban engine reports log_bans=%v, want true", got["log_bans"])
	}
}

func TestAutoBanLoggingOffSurvivesAReload(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/autoban", map[string]interface{}{
		"enabled":  true,
		"log_bans": false,
		"honeypot": map[string]interface{}{"enabled": true, "hits": 2, "window_minutes": 60, "ban_minutes": 240},
	})

	c, _ := mgr.Read()
	if !c.AutoBan.Configured {
		t.Fatal("saving the form did not mark auto-ban as configured")
	}
	if c.AutoBan.LogBans {
		t.Fatal("log_bans stored as true after the operator turned it off")
	}

	var got map[string]interface{}
	getJSON(t, s, tok, "/api/autoban", &got)
	if got["log_bans"] != false {
		t.Fatalf("the API re-enabled ban logging the operator switched off")
	}
}

// The default must not quietly change any OTHER setting. An operator
// upgrading to r46 must find their trap exactly as they left it.
func TestTheNewDefaultDoesNotDisturbExistingSettings(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	// An installation configured before r46: decoy mode, custom rate, a
	// path exception, and no Configured flag because the field did not
	// exist yet.
	_, err := mgr.Mutate(func(c *config.Config) error {
		c.Honeypot = config.Honeypot{
			Enabled: true, Mode: "decoy", RatePerMinute: 7,
			AllowPaths: []string{"/wp-admin"},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]interface{}
	getJSON(t, s, tok, "/api/honeypot", &got)

	if got["mode"] != "decoy" {
		t.Errorf("mode changed to %v", got["mode"])
	}
	if got["rate_per_minute"] != float64(7) {
		t.Errorf("rate changed to %v", got["rate_per_minute"])
	}
	paths, _ := got["allow_paths"].([]interface{})
	if len(paths) != 1 || paths[0] != "/wp-admin" {
		t.Errorf("allow_paths changed to %v", got["allow_paths"])
	}
	// Only the recommendation is added.
	if got["log_hits"] != true {
		t.Errorf("the recommendation was not applied: %v", got["log_hits"])
	}
}

// ── The ban log endpoint ─────────────────────────────────────

func TestBanLogEndpointOnAnEmptyServer(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	var got struct {
		Events []map[string]interface{} `json:"events"`
		Path   string                   `json:"path"`
	}
	getJSON(t, s, tok, "/api/autoban/log", &got)
	// A missing file is the normal state on a fresh install and must not
	// be an error — that would light the page up red for no reason.
	if len(got.Events) != 0 {
		t.Fatalf("got %d events on a fresh server", len(got.Events))
	}
	if got.Path == "" {
		t.Fatal("the endpoint did not say where the log lives")
	}
}

func TestBanLogLineParsesIntoFields(t *testing.T) {
	line := `2026-09-08T14:03:11+03:30 203.0.113.5 "ban honeypot" hits="3" until="2026-09-08T18:03:11+03:30"`
	got, ok := parseBanLogLine(line)
	if !ok {
		t.Fatal("a well-formed line was rejected")
	}
	for k, want := range map[string]string{
		"time": "2026-09-08T14:03:11+03:30", "ip": "203.0.113.5",
		"action": "ban", "reason": "honeypot", "hits": "3",
		"until": "2026-09-08T18:03:11+03:30",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %q", k, got[k], want)
		}
	}
}

func TestBanLogRejectsRubbish(t *testing.T) {
	for _, line := range []string{"", "   ", "nonsense", "2026-01-01 1.2.3.4"} {
		if _, ok := parseBanLogLine(line); ok {
			t.Errorf("accepted %q", line)
		}
	}
}

func TestBanLogUnbanLineHasNoReason(t *testing.T) {
	line := `2026-09-08T14:03:11Z 203.0.113.5 "unban " hits="0" until="-"`
	got, ok := parseBanLogLine(line)
	if !ok {
		t.Fatal("rejected an unban line")
	}
	if got["action"] != "unban" {
		t.Fatalf("action = %v", got["action"])
	}
}

// ── The UI contract ──────────────────────────────────────────

// The menu must offer Security and Health and must NOT offer the two retired
// entries. A stale entry would open a page that no longer exists.
func TestNavListIsUpToDate(t *testing.T) {
	src := readAsset(t, "js/app.js")

	navStart := strings.Index(src, "const NAV = [")
	if navStart < 0 {
		t.Fatal("the NAV list moved; this test needs updating")
	}
	nav := src[navStart : navStart+strings.Index(src[navStart:], "];")]

	for _, want := range []string{`id: "security"`, `id: "health"`} {
		if !strings.Contains(nav, want) {
			t.Errorf("the menu is missing %s", want)
		}
	}
	for _, gone := range []string{`id: "honeypot"`, `id: "autoban"`} {
		if strings.Contains(nav, gone) {
			t.Errorf("the menu still has the retired entry %s", gone)
		}
	}
}

// The retired ids must still resolve, so an old bookmark lands on the tab
// that replaced it rather than on "page not found".
func TestRetiredPagesRedirect(t *testing.T) {
	src := readAsset(t, "js/app.js")
	if !strings.Contains(src, "const MOVED = {") {
		t.Fatal("no redirect table for the retired page ids")
	}
	for _, want := range []string{`honeypot: "security/honeypot"`, `autoban: "security/autoban"`} {
		if !strings.Contains(src, want) {
			t.Errorf("no redirect for %s", want)
		}
	}
}

// The wrapper needs loadPage in its context; without it the tabs cannot pull
// their pane modules in and the page renders an error.
func TestPageContextExposesLoadPage(t *testing.T) {
	src := readAsset(t, "js/app.js")
	if !strings.Contains(src, "loadPage: loadPageScript") {
		t.Fatal("the page context does not expose loadPage, so Security cannot host its tabs")
	}
}

// Both pane modules must still exist and still be IIFE-wrapped: they load
// through plain <script> tags, so a top-level const would become a global
// and collide.
func TestPaneModulesStillExistAndAreWrapped(t *testing.T) {
	for _, name := range []string{"honeypot", "autoban", "security", "health"} {
		src := readAsset(t, "js/pages/"+name+".js")
		if !strings.Contains(src, "window.Pages."+name) {
			t.Errorf("%s.js does not register window.Pages.%s", name, name)
		}
		if !strings.Contains(src, "(function () {") {
			t.Errorf("%s.js is not wrapped in an IIFE — a top-level const would leak", name)
		}
	}
}

// The health page must poll on a timer AND register a cleanup, or navigating
// away leaves it hammering the API for the rest of the session.
func TestHealthPageStopsPollingWhenItLeaves(t *testing.T) {
	src := readAsset(t, "js/pages/health.js")
	if !strings.Contains(src, "setInterval") {
		t.Fatal("the health page does not refresh")
	}
	if !strings.Contains(src, "_shahragCleanup") {
		t.Fatal("the health page never registers a cleanup, so its timer leaks")
	}
	if !strings.Contains(src, "clearInterval") {
		t.Fatal("the cleanup does not clear the interval")
	}
	// The cleanup must be registered BEFORE the first await, or navigating
	// away during the very first request leaks the timer.
	cleanupAt := strings.Index(src, "container._shahragCleanup =")
	awaitAt := strings.Index(src, "await load()")
	if cleanupAt < 0 || awaitAt < 0 || cleanupAt > awaitAt {
		t.Fatal("the cleanup is registered after the first await")
	}
}

// The health endpoint must be behind auth. It publishes hostnames, paths,
// certificate names and the ban count.
func TestHealthReportRequiresAuth(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	req := httptest.NewRequest("GET", "/api/health/report", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauthenticated request got %d, want 401", w.Code)
	}
}

func TestHealthReportShape(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	var r map[string]interface{}
	getJSON(t, s, tok, "/api/health/report", &r)

	for _, key := range []string{"level", "checks", "cpu", "memory", "swap",
		"disk", "panel", "nginx", "security", "certs", "traffic", "ts"} {
		if _, ok := r[key]; !ok {
			t.Errorf("the report has no %q", key)
		}
	}
	checks, _ := r["checks"].([]interface{})
	if len(checks) == 0 {
		t.Fatal("the report has no checks")
	}
	mem, _ := r["memory"].(map[string]interface{})
	if mem["total_bytes"] == nil || mem["total_bytes"].(float64) <= 0 {
		t.Fatal("the report has no memory total")
	}
}

// Every hint and check id the health page asks for must exist in en.js, or
// the UI renders a raw dotted path where a sentence should be.
func TestEveryHealthKeyExistsInEnglish(t *testing.T) {
	en := readAsset(t, "js/i18n/en.js")
	page := readAsset(t, "js/pages/health.js")

	// Keys built by string concatenation in the page.
	for _, id := range []string{"nginx", "swap", "memory", "cpu", "disk",
		"inodes", "iowait", "steal", "certs", "protection", "errors", "panel"} {
		if !strings.Contains(en, "check_"+id+":") {
			t.Errorf("en.js has no health.check_%s", id)
		}
	}
	for _, h := range []string{"nginx_down", "nginx_conf", "nginx_boot",
		"memory_available", "cpu_load", "iowait", "steal", "inodes",
		"swap_none", "swap_measuring", "swap_idle", "swap_active",
		"swap_thrashing", "certs_none", "certs_expired", "certs_problem",
		"certs_soon", "protection_engine", "protection_off", "error_rate"} {
		if !strings.Contains(en, "hint_"+h+":") {
			t.Errorf("en.js has no health.hint_%s", h)
		}
	}
	for _, lvl := range []string{"ok", "warn", "bad"} {
		if !strings.Contains(en, "overall_"+lvl+":") {
			t.Errorf("en.js has no health.overall_%s", lvl)
		}
	}
	_ = page
}

// ── helpers ──────────────────────────────────────────────────

func login(t *testing.T, s *Server) string {
	t.Helper()
	body := strings.NewReader(`{"username":"admin","password":"secret123"}`)
	req := httptest.NewRequest("POST", "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if strings.Contains(c.Name, "session") || strings.Contains(c.Name, "shahrag") {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie returned")
	return ""
}

func getJSON(t *testing.T, s *Server, cookie, path string, out interface{}) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET %s -> %d %s", path, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: %v (%s)", path, err, w.Body.String())
	}
}

func putJSON(t *testing.T, s *Server, cookie, path string, body interface{}) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("PUT %s -> %d %s", path, w.Code, w.Body.String())
	}
}

var _ = http.StatusOK
