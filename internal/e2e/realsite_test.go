package e2e

// A real nginx serving a real rendered site.
//
// Everything else about this feature can be unit tested, but three questions
// can only be answered by an nginx process:
//
//  1. does the generated configuration actually parse (`nginx -t`);
//  2. does a request for a missing page return the panel's 404 rather than
//     nginx's grey one, with status 404 and not 200;
//  3. is /__shg_err/404.html really unreachable from outside.
//
// Number 2 is the one that matters most: an error_page pointing at a file
// that is not there does not produce a 404, it produces a 500, and no amount
// of string comparison in a unit test can catch that.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/nginx"
	"shahrag/internal/realsite"
	"shahrag/internal/templates"
)

// TestRealSiteServedByRealNginx renders a site, generates the location
// blocks, drops them into a minimal nginx, and asks real questions over TCP.
func TestRealSiteServedByRealNginx(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to run nginx")
	}
	bin := nginxBinary()
	if bin == "" {
		t.Skip("nginx is not installed")
	}
	dir := t.TempDir()
	// nginx's worker must be able to traverse the tree; t.TempDir() is
	// 0700 and owned by root.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// ── render a site ──
	sitesDir := filepath.Join(dir, "sites")
	r := &realsite.Renderer{
		Dir:    sitesDir,
		Client: templates.NewClient(filepath.Join(dir, "tplcache")),
	}
	site := config.RealSite{
		Enabled:  true,
		Template: "corporate-slate",
		Content: config.RealSiteContent{
			SiteName: "Northwind Trading", Language: "en",
		},
	}
	res, err := r.Render("nw.example", site)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	t.Logf("rendered %d files / %d bytes to %s", res.Files, res.Bytes, res.Root)
	// Permit traversal for www-data all the way down.
	filepath.WalkDir(sitesDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	os.Chmod(sitesDir, 0o755)

	plan := &nginx.RealSitePlan{Root: res.Root, ErrorPages: res.ErrorPages}
	locations := nginx.RealSiteLocations(plan)
	if locations == "" {
		t.Fatal("no locations were generated")
	}

	// ── minimal nginx around them ──
	port := freePort(t)
	conf := filepath.Join(dir, "nginx.conf")
	// `user root;` MUST be the first directive: after `pid` it is accepted
	// but the workers have already dropped privileges and cannot traverse
	// a root-owned temporary directory.
	body := fmt.Sprintf(`user root;
worker_processes 1;
pid %s/nginx.pid;
error_log %s/error.log warn;
events { worker_connections 64; }
http {
    access_log %s/access.log;
    client_body_temp_path %s/cbt;
    proxy_temp_path %s/pt;
    fastcgi_temp_path %s/ft;
    uwsgi_temp_path %s/ut;
    scgi_temp_path %s/st;
    types { text/html html; text/css css; image/svg+xml svg; text/plain txt; application/xml xml; }
    default_type application/octet-stream;
    server {
        listen 127.0.0.1:%d;
        server_name nw.example;
%s
    }
}
`, dir, dir, dir, dir, dir, dir, dir, dir, port, locations)
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// ── question 1: does it parse ──
	out, err := exec.Command(bin, "-t", "-c", conf, "-p", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("nginx -t rejected the generated config: %v\n%s\n--- config ---\n%s", err, out, body)
	}
	t.Logf("nginx -t: %s", strings.TrimSpace(string(out)))

	cmd := exec.Command(bin, "-c", conf, "-p", dir, "-g", "daemon off;")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitReady(t, base+"/")

	client := &http.Client{Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, string(b)
	}

	// ── the home page really is the site ──
	code, page := get("/")
	if code != 200 {
		t.Errorf("GET / = %d, want 200", code)
	}
	if !strings.Contains(page, "Northwind Trading") {
		t.Errorf("the home page is not the rendered site:\n%.400s", page)
	}
	if strings.Contains(page, "{{") {
		t.Error("an unsubstituted placeholder is visible to a visitor")
	}

	// ── other pages and assets ──
	for _, p := range []string{"/about.html", "/services.html", "/contact.html",
		"/assets/site.css", "/robots.txt", "/sitemap.xml"} {
		if c, _ := get(p); c != 200 {
			t.Errorf("GET %s = %d, want 200", p, c)
		}
	}

	// ── question 2: the 404 is OURS, and is a 404 ──
	code, page = get("/no-such-page-here")
	if code != 404 {
		t.Errorf("GET a missing page = %d, want 404", code)
	}
	// The real failure mode, measured on nginx 1.26.3: when the error page
	// is missing or unreadable nginx keeps the 404 but falls back to its
	// own page, which ends in "<hr><center>nginx/1.26.3</center>". The
	// status alone would not catch that, so the BODY is what is asserted.
	if strings.Contains(page, "<hr><center>nginx") {
		t.Error("nginx's own 404 was served, advertising the server version — the whole point is that it is not")
	}
	if !strings.Contains(page, "404") || !strings.Contains(page, "Northwind Trading") {
		t.Errorf("the 404 is not the branded page:\n%.400s", page)
	}

	// ── question 3: the error pages are not reachable directly ──
	if c, _ := get("/__shg_err/errors/404.html"); c != 404 {
		t.Errorf("the internal error location answered %d — it must be unreachable", c)
	}

	// ── nginx's version must not be advertised on our page ──
	if strings.Contains(page, "nginx/") {
		t.Error("the error page leaks the nginx version")
	}
}

// The same, for a domain whose real site is OFF: the generated config must be
// byte-identical to one produced without the feature.
func TestDisabledRealSiteChangesNothing(t *testing.T) {
	c := config.Default()
	c.Domains["a.example"] = config.Domain{Cert: "/tmp/c", Key: "/tmp/k"}
	if c.AnyRealSite() {
		t.Fatal("nothing is enabled")
	}
	dir := t.TempDir()
	r := &realsite.Renderer{Dir: filepath.Join(dir, "sites"),
		Client: templates.NewClient(filepath.Join(dir, "cache"))}
	plans, err := realsite.Plan(r, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Errorf("a disabled feature produced %d plans", len(plans))
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "sites")); err == nil && len(entries) > 0 {
		t.Errorf("a disabled feature wrote %d entries to disk", len(entries))
	}
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("nginx never became ready")
}
