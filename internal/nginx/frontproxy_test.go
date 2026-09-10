package nginx

// A CDN in front of the server.
//
// The regression test for a real incident: with Cloudflare in front, the
// honeypot and the ban engine started recording Cloudflare and Google
// addresses. The detectors were working perfectly — the address they could
// see was the CDN's edge, so one scanner arriving through Cloudflare got
// Cloudflare banned, and Cloudflare is the whole site.
//
// Everything here runs against a real nginx, because the failure and the
// fix are both about what nginx actually reports as $remote_addr.

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
)

func cfConfig() *config.Config {
	c := config.Default()
	c.TrustedProxies = config.TrustedProxies{
		Enabled: map[string]bool{config.ProxyCloudflare: true},
	}
	return c
}

// Nothing is emitted until the operator enables a list. An upgrade must not
// silently start trusting headers.
func TestNothingIsTrustedByDefault(t *testing.T) {
	c := config.Default()
	if got := FrontProxyPrelude(c); got != "" {
		t.Fatalf("a fresh install already trusts something:\n%s", got)
	}
	if got := c.TrustedProxies.NeverBanRanges(); len(got) != 0 {
		t.Fatalf("a fresh install already exempts %d ranges", len(got))
	}
}

func TestEnablingCloudflareEmitsItsRanges(t *testing.T) {
	got := FrontProxyPrelude(cfConfig())
	if !strings.Contains(got, "real_ip_header CF-Connecting-IP;") {
		t.Fatalf("no CF header configured:\n%s", got)
	}
	// A couple of Cloudflare's published ranges.
	for _, want := range []string{"104.16.0.0/13", "173.245.48.0/20", "2606:4700::/32"} {
		if !strings.Contains(got, "set_real_ip_from "+want+";") {
			t.Errorf("missing range %s", want)
		}
	}
	// And nothing wide.
	for _, bad := range []string{"0.0.0.0/0", "::/0"} {
		if strings.Contains(got, bad) {
			t.Fatalf("the trust list contains %s, which lets anyone claim any address", bad)
		}
	}
}

