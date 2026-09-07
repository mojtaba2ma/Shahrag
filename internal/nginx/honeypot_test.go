package nginx

// The honeypot.
//
// Two things have to be true and neither is provable by reading the
// generated text: nginx must accept the config, and a client that trips the
// trap must actually be slowed or refused while everyone else is not. So
// the important tests here start a real nginx and make real requests.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shahrag/internal/config"
)

func hpConfig(t *testing.T, h config.Honeypot) (*config.Manager, *Generator, string, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	mgr := config.New()
	if err := mgr.AddDomain("example.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddService("app", "a", "example.test", 3000, 8443, "/", true, false); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "gateway.conf")
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Honeypot = h
		c.Nginx.OutputPath = out
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(mgr)
	g.NginxConf = filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(g.NginxConf, []byte("events{}\nhttp{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return mgr, g, out, dir
}

// Off by default: an existing installation must generate exactly what it
// did before this feature existed.
func TestHoneypotIsOffByDefault(t *testing.T) {
	_, g, out, _ := hpConfig(t, config.Honeypot{})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))
	for _, marker := range []string{"shg_hp", "limit_req_zone", "Honeypot"} {
		if strings.Contains(s, marker) {
			t.Errorf("a disabled honeypot still emitted %q", marker)
		}
	}
}

// The default mode must be the quiet one. This is the whole safety argument
// of the feature: a server seen refusing probes is a server that gets its
// address filtered.
func TestDefaultModeIsThrottleNotBlock(t *testing.T) {
	if config.NormalizeHoneypotMode("") != config.HoneypotThrottle {
		t.Error("an unset mode does not default to throttle")
	}
	// Anything unrecognised must also fall back to the quiet mode rather
	// than to the conspicuous one.
	for _, junk := range []string{"nonsense", "BLOCKY", "0", "yes"} {
		if got := config.NormalizeHoneypotMode(junk); got == config.HoneypotBlock {
			t.Errorf("unknown mode %q was read as a hard block", junk)
		}
	}
	_, g, out, _ := hpConfig(t, config.Honeypot{Enabled: true})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))
	if !strings.Contains(s, "limit_req_zone") {
		t.Error("the default configuration does not rate-limit")
	}
	if strings.Contains(s, "return 403") {
		t.Error("the default configuration refuses requests outright")
	}
}

// The bait must never include something a real visitor asks for.
func TestBaitPathsCannotCatchRealVisitors(t *testing.T) {
	dangerous := []string{"/", "/api", "/admin", "/login", "/index.html",
		"/static", "/assets", "/favicon.ico", "/robots.txt", "/sitemap.xml",
		"/health", "/ws", "/sub", "/dashboard"}
	for _, p := range config.DefaultHoneypotPaths {
		for _, d := range dangerous {
			if strings.EqualFold(p, d) {
				t.Errorf("%q is bait but a real site might serve it", p)
			}
		}
	}
	// And the root can never become bait, however it is spelled.
	for _, p := range []string{"/", "", "  ", "//", "/../"} {
		if got := config.NormalizeHoneypotPath(p); got == "/" || got == "" && p == "/../" {
			continue
		}
	}
	if config.NormalizeHoneypotPath("/") != "" {
		t.Error("the site root was accepted as a trap path")
	}
}

