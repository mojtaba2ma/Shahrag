package web

// The tuning API and the r47 UI contract.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"shahrag/internal/config"
)

func TestTuningEndpointOffersARecommendationForEveryField(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	var got struct {
		Tuning      config.Tuning                    `json:"tuning"`
		Recommended map[string]config.Recommendation `json:"recommended"`
		System      struct {
			Cores    int   `json:"cores"`
			RAMBytes int64 `json:"ram_bytes"`
		} `json:"system"`
		Profiles  []string `json:"profiles"`
		Suggested string   `json:"suggested_profile"`
	}
	getJSON(t, s, tok, "/api/settings/tuning", &got)

	if len(got.Recommended) < 25 {
		t.Fatalf("only %d recommendations returned", len(got.Recommended))
	}
	// The system facts must be real, or the recommendations are fiction.
	if got.System.Cores <= 0 || got.System.RAMBytes <= 0 {
		t.Fatalf("the endpoint reported an impossible machine: %+v", got.System)
	}
	if len(got.Profiles) != 3 {
		t.Fatalf("profiles = %v", got.Profiles)
	}
	if got.Suggested == "" {
		t.Fatal("no profile was suggested")
	}
	// Every recommendation must carry a reason the UI can translate.
	for k, r := range got.Recommended {
		if r.Why == "" {
			t.Errorf("%s has no reason code", k)
		}
	}
}

// Tuning must be OFF by default, and emit nothing until switched on. This is
// what makes it safe to ship to an existing installation.
func TestTuningIsOffByDefault(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	var got map[string]interface{}
	getJSON(t, s, tok, "/api/settings/tuning", &got)
	if got["enabled"] != false {
		t.Fatalf("tuning defaults to enabled=%v", got["enabled"])
	}
	c, _ := mgr.Read()
	if c.NginxSettings.Tuning.Enabled {
		t.Fatal("the stored config has tuning enabled")
	}
}

func TestApplyingAProfileFillsEveryField(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/settings/tuning", map[string]interface{}{
		"profile": "small", "enabled": true,
	})

	c, _ := mgr.Read()
	tn := c.NginxSettings.Tuning
	if !tn.Enabled {
		t.Fatal("not enabled")
	}
	if tn.Profile != "small" {
		t.Fatalf("profile = %q", tn.Profile)
	}
	if tn.WorkerRLimitNofile == 0 || tn.ProxyReadTimeout == 0 || tn.SSLECDHCurve == "" {
		t.Fatalf("the profile left fields empty: %+v", tn)
	}
	// The dangerous one must NOT be switched on by a preset.
	if tn.AccessLogOff {
		t.Fatal("a preset switched the access log off")
	}
}

// An explicit field in the same request as a profile must win. That ordering
// is what lets the UI send "apply the small profile, but I want 60 MB".
func TestAnExplicitFieldBeatsTheProfile(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/settings/tuning", map[string]interface{}{
		"profile": "small", "enabled": true, "client_max_body_mb": 60,
	})
	c, _ := mgr.Read()
	if got := c.NginxSettings.Tuning.ClientMaxBodyMB; got != 60 {
		t.Fatalf("client_max_body_mb = %d, want the explicit 60", got)
	}
	// ...and the rest still came from the profile.
	if c.NginxSettings.Tuning.ProxyReadTimeout == 0 {
		t.Fatal("the profile was not applied alongside the explicit field")
	}
}

func TestAutoFillUsesTheRealMachine(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	putJSON(t, s, tok, "/api/settings/tuning", map[string]interface{}{
		"auto_fill": true, "enabled": true,
	})
	c, _ := mgr.Read()
	tn := c.NginxSettings.Tuning
	if tn.WorkerProcesses != "auto" {
		t.Errorf("worker_processes = %q", tn.WorkerProcesses)
	}
	if tn.WorkerRLimitNofile < 1024 {
		t.Errorf("worker_rlimit_nofile = %d", tn.WorkerRLimitNofile)
	}
}