// The security property, proven against a real nginx: with the trust list
// in place a forwarded header from a TRUSTED peer is believed, and the same
// header from an untrusted peer is ignored.
//
// If it were believed from anywhere, a caller who can reach the origin
// directly could claim to be any address at all and walk past every ban, IP
// lock and honeypot exemption in the panel.
func TestAForgedHeaderIsIgnoredFromAnUntrustedPeer(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	dir := t.TempDir()
	port := freePort(t)

	// Trust a range this machine is definitely NOT in.
	c := config.Default()
	c.TrustedProxies = config.TrustedProxies{
		Enabled:           map[string]bool{},
		ExtraTrustedCIDRs: []string{"198.51.100.0/24"},
	}

	conf := filepath.Join(dir, "nginx.conf")
	full := fmt.Sprintf(`worker_processes 1;
error_log %s/error.log warn;
pid %s/nginx.pid;
events { worker_connections 32; }
http {
  client_body_temp_path %s/b; proxy_temp_path %s/p;
  fastcgi_temp_path %s/f; uwsgi_temp_path %s/u; scgi_temp_path %s/s;
  access_log off;
%s
  server {
    listen 127.0.0.1:%d;
    location / { return 200 "seen:$remote_addr\n"; }
  }
}
`, dir, dir, dir, dir, dir, dir, dir, FrontProxyPrelude(c), port)

	if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(bin, "-t", "-c", conf, "-p", dir).CombinedOutput()
	if !strings.Contains(string(out), "syntax is ok") {
		t.Fatalf("nginx rejected the generated config:\n%s\n%s", out, full)
	}
	if err := exec.Command(bin, "-c", conf, "-p", dir).Run(); err != nil {
		t.Skipf("cannot start nginx here: %v", err)
	}
	defer exec.Command(bin, "-c", conf, "-p", dir, "-s", "stop").Run()
	time.Sleep(300 * time.Millisecond)

	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Skipf("could not reach the fixture: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	got := strings.TrimSpace(string(buf[:n]))

	if got == "seen:1.2.3.4" {
		t.Fatal("a forged X-Forwarded-For from an UNTRUSTED peer was believed.\n" +
			"Anybody able to reach the origin could claim any address and\n" +
			"bypass every ban, IP lock and honeypot exemption in the panel.")
	}
	if got != "seen:127.0.0.1" {
		t.Fatalf("unexpected: %q", got)
	}
	t.Logf("an untrusted peer's forged header was correctly ignored (%s)", got)
}

// ...and the same header IS believed from a trusted peer, or the feature
// does nothing at all. The previous test alone would pass if realip were
// simply broken.
func TestATrustedPeersHeaderIsBelieved(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	dir := t.TempDir()
	port := freePort(t)

	c := config.Default()
	c.TrustedProxies = config.TrustedProxies{
		Enabled: map[string]bool{},
		// Loopback IS the caller in this test.
		ExtraTrustedCIDRs: []string{"127.0.0.1/32"},
	}

	conf := filepath.Join(dir, "nginx.conf")
	full := fmt.Sprintf(`worker_processes 1;
error_log %s/error.log warn;
pid %s/nginx.pid;
events { worker_connections 32; }
http {
  client_body_temp_path %s/b; proxy_temp_path %s/p;
  fastcgi_temp_path %s/f; uwsgi_temp_path %s/u; scgi_temp_path %s/s;
  access_log off;
%s
  server {
    listen 127.0.0.1:%d;
    location / { return 200 "seen:$remote_addr\n"; }
  }
}
`, dir, dir, dir, dir, dir, dir, dir, FrontProxyPrelude(c), port)

	if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(bin, "-c", conf, "-p", dir).Run(); err != nil {
		t.Skipf("cannot start nginx here: %v", err)
	}
	defer exec.Command(bin, "-c", conf, "-p", dir, "-s", "stop").Run()
	time.Sleep(300 * time.Millisecond)

	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Skipf("could not reach the fixture: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	got := strings.TrimSpace(string(buf[:n]))

	if got != "seen:203.0.113.9" {
		t.Fatalf("a trusted peer's header was ignored: %q — the CDN's real\n"+
			"visitor address would never reach the ban engine", got)
	}
}

// Every generated trust block must load in a real nginx.
func TestEveryBuiltinListGeneratesValidConfig(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	for _, l := range config.BuiltinProxyLists {
		if l.Kind != config.ProxyKindProxy {
			continue
		}
		c := config.Default()
		c.TrustedProxies = config.TrustedProxies{Enabled: map[string]bool{l.ID: true}}

		dir := t.TempDir()
		conf := filepath.Join(dir, "nginx.conf")
		full := fmt.Sprintf(`worker_processes 1;
events { worker_connections 32; }
http {
%s
  server { listen 127.0.0.1:%d; return 204; }
}
`, FrontProxyPrelude(c), freePort(t))
		if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _ := exec.Command(bin, "-t", "-c", conf, "-p", dir,
			"-e", filepath.Join(dir, "err.log")).CombinedOutput()
		if !strings.Contains(string(out), "syntax is ok") {
			t.Errorf("%s produced a config nginx rejects:\n%s\n%s", l.ID, out, full)
		}
	}
}

// A crawler list must NOT be trusted to report addresses. Trusting
// Googlebot's ranges to set $remote_addr would let anything reaching the
// origin from one of them claim to be any address.
func TestCrawlerListsAreNeverTrustedForHeaders(t *testing.T) {
	c := config.Default()
	c.TrustedProxies = config.TrustedProxies{
		Enabled: map[string]bool{config.ProxyGooglebot: true},
	}
	if got := FrontProxyPrelude(c); got != "" {
		t.Fatalf("a crawler list was used as a header trust list:\n%s", got)
	}
	// It must still be in the never-ban list, which is its actual job.
	if len(c.TrustedProxies.NeverBanRanges()) == 0 {
		t.Fatal("the crawler list does not exempt anything from banning")
	}
}

// Validation must refuse a wildcard trust outright — a warning is not
// enough for something that disables every protection at once.
func TestAWildcardTrustIsRefused(t *testing.T) {
	for _, bad := range []string{"0.0.0.0/0", "::/0"} {
		err := config.ValidateTrustedProxies(config.TrustedProxies{
			ExtraTrustedCIDRs: []string{bad},
		})
		if err == nil {
			t.Errorf("trusting %s was accepted", bad)
		}
	}
	// A wildcard in the NEVER-BAN list is merely useless, not dangerous,
	// so it is allowed.
	if err := config.ValidateTrustedProxies(config.TrustedProxies{
		ExtraNeverBanCIDRs: []string{"10.0.0.0/8"},
	}); err != nil {
		t.Errorf("a legitimate never-ban range was refused: %v", err)
	}
}

func TestGarbageRangesAreRefused(t *testing.T) {
	for _, bad := range []string{"not-an-ip", "1.2.3.4/99", "999.1.1.1"} {
		if err := config.ValidateTrustedProxies(config.TrustedProxies{
			ExtraNeverBanCIDRs: []string{bad},
		}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The warning that would have prevented the incident.
func TestProtectionWithoutATrustedProxyWarns(t *testing.T) {
	c := config.Default()
	c.AutoBan.Enabled = true
	got := config.TrustedProxyWarnings(c.TrustedProxies, c)
	found := false
	for _, w := range got {
		if w == "no_proxy_trusted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("banning is on with nothing trusted and no warning was produced: %v", got)
	}

	// With Cloudflare enabled the warning goes away.
	c.TrustedProxies.Enabled = map[string]bool{config.ProxyCloudflare: true}
	for _, w := range config.TrustedProxyWarnings(c.TrustedProxies, c) {
		if w == "no_proxy_trusted" {
			t.Error("the warning persists after a proxy was trusted")
		}
	}
}

// Googlebot's ranges must be the CRAWLER ranges, not Google Cloud —
// otherwise every scanner renting a VM there gets a free pass. This is the
// operator's own observation and it is the right one.
func TestGoogleCloudIsNotInTheCrawlerList(t *testing.T) {
	var google config.ProxyList
	for _, l := range config.BuiltinProxyLists {
		if l.ID == config.ProxyGooglebot {
			google = l
		}
	}
	if len(google.CIDRs) == 0 {
		t.Fatal("no Googlebot list")
	}
	// A few well-known Google Cloud ranges that must NOT appear.
	for _, gcp := range []string{"35.192.0.0/14", "104.154.0.0/15", "35.184.0.0/13"} {
		for _, have := range google.CIDRs {
			if have == gcp {
				t.Errorf("%s is a Google CLOUD range, not a crawler range — "+
					"anyone renting a VM there would be unbannable", gcp)
			}
		}
	}
	// The core crawler range must be there.
	found := false
	for _, have := range google.CIDRs {
		if have == "66.249.64.0/19" {
			found = true
		}
	}
	if !found {
		t.Error("Googlebot's main crawler range is missing")
	}
}