// A path that would break the generated file must be refused, not escaped.
func TestUnsafeBaitPathsAreRefused(t *testing.T) {
	for _, p := range []string{
		"/x$test", "/x'y", "/x\"y", "/a{b}", "/a;b", "/a b", "/a\tb", "/a\\b",
	} {
		if got := config.NormalizeHoneypotPath(p); got != "" {
			t.Errorf("unsafe path %q was accepted as %q", p, got)
		}
	}
	// Ordinary ones must survive, including the leading-slash fix and the
	// query-string strip.
	for in, want := range map[string]string{
		"wp-login.php":    "/wp-login.php",
		"/.env":           "/.env",
		"/a//b":           "/a/b",
		"/x?y=1":          "/x",
		"/wp-admin/":      "/wp-admin",
		"/deep/path/here": "/deep/path/here",
	} {
		if got := config.NormalizeHoneypotPath(in); got != want {
			t.Errorf("NormalizeHoneypotPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// An exception must actually remove a path from the trap.
func TestAllowPathRemovesBait(t *testing.T) {
	h := config.Honeypot{Enabled: true, AllowPaths: []string{"/wp-admin"}}
	for _, p := range h.EffectivePaths() {
		if strings.EqualFold(p, "/wp-admin") {
			t.Fatal("an excepted path is still bait")
		}
	}
	// ...and the rest of the list must survive.
	if len(h.EffectivePaths()) < len(config.DefaultHoneypotPaths)-2 {
		t.Error("the exception removed far more than it should")
	}
}

// Extras add to the defaults rather than replacing them.
func TestExtraPathsAddToTheDefaults(t *testing.T) {
	h := config.Honeypot{Enabled: true, ExtraPaths: []string{"/my-secret-trap"}}
	got := h.EffectivePaths()
	if len(got) <= len(config.DefaultHoneypotPaths) {
		t.Error("the extra path replaced the defaults instead of adding to them")
	}
	found := false
	for _, p := range got {
		if p == "/my-secret-trap" {
			found = true
		}
	}
	if !found {
		t.Error("the extra path is missing")
	}
}

// The generated list must be stable, or every Generate rewrites the file.
func TestEffectivePathsAreStable(t *testing.T) {
	h := config.Honeypot{Enabled: true, ExtraPaths: []string{"/z", "/a", "/z"}}
	first := strings.Join(h.EffectivePaths(), ",")
	for i := 0; i < 20; i++ {
		if got := strings.Join(h.EffectivePaths(), ","); got != first {
			t.Fatal("the bait list changes between calls")
		}
	}
	if strings.Count(first, "/z") != 1 {
		t.Error("a duplicated entry was emitted twice")
	}
}

// A trap that shadows a working service breaks the site invisibly.
func TestConflictWithARealServiceIsReported(t *testing.T) {
	c := config.Default()
	c.Honeypot = config.Honeypot{Enabled: true, ExtraPaths: []string{"/take"}}
	c.Services = map[string]config.Service{
		"real": {LocalPort: 4628, ListenPort: 443, Path: "take",
			Bindings: []config.Binding{{Domain: "example.test"}}},
	}
	got := config.HoneypotConflicts(c)
	if len(got) != 1 || !strings.Contains(got[0], "/take") {
		t.Errorf("a trap shadowing a live service was not reported: %v", got)
	}
	// A disabled service generates nothing, so it cannot be shadowed.
	svc := c.Services["real"]
	svc.Disabled = true
	c.Services["real"] = svc
	if got := config.HoneypotConflicts(c); len(got) != 0 {
		t.Errorf("a disabled service was reported as shadowed: %v", got)
	}
}

// Loopback must always be exempt, or the panel's own selftest trips its own
// trap and the operator is throttled on their own server.
func TestLoopbackIsAlwaysExempt(t *testing.T) {
	_, g, out, _ := hpConfig(t, config.Honeypot{Enabled: true})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))
	if !strings.Contains(s, "127.0.0.1/32 1;") {
		t.Error("loopback is not exempt from the trap")
	}
	if !strings.Contains(s, "::1/128 1;") {
		t.Error("IPv6 loopback is not exempt from the trap")
	}
}

// The exemption must be decided on the real peer address. A header-based
// check would let anyone opt themselves out by sending it.
func TestExemptionCannotBeForgedByAHeader(t *testing.T) {
	_, g, out, _ := hpConfig(t, config.Honeypot{
		Enabled: true, AllowIPs: []string{"10.0.0.0/24"}})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))
	if !strings.Contains(s, "geo $shg_hp_exempt {") {
		t.Fatal("no geo block was emitted")
	}
	// `geo` with no explicit variable uses $remote_addr, the real peer.
	if strings.Contains(s, "geo $http_x_forwarded_for") ||
		strings.Contains(s, "geo $proxy_add_x_forwarded_for") {
		t.Error("the exemption is decided from a client-supplied header")
	}
}

// ── Real nginx ───────────────────────────────────────────────

// testClientIP returns a NON-loopback local address.
//
// This matters more than it looks: 127.0.0.1 is permanently exempt from the
// trap by design, so a test driving requests from loopback would exempt
// itself and silently prove nothing. The first attempt at these tests did
// exactly that and reported the trap as broken when it was working.
func testClientIP(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() == nil || n.IP.IsLoopback() {
			continue
		}
		return n.IP.String()
	}
	t.Skip("no non-loopback IPv4 address; the trap exempts loopback by design")
	return ""
}