func TestInvalidTuningIsRefusedWithAReadableReason(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	body := `{"enabled":true,"gzip_comp_level":99}`
	req := httptest.NewRequest("PUT", "/api/settings/tuning", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", tok)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("an impossible value was accepted: %d %s", w.Code, w.Body.String())
	}
	var e map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	detail, _ := e["detail"].(string)
	if !strings.Contains(detail, "gzip_comp_level") {
		t.Fatalf("the error does not name the offending field: %q", detail)
	}
}

// Injection through the one free-text field must be refused at the API, not
// left for nginx to reject at reload time.
func TestCurveInjectionIsRefusedAtTheAPI(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	body := `{"enabled":true,"ssl_ecdh_curve":"X25519; return 200 'pwned'"}`
	req := httptest.NewRequest("PUT", "/api/settings/tuning", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", tok)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("an injected curve list was accepted: %d", w.Code)
	}
	c, _ := mgr.Read()
	if strings.Contains(c.NginxSettings.Tuning.SSLECDHCurve, "return") {
		t.Fatal("the injected value was stored")
	}
}

// Switching the access log off must produce a warning, because it silently
// blinds the statistics page and the 404-flood ban rule.
func TestTurningTheAccessLogOffWarns(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	tok := login(t, s)

	b, _ := json.Marshal(map[string]interface{}{
		"enabled": true, "auto_fill": true, "access_log_off": true,
	})
	req := httptest.NewRequest("PUT", "/api/settings/tuning", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", tok)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	warns, _ := got["warnings"].([]interface{})
	found := false
	for _, x := range warns {
		if x == "access_log_off_breaks_stats" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning about the statistics page: %v", warns)
	}
}

func TestTuningRequiresAuth(t *testing.T) {
	s, _ := newTestServer(t, "panel")
	for _, m := range []string{"GET", "PUT"} {
		req := httptest.NewRequest(m, "/api/settings/tuning", strings.NewReader("{}"))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Errorf("%s got %d, want 401", m, w.Code)
		}
	}
	// The measurement endpoint sends real traffic, so it especially must
	// not be reachable unauthenticated.
	req := httptest.NewRequest("POST", "/api/settings/tuning/measure", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("the measurement endpoint got %d, want 401", w.Code)
	}
}

// ── The topology endpoint the map is built from ──────────────

