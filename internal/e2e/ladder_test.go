package e2e

// The whole chain, played out against a REAL nginx.
//
// Everything else tests a piece: the config validates a ladder, the engine
// climbs it, the API serves it. This starts nginx, plays the part of a
// scanner three separate times, and checks that the SAME address got three
// different, increasing bans — and that between them nginx really did
// refuse it and really did serve it again once the ban was lifted.
//
// It lives in its own package because it needs both `banner` and `nginx`,
// and putting it in either would be the wrong home for it.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"shahrag/internal/banner"
	"shahrag/internal/config"
	nginxpkg "shahrag/internal/nginx"
)

// nginxBinary finds nginx. It is not on PATH on Debian.
func nginxBinary() string {
	for _, p := range []string{"/usr/sbin/nginx", "/usr/local/sbin/nginx", "/usr/bin/nginx"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	p, _ := exec.LookPath("nginx")
	return p
}

// clientIP returns a non-loopback local address. Loopback is permanently
// exempt from both the trap and bans, so a test driven from 127.0.0.1
// would pass no matter what the code did.
func clientIP(t *testing.T) string {
	t.Helper()
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		t.Skip("no interfaces")
	}
	for _, a := range ifaces {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			return n.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 address; the trap and bans both exempt loopback")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type banList struct{ ips []string }

func (b *banList) BannedIPs(max int) []string { return b.ips }

func get(t *testing.T, url string) int {
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// The full story: trap → ban at step 1 → release → trap again → ban at
// step 2 (longer) → release → trap again → ban at step 3 (longer still),
// with a real nginx refusing the address in between.
func TestScannerThatKeepsComingBackGetsProgressivelyLongerBans(t *testing.T) {
	bin := nginxBinary()
	if bin == "" {
		t.Skip("nginx is not installed")
	}
	ip := clientIP(t)
	dir := t.TempDir()

	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	hpLog := filepath.Join(dir, "honeypot.log")
	accessLog := filepath.Join(dir, "run", "logs", "access.log")

	oldHP := nginxpkg.HoneypotLogPath
	nginxpkg.HoneypotLogPath = hpLog
	t.Cleanup(func() { nginxpkg.HoneypotLogPath = oldHP })
	t.Cleanup(func() { nginxpkg.SetBanProvider(nil) })

	mgr := config.New()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Honeypot = config.Honeypot{
			Enabled: true, Mode: config.HoneypotThrottle,
			RatePerMinute: 600, LogHits: true, Configured: true,
		}
		c.AutoBan = config.DefaultAutoBan()
		c.AutoBan.Enabled = true
		c.AutoBan.Configured = true
		c.AutoBan.Action = config.BanActionNotFound
		// One bait hit is enough, so the test drives one rung per round
		// instead of simulating a full wordlist three times.
		c.AutoBan.Honeypot = config.AutoBanRule{
			Enabled: true, Hits: 1, WindowMinutes: 60, BanMinutes: 1}
		c.AutoBan.AuthFail.Enabled = false
		c.AutoBan.NotFound.Enabled = false
		c.AutoBan.ErrorRate.Enabled = false
		c.AutoBan.Escalation = config.BanEscalation{
			Enabled: true, Steps: []int{30, 120, config.PermanentStep}, DecayHours: 72}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	bans := &banList{}
	nginxpkg.SetBanProvider(bans)

	// ── a real nginx serving the trap and the ban guard ──
	root := filepath.Join(dir, "run")
	for _, d := range []string{"logs", "tmp", "www"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	port := freePort(t)
	conf := filepath.Join(dir, "run.conf")

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
			"    access_log " + accessLog + ";\n" +
			nginxpkg.HoneypotPrelude(c) +
			nginxpkg.AutoBanPrelude(c) +
			"    server {\n" +
			fmt.Sprintf("        listen %s:%d;\n", ip, port) +
			"        server_name _;\n" +
			nginxpkg.AutoBanServerGuard(c) +
			nginxpkg.HoneypotLocations(c) +
			// Answered from a phase AFTER limit_req, as the real
			// generated config is; a bare `return` runs before it.
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

	// The engine reads nginx's own honeypot log — no stub anywhere.
	banner.StatePath = filepath.Join(dir, "bans.json")
	banner.BanLogPath = filepath.Join(dir, "bans.log")
	eng := banner.New(mgr, hpLog, accessLog, nil)
	// Start(), not just New(): the running service primes its log cursors
	// before its first scan, and a log that nginx has not created yet has
	// to be picked up from its beginning when it appears. That is the r45
	// bug — an engine that never primed treated the first sighting of the
	// honeypot log as "start at the end" and missed every hit already in
	// it. Testing without Start() would test a configuration that never
	// runs in production.
	// Prime BEFORE Start, synchronously. This test drives Scan() by hand,
	// which the running service never does, and priming on Start's
	// goroutine races that call: found under -race, where priming landed
	// after the first trap hit and seeked straight past it, so round one
	// saw no ban at all. Ordering it explicitly removes the race rather
	// than hiding it behind a sleep.
	eng.Prime()
	eng.Start()
	t.Cleanup(eng.Stop)

	if code := get(t, base+"/"); code != 200 {
		t.Fatalf("the site is not serving before any of this starts: %d", code)
	}

	type round struct {
		wantMinutes int
		wantPerm    bool
	}
	rounds := []round{{30, false}, {120, false}, {0, true}}

	for i, want := range rounds {
		// The scanner trips the trap. This is a REAL request through
		// nginx, which writes the honeypot log line the engine reads.
		if code := get(t, base+"/.env"); code != 404 {
			t.Fatalf("round %d: the trap returned %d, want 404", i+1, code)
		}
		eng.Scan()

		var got banner.Ban
		found := false
		for _, b := range eng.ActiveBans() {
			if b.IP == ip {
				got, found = b, true
			}
		}
		if !found {
			t.Fatalf("round %d: the scanner was not banned at all", i+1)
		}
		if got.Level != i+1 {
			t.Errorf("round %d: banned at level %d, want %d", i+1, got.Level, i+1)
		}
		if want.wantPerm {
			if !got.Permanent {
				t.Errorf("round %d: the ban is not permanent", i+1)
			}
		} else {
			mins := int(got.ExpiresAt.Sub(got.BannedAt).Round(time.Minute) / time.Minute)
			if mins != want.wantMinutes {
				t.Errorf("round %d: the ban lasts %d minutes, want %d", i+1, mins, want.wantMinutes)
			}
		}
		t.Logf("round %d: level %d, permanent=%v, %v",
			i+1, got.Level, got.Permanent, got.ExpiresAt.Sub(got.BannedAt).Round(time.Minute))

		// nginx really refuses it.
		bans.ips = []string{ip}
		reload()
		if code := get(t, base+"/"); code != 404 {
			t.Errorf("round %d: a banned client still got %d from the ordinary page", i+1, code)
		}

		// The ban is lifted (as an expiry would), the ledger is kept.
		bans.ips = nil
		reload()
		eng.Unban(ip)
		if code := get(t, base+"/"); code != 200 {
			t.Errorf("round %d: after release the client is still refused: %d", i+1, code)
		}
	}

	// The ledger says what it will do next, and it is the permanent step.
	offs := eng.Offenders()
	if len(offs) == 0 {
		t.Fatal("the ledger is empty after three bans")
	}
	var mine *banner.OffenderInfo
	for i := range offs {
		if offs[i].IP == ip {
			mine = &offs[i]
		}
	}
	if mine == nil {
		t.Fatalf("the scanner is not in the ledger; it has %v", offs)
	}
	if mine.TotalBans != 3 {
		t.Errorf("the ledger records %d bans, want 3", mine.TotalBans)
	}
	if mine.NextMinutes != -1 {
		t.Errorf("the next ban is %d minutes; it should be permanent", mine.NextMinutes)
	}
	t.Logf("ledger: level %d, %d bans in total, next ban permanent, drops a step at %s",
		mine.Level, mine.TotalBans, mine.DecaysAt.Format(time.RFC3339))
}