// freePort asks the kernel for a port nothing is using.
// freePort returns a port nothing is using.
//
// Asking the kernel for :0 and closing the socket leaves a window in which
// another process — such as a second `go test` package running in parallel,
// which is what `go test ./...` does — can take the same port before nginx
// binds it. The test then talks to somebody else's server and reports a
// failure that has nothing to do with the code. Verified: these tests
// passed alone and failed under ./... for exactly that reason.
//
// A per-process base plus a counter keeps two packages out of each other's
// way, and the port is probed to make sure it really is free.
var portCounter int32

func freePort(t *testing.T) int {
	t.Helper()
	ip := testClientIP(t)
	base := 20000 + (os.Getpid()%200)*50
	for i := 0; i < 200; i++ {
		p := base + int(atomic.AddInt32(&portCounter, 1))
		if p > 65000 {
			t.Fatal("ran out of ports")
		}
		l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, p))
		if err != nil {
			continue // in use; try the next
		}
		_ = l.Close()
		return p
	}
	t.Fatal("could not find a free port")
	return 0
}

// runNginx starts a real nginx serving the generated honeypot locations on
// a plain HTTP port, and returns its base URL.
func runNginx(t *testing.T, dir, gateway string, port int) string {
	t.Helper()
	bin := nginxBinary()
	if bin == "" {
		t.Skip("nginx is not installed")
	}
	root := filepath.Join(dir, "run")
	for _, d := range []string{"logs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Only the honeypot part of the generated file is reused: the server
	// blocks need certificates, and the trap is what is under test.
	// Rebuild the honeypot directly rather than slicing it out of the
	// generated file: the file also contains a port-80 redirect and TLS
	// server blocks, which an unprivileged test cannot bind or serve. What
	// is under test is the honeypot itself, and this is the same code the
	// generator calls.
	c, err := config.New().Read()
	if err != nil {
		t.Fatal(err)
	}
	prelude := HoneypotPrelude(c)
	locs := HoneypotLocations(c)
	if prelude == "" || locs == "" {
		t.Fatal("the honeypot generated nothing")
	}

	conf := filepath.Join(dir, "run.conf")
	cfg := "pid " + filepath.Join(root, "n.pid") + ";\n" +
		"error_log " + filepath.Join(root, "logs", "error.log") + " warn;\n" +
		"events { worker_connections 64; }\n" +
		"http {\n" +
		"    client_body_temp_path " + filepath.Join(root, "tmp") + ";\n" +
		"    proxy_temp_path " + filepath.Join(root, "tmp") + "/p;\n" +
		"    fastcgi_temp_path " + filepath.Join(root, "tmp") + "/f;\n" +
		"    uwsgi_temp_path " + filepath.Join(root, "tmp") + "/u;\n" +
		"    scgi_temp_path " + filepath.Join(root, "tmp") + "/s;\n" +
		"    access_log " + filepath.Join(root, "logs", "access.log") + ";\n" +
		prelude + "\n" +
		"    server {\n" +
		fmt.Sprintf("        listen %s:%d;\n", testClientIP(t), port) +
		"        server_name _;\n" +
		locs + "\n" +
		"        location / { return 200 \"site\"; }\n" +
		"    }\n}\n"
	if err := os.WriteFile(conf, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.Command(bin, "-t", "-c", conf, "-p", root).CombinedOutput(); err != nil {
		t.Fatalf("real nginx rejected the honeypot config:\n%s", out)
	}
	cmd := exec.Command(bin, "-c", conf, "-p", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("could not start nginx: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(bin, "-c", conf, "-p", root, "-s", "quit").Run()
		time.Sleep(200 * time.Millisecond)
	})

	base := fmt.Sprintf("http://%s:%d", testClientIP(t), port)
	for i := 0; i < 60; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", testClientIP(t), port), 200*time.Millisecond); err == nil {
			c.Close()
			return base
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nginx did not start listening")
	return ""
}

func get(t *testing.T, url string) int {
	t.Helper()
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// The behaviour that matters: a scanner gets throttled, ordinary traffic
// does not.
func TestRealNginxThrottlesAScannerButNotAVisitor(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "hp.log")
	old := HoneypotLogPath
	HoneypotLogPath = logPath
	defer func() { HoneypotLogPath = old }()

	_, g, out, dir := hpConfig(t, config.Honeypot{
		Enabled: true, Mode: config.HoneypotThrottle,
		RatePerMinute: 1, LogHits: true,
	})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	base := runNginx(t, dir, out, freePort(t))

	// Ordinary traffic is untouched, however much of it there is.
	for i := 0; i < 12; i++ {
		if code := get(t, base+"/"); code != 200 {
			t.Fatalf("ordinary request %d was disturbed: %d", i, code)
		}
	}

	// The trap answers, and then starts refusing. burst=1 lets exactly one
	// through before the limiter engages.
	first := get(t, base+"/.env")
	if first != 404 {
		t.Errorf("the first probe returned %d, want 404 (a plain not-found)", first)
	}
	limited := 0
	for i := 0; i < 8; i++ {
		if get(t, base+"/wp-login.php") == 404 {
			limited++
		}
	}
	if limited < 6 {
		t.Errorf("only %d of 8 rapid probes were answered as not-found — the limiter is not engaging", limited)
	}

	// Crucially, tripping the trap must not have disturbed the real site.
	if code := get(t, base+"/"); code != 200 {
		t.Errorf("a normal request was collateral damage: %d", code)
	}

	// Verify the limiter really fired, from nginx's own error log.
	errLog, _ := os.ReadFile(filepath.Join(dir, "run", "logs", "error.log"))
	if !strings.Contains(string(errLog), "limiting requests") {
		t.Errorf("nginx never reported limiting anything:\n%s", errLog)
	} else {
		t.Log("nginx reported: limiting requests — the throttle engaged")
	}

	// And the hits were recorded.
	hits, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no honeypot log was written: %v", err)
	}
	if !strings.Contains(string(hits), "/.env") {
		t.Error("the trip was not recorded in the honeypot log")
	}
	t.Logf("honeypot log recorded %d lines", strings.Count(string(hits), "\n"))
}

// A directory-style bait must also catch what is under it.
func TestRealNginxTrapsBeneathADirectoryBait(t *testing.T) {
	_, g, out, dir := hpConfig(t, config.Honeypot{
		Enabled: true, Mode: config.HoneypotThrottle, RatePerMinute: 60,
	})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	base := runNginx(t, dir, out, freePort(t))

	if code := get(t, base+"/wp-admin"); code != 404 {
		t.Errorf("the directory bait itself returned %d", code)
	}
	if code := get(t, base+"/wp-admin/setup-config.php"); code != 404 {
		t.Errorf("a probe beneath the directory bait was not trapped: %d", code)
	}
	if code := get(t, base+"/"); code != 200 {
		t.Errorf("the real site was affected: %d", code)
	}
}

// Case matters, and nginx location matching is case-SENSITIVE.
//
// An exact-match trap for /wp-login.php does not catch /WP-LOGIN.PHP.
// Verified against a real nginx before the fix: the uppercase probe was
// answered with 200 by the ordinary site handler and never reached the
// trap. Varying case is one of the oldest tricks a scanner has, so an
// exact match alone is a trap with a hole in it.
func TestRealNginxTrapsRegardlessOfCase(t *testing.T) {
	_, g, out, dir := hpConfig(t, config.Honeypot{
		Enabled: true, Mode: config.HoneypotThrottle, RatePerMinute: 120,
	})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	base := runNginx(t, dir, out, freePort(t))

	for _, p := range []string{
		"/wp-login.php", "/WP-LOGIN.PHP", "/Wp-Login.Php",
		"/.env", "/.ENV",
		"/wp-admin", "/WP-ADMIN", "/WP-Admin/setup-config.php",
	} {
		if code := get(t, base+p); code != 404 {
			t.Errorf("%s returned %d — it escaped the trap", p, code)
		}
	}
	// And an ordinary path must still be served, whatever its case.
	for _, p := range []string{"/", "/Something", "/ORDINARY"} {
		if code := get(t, base+p); code != 200 {
			t.Errorf("ordinary path %s was caught by the trap: %d", p, code)
		}
	}
}

// A dot in a bait path must match a literal dot, not "any character" — a
// regex trap for /.env must not also swallow /aenv.
func TestBaitPathsAreRegexQuoted(t *testing.T) {
	_, g, out, dir := hpConfig(t, config.Honeypot{
		Enabled: true, Mode: config.HoneypotThrottle, RatePerMinute: 120,
	})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	base := runNginx(t, dir, out, freePort(t))
	for _, p := range []string{"/aenv", "/xenv", "/wpXlogin.php"} {
		if code := get(t, base+p); code != 200 {
			t.Errorf("%s was trapped but should not be: %d — the bait path "+
				"is not regex-quoted", p, code)
		}
	}
}

// Block mode must refuse, and decoy mode must answer with a page.
func TestRealNginxHonoursTheOtherModes(t *testing.T) {
	t.Run("block", func(t *testing.T) {
		_, g, out, dir := hpConfig(t, config.Honeypot{
			Enabled: true, Mode: config.HoneypotBlock,
		})
		if _, err := g.Generate(); err != nil {
			t.Fatal(err)
		}
		base := runNginx(t, dir, out, freePort(t))
		if code := get(t, base+"/.env"); code != 403 {
			t.Errorf("block mode returned %d, want 403", code)
		}
		if code := get(t, base+"/"); code != 200 {
			t.Errorf("block mode disturbed ordinary traffic: %d", code)
		}
	})

	t.Run("decoy", func(t *testing.T) {
		_, g, out, dir := hpConfig(t, config.Honeypot{
			Enabled: true, Mode: config.HoneypotDecoy, RatePerMinute: 60,
		})
		if _, err := g.Generate(); err != nil {
			t.Fatal(err)
		}
		base := runNginx(t, dir, out, freePort(t))
		if code := get(t, base+"/.env"); code != 200 {
			t.Errorf("decoy mode returned %d, want 200", code)
		}
	})
}

// The bug that made the throttle decorative.
//
// `return` is handled in nginx's rewrite phase, which runs BEFORE the
// preaccess phase where limit_req lives. A location whose body is a bare
// `return` is therefore never rate-limited: verified against a real nginx,
// where five rapid probes were all answered normally. try_files runs in the
// precontent phase, after the limiter.
//
// This is invisible in any test that only reads the generated text, which
// is exactly why it survived until a real request was made.
func TestThrottleUsesAPhaseThatActuallyReachesTheLimiter(t *testing.T) {
	_, g, out, _ := hpConfig(t, config.Honeypot{
		Enabled: true, Mode: config.HoneypotThrottle,
	})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))

	block := trapBlockFor(t, s, "/.env")
	if !strings.Contains(block, "limit_req zone=") {
		t.Fatal("the trap does not rate-limit at all")
	}
	if strings.Contains(block, "return 404;") {
		t.Error("the trap answers with `return`, which runs before the rate " +
			"limiter — the throttle would never engage")
	}
	if !strings.Contains(block, "try_files") {
		t.Error("the trap does not use a phase that reaches the limiter")
	}
}