func TestTopologyDerivesPortKinds(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	_, err := mgr.Mutate(func(c *config.Config) error {
		c.ListenPorts = []int{443, 8443}
		c.Services = map[string]config.Service{
			"web": {LocalPort: 3000, ListenPort: 8443, Path: "app"},
			"off": {LocalPort: 4000, ListenPort: 8443, Path: "old", Disabled: true},
		}
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services = map[string]config.RealityService{
			"steam": {SNI: "steamcommunity.com", LocalPort: 50768,
				Ports: []int{443, 2053}, Target: config.PassthroughTarget},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Ports []struct {
			Port     int      `json:"port"`
			Kind     string   `json:"kind"`
			Services []string `json:"services"`
			SNINames []string `json:"sni_names"`
			Fallback int      `json:"fallback"`
		} `json:"ports"`
		Services []struct {
			Name        string `json:"name"`
			Disabled    bool   `json:"disabled"`
			Passthrough bool   `json:"passthrough"`
		} `json:"services"`
		RealityHTTPPort int `json:"reality_http_port"`
	}
	getJSON(t, s, tok, "/api/stats/topology", &got)

	byPort := map[int]string{}
	for _, p := range got.Ports {
		byPort[p.Port] = p.Kind
	}
	// 443 and 2053 belong to the stream module, so they are SNI, not
	// https — getting this wrong would draw the map with the wrong shape.
	if byPort[443] != "sni" {
		t.Errorf("port 443 kind = %q, want sni", byPort[443])
	}
	if byPort[2053] != "sni" {
		t.Errorf("port 2053 kind = %q, want sni", byPort[2053])
	}
	if byPort[8443] != "https" {
		t.Errorf("port 8443 kind = %q, want https", byPort[8443])
	}
	if byPort[6038] != "http" {
		t.Errorf("the reality fallback port kind = %q, want http", byPort[6038])
	}
	if got.RealityHTTPPort != 6038 {
		t.Errorf("reality_http_port = %d", got.RealityHTTPPort)
	}

	// A disabled service must be present but MARKED. Omitting it would
	// make the map lie by silence; drawing it as live would be worse.
	var sawDisabled bool
	for _, svc := range got.Services {
		if svc.Name == "off" {
			sawDisabled = true
			if !svc.Disabled {
				t.Error("a disabled service is not marked as such")
			}
		}
	}
	if !sawDisabled {
		t.Error("the disabled service is missing from the topology entirely")
	}
	// It must not appear in a port's live service list, though.
	for _, p := range got.Ports {
		for _, n := range p.Services {
			if n == "off" {
				t.Errorf("port %d lists a disabled service", p.Port)
			}
		}
	}
}

// The topology must be stable between calls, or the map's boxes move on
// every five-second refresh and it becomes unreadable.
func TestTopologyIsStablyOrdered(t *testing.T) {
	s, mgr := newTestServer(t, "panel")
	tok := login(t, s)

	_, _ = mgr.Mutate(func(c *config.Config) error {
		c.Services = map[string]config.Service{}
		for _, n := range []string{"zeta", "alpha", "mike", "bravo"} {
			c.Services[n] = config.Service{LocalPort: 3000, ListenPort: 443, Path: n}
		}
		return nil
	})

	var first []string
	for i := 0; i < 6; i++ {
		var got struct {
			Services []struct {
				Name string `json:"name"`
			} `json:"services"`
		}
		getJSON(t, s, tok, "/api/stats/topology", &got)
		names := make([]string, 0, len(got.Services))
		for _, s := range got.Services {
			names = append(names, s.Name)
		}
		if first == nil {
			first = names
			continue
		}
		if strings.Join(names, ",") != strings.Join(first, ",") {
			t.Fatalf("order changed between calls:\n  %v\n  %v", first, names)
		}
	}
	if strings.Join(first, ",") != "alpha,bravo,mike,zeta" {
		t.Fatalf("not sorted: %v", first)
	}
}

// ── UI contract ──────────────────────────────────────────────

func TestStatusPageHasThreeTabs(t *testing.T) {
	src := readAsset(t, "js/pages/status.js")
	for _, want := range []string{`id: "health"`, `id: "stats"`, `id: "map"`} {
		if !strings.Contains(src, want) {
			t.Errorf("the Status page is missing the tab %s", want)
		}
	}
}

func TestTabPageIsShared(t *testing.T) {
	// Both wrappers must use the same helper, or every bug in lazy
	// loading and cleanup chaining has to be fixed twice.
	tp := readAsset(t, "js/tabpage.js")
	for _, want := range []string{"loadPage", "_shahragCleanup", "ticket", "replaceState"} {
		if !strings.Contains(tp, want) {
			t.Errorf("tabpage.js does not handle %s", want)
		}
	}
}

// The resource bars must be coloured by VALUE, and the swap bar by VERDICT.
// Confusing the two is the bug that would make a full-but-idle swap render
// red, which is the exact misunderstanding the health page exists to fix.
func TestResourceBarsUseTheScaleAndSwapUsesTheVerdict(t *testing.T) {
	src := readAsset(t, "js/pages/health.js")
	if !strings.Contains(src, "function ramp(") {
		t.Fatal("no continuous colour ramp")
	}
	if !strings.Contains(src, `hsl(`) {
		t.Error("the ramp is not interpolated in HSL, so mid-range colours will be muddy")
	}
	// The three resource rows pass "scale".
	if n := strings.Count(src, `"scale"`); n < 4 {
		t.Errorf("only %d uses of the value scale; cpu, memory and disk should all use it", n)
	}
	// The swap bar must NOT.
	i := strings.Index(src, "function swapCard(")
	j := strings.Index(src[i:], "function resourceCard(")
	if i < 0 || j < 0 {
		t.Fatal("could not isolate swapCard")
	}
	if strings.Contains(src[i:i+j], `bar(s.used_pct, "scale")`) {
		t.Fatal("the swap bar is coloured by its own percentage — a full but " +
			"idle swap would render red, which is the exact misunderstanding " +
			"this page exists to correct")
	}
	if !strings.Contains(src[i:i+j], "bar(s.used_pct, check.level)") {
		t.Error("the swap bar is not coloured by its verdict")
	}
}

func TestMapPageDrawsWithoutALibrary(t *testing.T) {
	src := readAsset(t, "js/pages/map.js")
	if !strings.Contains(src, "<svg") {
		t.Fatal("the map does not draw an SVG")
	}
	// Assets are go:embed'ed and there is no build step, so a CDN import
	// would simply fail to load in the panel.
	//
	// Matched against real URL shapes, not bare substrings: "cdn." on its
	// own also matches the hostname in a comment, which is a test mistake
	// rather than a finding.
	for _, forbidden := range []string{
		"https://cdn.", "http://cdn.", "//unpkg.com", "//cdnjs.",
		"//cdn.jsdelivr", "import(", "from \"http",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("the map references %q, which cannot work in an embedded asset", forbidden)
		}
	}
	// RTL is handled by mirroring the COORDINATES, not by transforming
	// elements — an earlier version used a transform and drew every label
	// outside its own box. The SVG must also opt out of the page's own
	// direction, or the browser mirrors text placement a second time.
	if !strings.Contains(src, "all.forEach(n => { n.x = W - n.x - n.w; });") {
		t.Error("the map does not mirror its coordinates for RTL")
	}
	// The containers must be mirrored too, or they sit on the wrong side
	// of the nodes they are meant to contain.
	//
	// Asserted on the BEHAVIOUR rather than on an exact list, because the
	// literal "[sniBox, httpBox]" broke the moment a third container (the
	// LocalHost group) was added in r51 — the map was correct and the
	// test was pinned to yesterday's spelling. What must be true is that
	// every container box is mirrored in the same pass.
	if !strings.Contains(src, "forEach(b => { if (b) b.x = W - b.x - b.w; })") {
		t.Error("the containers are not mirrored for RTL")
	}
	for _, box := range []string{"sniBox", "httpBox", "localBox"} {
		// Each container must appear in that mirroring list.
		i := strings.Index(src, "forEach(b => { if (b) b.x = W - b.x - b.w; })")
		if i < 0 {
			break
		}
		// The list literal sits immediately before the forEach.
		start := strings.LastIndex(src[:i], "[")
		if start < 0 || !strings.Contains(src[start:i], box) {
			t.Errorf("%s is not mirrored for RTL, so it would sit on the wrong side of the boxes it contains", box)
		}
	}
	if !strings.Contains(src, `direction="ltr"`) {
		t.Error("the SVG does not opt out of the page direction, so RTL will " +
			"mirror text placement on top of the coordinate mirroring")
	}
	// Motion must be optional.
	if !strings.Contains(src, "prefers-reduced-motion") {
		t.Error("the map animates unconditionally")
	}
}

func TestTuningFormCoversEveryField(t *testing.T) {
	src := readAsset(t, "js/tuning.js")
	// Every field in the Go struct must appear in the form's catalogue,
	// or it is a setting nobody can reach.
	for _, key := range []string{
		"worker_processes", "worker_rlimit_nofile", "multi_accept",
		"keepalive_timeout", "keepalive_requests", "client_header_timeout",
		"client_body_timeout", "send_timeout", "proxy_connect_timeout",
		"proxy_read_timeout", "proxy_send_timeout", "proxy_socket_keepalive",
		"upstream_keepalive", "proxy_buffering_off", "client_max_body_mb",
		"client_body_buffer_kb", "large_client_header_kb", "gzip_enabled",
		"gzip_comp_level", "gzip_min_length", "ssl_session_cache_mb",
		"ssl_session_timeout_min", "ssl_ecdh_curve", "server_tokens_off",
		"limit_conn_per_ip", "open_file_cache_max", "access_log_off",
	} {
		if !strings.Contains(src, `"`+key+`"`) {
			t.Errorf("the form has no field for %s", key)
		}
	}
	// Fill-everything must skip the access log.
	if !strings.Contains(src, `if (k === "access_log_off") return;`) {
		t.Error("fill-everything would touch the switch that blinds the statistics page")
	}
}
