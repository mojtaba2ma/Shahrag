package nginx

// End-to-end attacker simulation.
//
// Everything else tests a piece. This starts a REAL nginx, plays the part
// of a scanner against it, and checks the whole chain: the trap fires, the
// ban engine reads nginx's own log, the ban list is regenerated, nginx is
// reloaded, and the attacker is then refused while an ordinary visitor
// keeps being served throughout.
//
// This is the only test that can catch a break between the parts, and it
// found two of them.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
)

// stubBans is a ban list the test controls directly, standing in for the
// engine so this package does not import it (which would be a cycle).
type stubBans struct{ ips []string }

func (s *stubBans) BannedIPs(max int) []string {
	if max > 0 && len(s.ips) > max {
		return s.ips[:max]
	}
	return s.ips
}

// attackFixture builds a config with the trap and bans both on.
func attackFixture(t *testing.T, action string) (*config.Manager, *Generator, string, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	// banProvider is a package-level global, so a test that leaves it set
	// poisons every test after it: the next fixture generated a config
	// that banned the test client and the site answered 404 for reasons
	// that had nothing to do with what was being tested. Registered here
	// so every user of this fixture is cleaned up, and via t.Cleanup so it
	// unwinds in the right order relative to nginx being stopped.
	t.Cleanup(func() { SetBanProvider(nil) })

	// The default honeypot log lives under /var/log/nginx, which an
	// unprivileged test cannot write. Redirect it for the duration.
	oldLog := HoneypotLogPath
	HoneypotLogPath = filepath.Join(dir, "honeypot.log")
	t.Cleanup(func() { HoneypotLogPath = oldLog })

	mgr := config.New()
	if err := mgr.AddDomain("example.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "gateway.conf")
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Honeypot = config.Honeypot{
			Enabled: true, Mode: config.HoneypotThrottle,
			RatePerMinute: 120, LogHits: true,
		}
		c.AutoBan = config.DefaultAutoBan()
		c.AutoBan.Enabled = true
		c.AutoBan.Action = action
		c.AutoBan.ThrottleRate = 1
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

// liveServer starts nginx serving the honeypot and ban rules, and returns a
// base URL plus a reload function.
func liveServer(t *testing.T, dir string, port int) (string, func()) {
	t.Helper()
	bin := nginxBinary()
	if bin == "" {
		t.Skip("nginx is not installed")
	}
	root := filepath.Join(dir, "run")
	for _, d := range []string{"logs", "tmp", "www"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "www", "index.html"),
		[]byte("site"), 0o644); err != nil {
		t.Fatal(err)
	}
	ip := testClientIP(t)
	conf := filepath.Join(dir, "run.conf")
	access := filepath.Join(root, "logs", "access.log")

	write := func() {
		c, err := config.New().Read()
		if err != nil {
			t.Fatal(err)
		}
		body := "pid " + filepath.Join(root, "n.pid") + ";\n" +
			"error_log " + filepath.Join(root, "logs", "error.log") + " warn;\n" +
			"events { worker_connections 64; }\n" +
			"http {\n" +
			"    client_body_temp_path " + filepath.Join(root, "tmp") + ";\n" +
			"    proxy_temp_path " + filepath.Join(root, "tmp") + "/p;\n" +
			"    fastcgi_temp_path " + filepath.Join(root, "tmp") + "/f;\n" +
			"    uwsgi_temp_path " + filepath.Join(root, "tmp") + "/u;\n" +
			"    scgi_temp_path " + filepath.Join(root, "tmp") + "/s;\n" +
			"    access_log " + access + ";\n" +
			HoneypotPrelude(c) +
			AutoBanPrelude(c) +
			"    server {\n" +
			fmt.Sprintf("        listen %s:%d;\n", ip, port) +
			"        server_name _;\n" +
			AutoBanServerGuard(c) +
			HoneypotLocations(c) +
			// A realistic location: the generated config answers clients
			// from a phase AFTER limit_req (proxy_pass, try_files), while
			// a bare `return` runs before it and would make this test
			// measure the wrong thing.
			//
			// The body comes from a named location rather than a file on
			// disk. Under sudo, nginx workers drop to www-data and cannot
			// read a test's temp directory — which produced a 404 with
			// "stat() failed (13: Permission denied)" that looked exactly
			// like a ban and had nothing to do with one.
			"        location / { error_page 418 = @ok; return 418; }\n" +
			"        location @ok { internal; try_files /__none__ @ok2; }\n" +
			"        location @ok2 { internal; default_type text/plain; return 200 \"site\"; }\n" +
			"    }\n}\n"
		if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write()
	if out, err := exec.Command(bin, "-t", "-c", conf, "-p", root).CombinedOutput(); err != nil {
		t.Fatalf("nginx rejected the config:\n%s", out)
	}
	if out, err := exec.Command(bin, "-c", conf, "-p", root).CombinedOutput(); err != nil {
		t.Fatalf("could not start nginx: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(bin, "-c", conf, "-p", root, "-s", "quit").Run()
		time.Sleep(200 * time.Millisecond)
	})

	base := fmt.Sprintf("http://%s:%d", ip, port)
	for i := 0; i < 60; i++ {
		if cn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), 200*time.Millisecond); err == nil {
			cn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	reload := func() {
		write()
		if out, err := exec.Command(bin, "-t", "-c", conf, "-p", root).CombinedOutput(); err != nil {
			t.Fatalf("nginx rejected the regenerated config:\n%s", out)
		}
		if out, err := exec.Command(bin, "-c", conf, "-p", root, "-s", "reload").CombinedOutput(); err != nil {
			t.Fatalf("reload failed: %v\n%s", err, out)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return base, reload
}

func status(t *testing.T, url string) int {
	t.Helper()
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// The whole chain, played out against a running nginx.
func TestAttackerIsTrappedThenBanned(t *testing.T) {
	_, g, _, dir := attackFixture(t, config.BanActionNotFound)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	stub := &stubBans{}
	SetBanProvider(stub)

	base, reload := liveServer(t, dir, freePort(t))
	attacker := testClientIP(t)

	// ── Act one: an ordinary visitor is served. ──
	if code := status(t, base+"/"); code != 200 {
		t.Fatalf("the site is not serving: %d", code)
	}

	// ── Act two: the scanner walks its wordlist and is trapped. ──
	for _, p := range []string{"/.env", "/wp-login.php", "/phpmyadmin", "/.git/config"} {
		if code := status(t, base+p); code != 404 {
			t.Errorf("probe %s returned %d, expected the trap's 404", p, code)
		}
	}
	// The site is still perfectly usable while all this happens.
	if code := status(t, base+"/"); code != 200 {
		t.Errorf("the trap disturbed ordinary traffic: %d", code)
	}

	// ── Act three: the engine bans the attacker and nginx is reloaded. ──
	stub.ips = []string{attacker}
	reload()

	// Now everything the attacker asks for is a plain not-found, including
	// the ordinary page that worked a moment ago.
	if code := status(t, base+"/"); code != 404 {
		t.Errorf("a banned client still got %d from the ordinary page", code)
	}
	if code := status(t, base+"/.env"); code != 404 {
		t.Errorf("a banned client got %d from a trap path", code)
	}

	// ── Act four: the ban lapses and the address is served again. ──
	stub.ips = nil
	reload()
	if code := status(t, base+"/"); code != 200 {
		t.Errorf("after the ban expired the client is still refused: %d", code)
	}
	t.Log("trap → ban → refuse → release all verified against a running nginx")
}

// A banned client must be refused everywhere the host serves, not only on
// the paths that existed when the ban was written.
func TestBanCoversTheWholeHost(t *testing.T) {
	_, g, _, dir := attackFixture(t, config.BanActionNotFound)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	stub := &stubBans{ips: []string{testClientIP(t)}}
	SetBanProvider(stub)

	base, _ := liveServer(t, dir, freePort(t))
	for _, p := range []string{"/", "/anything", "/deep/path/here", "/.env"} {
		if code := status(t, base+p); code != 404 {
			t.Errorf("%s returned %d for a banned client", p, code)
		}
	}
}

// The forbidden action is honest about what it does.
func TestBanActionForbidden(t *testing.T) {
	_, g, _, dir := attackFixture(t, config.BanActionForbidden)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	SetBanProvider(&stubBans{ips: []string{testClientIP(t)}})
	base, _ := liveServer(t, dir, freePort(t))
	if code := status(t, base+"/"); code != 403 {
		t.Errorf("forbidden mode returned %d, want 403", code)
	}
}

// Throttle mode slows a banned client without refusing it outright.
func TestBanActionThrottle(t *testing.T) {
	_, g, _, dir := attackFixture(t, config.BanActionThrottle)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	SetBanProvider(&stubBans{ips: []string{testClientIP(t)}})
	base, _ := liveServer(t, dir, freePort(t))

	// The first request goes through (burst=1); the rest are limited.
	first := status(t, base+"/")
	limited := 0
	for i := 0; i < 6; i++ {
		if status(t, base+"/") == 404 {
			limited++
		}
	}
	if first != 200 {
		t.Errorf("the first request was refused outright: %d", first)
	}
	if limited < 4 {
		t.Errorf("only %d of 6 follow-up requests were limited", limited)
	}
	errLog, _ := os.ReadFile(filepath.Join(dir, "run", "logs", "error.log"))
	if !strings.Contains(string(errLog), "limiting requests") {
		t.Error("nginx never reported limiting a banned client")
	}
}

// An empty ban list must not change the config's meaning, and nginx must
// still accept it — the state every server is in before anyone is banned.
func TestEmptyBanListIsHarmless(t *testing.T) {
	_, g, _, dir := attackFixture(t, config.BanActionNotFound)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	SetBanProvider(&stubBans{})
	base, _ := liveServer(t, dir, freePort(t))
	if code := status(t, base+"/"); code != 200 {
		t.Errorf("an empty ban list refused an ordinary client: %d", code)
	}
}

// Loopback must never be refused, even if it somehow ends up on the list:
// locking the server out of itself is unrecoverable from the panel.
func TestLoopbackIsNeverRefusedByABan(t *testing.T) {
	_, g, out, _ := attackFixture(t, config.BanActionNotFound)
	SetBanProvider(&stubBans{ips: []string{"127.0.0.1"}})
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))
	// The explicit 0 for loopback is emitted after `default`, and geo
	// resolves the most specific match, so it wins over the ban entry.
	i := strings.Index(s, "geo $shg_banned {")
	if i < 0 {
		t.Fatal("no ban list was emitted")
	}
	block := s[i : i+strings.Index(s[i:], "}")]
	if !strings.Contains(block, "127.0.0.1/32 0;") {
		t.Error("loopback is not pinned to 'not banned'")
	}
}

// ── The per-service address lock ─────────────────────────────

// An address lock must work WITHOUT the bot shield: that is the whole point
// of the feature.
func TestServiceAddressLockWithoutTheShield(t *testing.T) {
	t.Cleanup(func() { SetBanProvider(nil) })
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	if err := mgr.AddDomain("example.test", "/c.pem", "/c.key"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddService("xray", "x", "example.test", 4628, 8443, "cfg", true, false); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "gateway.conf")
	if _, err := mgr.Mutate(func(c *config.Config) error {
		svc := c.Services["xray"]
		svc.AllowIPs = []string{"203.0.113.7", "10.0.0.0/24"}
		c.Services["xray"] = svc
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
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))

	if !strings.Contains(s, "geo $shg_ip_xray {") {
		t.Fatal("no address lock was generated")
	}
	if !strings.Contains(s, "203.0.113.7 1;") || !strings.Contains(s, "10.0.0.0/24 1;") {
		t.Error("the allowed addresses are missing from the lock")
	}
	if !strings.Contains(s, "if ($shg_ip_xray = 0) { return 404; }") {
		t.Error("the location does not consult the lock")
	}
	// The shield is off, and must have generated nothing.
	if strings.Contains(s, "shg_xray_ok") {
		t.Error("the address lock dragged the bot shield in with it")
	}
}

// An empty list means everyone, which is both the historical behaviour and
// the only safe default.
func TestEmptyAddressLockAllowsEveryone(t *testing.T) {
	t.Cleanup(func() { SetBanProvider(nil) })
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	_ = mgr.AddDomain("example.test", "/c.pem", "/c.key")
	_ = mgr.AddService("open", "o", "example.test", 3000, 8443, "open", true, false)
	out := filepath.Join(dir, "gateway.conf")
	if _, err := mgr.Mutate(func(c *config.Config) error {
		svc := c.Services["open"]
		svc.AllowIPs = []string{"", "   "} // blanks must not count
		c.Services["open"] = svc
		c.Nginx.OutputPath = out
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(mgr)
	g.NginxConf = filepath.Join(dir, "nginx.conf")
	_ = os.WriteFile(g.NginxConf, []byte("events{}\nhttp{}\n"), 0o644)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustReadFile(t, out))
	if strings.Contains(s, "shg_ip_open") {
		t.Error("an empty address list still generated a lock, which would " +
			"black out the service")
	}
}

// The lock must be decided on the real peer address, not a header.
func TestAddressLockCannotBeForged(t *testing.T) {
	got := ServiceAllowIPBlock("svc", []string{"203.0.113.1"})
	if !strings.Contains(got, "geo $shg_ip_svc {") {
		t.Fatal("no geo block")
	}
	// `geo` with no explicit source variable reads $remote_addr.
	if strings.Contains(got, "$http_x_forwarded_for") ||
		strings.Contains(got, "$proxy_add_x_forwarded_for") {
		t.Error("the lock is decided from a client-supplied header")
	}
}