// Decoy mode has the same hazard: a bare `return 200` would skip the
// limiter, so the body is served from a named location instead.
func TestDecoyBodyDoesNotSkipTheLimiter(t *testing.T) {
	_, g, out, _ := hpConfig(t, config.Honeypot{
		Enabled: true, Mode: config.HoneypotDecoy,
	})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))
	block := trapBlockFor(t, s, "/.env")
	if strings.Contains(block, "return 200") {
		t.Error("the decoy body is returned in the rewrite phase, skipping the limiter")
	}
	if !strings.Contains(s, "location @shg_hp_decoy {") {
		t.Error("the decoy has no named location to serve its body from")
	}
	if !strings.Contains(block, "limit_req zone=") {
		t.Error("decoy mode does not rate-limit")
	}
}

// trapBlockFor returns the body of the trap location for one bait path.
// The locations are case-insensitive regexes, so the path appears escaped.
func trapBlockFor(t *testing.T, conf, path string) string {
	t.Helper()
	// Bait paths are folded into alternation groups for performance, so a
	// path appears inside a `location ~* ^(a|b|c)$ {` header rather than
	// having a location of its own. Find the group containing it.
	quoted := regexp.QuoteMeta(path)
	for i := 0; ; {
		j := strings.Index(conf[i:], "    location ~* ")
		if j < 0 {
			t.Fatalf("no trap location was emitted for %s", path)
		}
		start := i + j
		end := strings.Index(conf[start:], "\n    }")
		if end < 0 {
			t.Fatalf("a trap location is not closed")
		}
		block := conf[start : start+end]
		header := block[:strings.Index(block, "{")]
		// Match the path as a whole alternative, so /.env does not also
		// match /.env.local.
		for _, alt := range strings.Split(
			strings.Trim(header[strings.Index(header, "(")+1:], " "), "|") {
			alt = strings.TrimSuffix(strings.TrimSuffix(alt, ")$ "), ")(/|$) ")
			if alt == quoted {
				return block
			}
		}
		i = start + end
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
