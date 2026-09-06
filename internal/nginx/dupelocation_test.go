package nginx

// The generator's last line of defence against a duplicate location.
//
// The panel refuses to SAVE two services that share a path, but config.json
// can also be hand-edited, restored from an old backup, or written by a
// version that predates that check. If such a config reached the generator
// unchanged, nginx would reject the entire file — and a rejected file means
// nginx does not start, so every site on the server goes down, not only the
// two that clash.
//
// These tests build exactly that config, bypassing the manager's validation
// the same way a hand edit would, and prove the generated file is still
// accepted by a REAL nginx.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// clashFixture writes a config containing two services that share a path.
func clashFixture(t *testing.T, path1, path2 string, port1, port2 int) (*Generator, string, string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")

	crt := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "c.key")
	if out, err := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048",
		"-nodes", "-keyout", key, "-out", crt, "-days", "2",
		"-subj", "/CN=example.test").CombinedOutput(); err != nil {
		t.Skipf("openssl unavailable: %v %s", err, out)
	}

	mgr := config.New()
	if err := mgr.AddDomain("example.test", crt, key); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "gateway.conf")
	// Written directly through Mutate: this is what a hand-edited file or an
	// old backup looks like, and the point is that the generator must cope.
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Services = map[string]config.Service{
			"first": {LocalPort: 3000, ListenPort: port1, Path: path1, PathOwned: true,
				Bindings: []config.Binding{{Domain: "example.test", Subdomain: "www"}}},
			"second": {LocalPort: 3001, ListenPort: port2, Path: path2, PathOwned: true,
				Bindings: []config.Binding{{Domain: "example.test", Subdomain: "www"}}},
		}
		c.ListenPorts = []int{port1, port2}
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
	return g, out, dir
}

// realNginxAccepts runs a genuine syntax check over the generated file.
func realNginxAccepts(t *testing.T, dir, httpOut string) (bool, string) {
	t.Helper()
	bin := nginxBinary()
	if bin == "" {
		t.Skip("nginx is not installed")
	}
	root := filepath.Join(dir, "root")
	_ = os.MkdirAll(filepath.Join(root, "logs"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "tmp"), 0o755)
	conf := filepath.Join(dir, "check.conf")
	body := "pid " + filepath.Join(root, "n.pid") + ";\n" +
		"error_log " + filepath.Join(root, "logs", "e.log") + ";\n" +
		"events { worker_connections 64; }\n" +
		"http {\n" +
		"    client_body_temp_path " + filepath.Join(root, "tmp") + ";\n" +
		"    proxy_temp_path " + filepath.Join(root, "tmp") + "/p;\n" +
		"    fastcgi_temp_path " + filepath.Join(root, "tmp") + "/f;\n" +
		"    uwsgi_temp_path " + filepath.Join(root, "tmp") + "/u;\n" +
		"    scgi_temp_path " + filepath.Join(root, "tmp") + "/s;\n" +
		"    access_log " + filepath.Join(root, "logs", "a.log") + ";\n" +
		"    include " + httpOut + ";\n}\n"
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-c", conf, "-p", root).CombinedOutput()
	txt := string(out)
	// An unprivileged test cannot bind port 80; that says nothing about the
	// configuration itself.
	if err != nil && strings.Contains(txt, "syntax is ok") &&
		strings.Contains(txt, "Permission denied") {
		return true, txt
	}
	return err == nil, txt
}

// The bug, end to end: this exact config used to make nginx refuse to start.
func TestGeneratorNeverEmitsADuplicateLocation(t *testing.T) {
	g, out, dir := clashFixture(t, "/", "/", 8443, 8443)
	if _, err := g.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)

	if n := strings.Count(s, "location / {"); n != 1 {
		t.Errorf("emitted %d root locations in one server block, want 1", n)
	}
	// The skip has to be explained, or the next person to read the file
	// will think the service was lost.
	if !strings.Contains(s, "SKIPPED") {
		t.Error("the skipped service is not explained in the generated file")
	}
	// The first service alphabetically wins and must still be served.
	if !strings.Contains(s, "127.0.0.1:3000") {
		t.Error("the surviving service's upstream is missing")
	}

	ok, msg := realNginxAccepts(t, dir, out)
	if !ok {
		t.Errorf("real nginx REJECTED the generated config:\n%s", msg)
	}
	if strings.Contains(msg, "duplicate location") {
		t.Errorf("nginx still reports a duplicate location:\n%s", msg)
	}
	t.Log("real nginx accepted a config built from a clashing config.json")
}

// The same for a named path, and with the two services in an order where
// the map iteration might otherwise pick differently.
func TestGeneratorDeduplicatesNamedPaths(t *testing.T) {
	g, out, dir := clashFixture(t, "api", "api", 8443, 8443)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustRead(t, out))
	if n := strings.Count(s, "location /api"); n != 1 {
		t.Errorf("emitted %d /api locations, want 1", n)
	}
	ok, msg := realNginxAccepts(t, dir, out)
	if !ok {
		t.Errorf("real nginx rejected the config:\n%s", msg)
	}
}

// Paths that only LOOK the same must not be collapsed: "/app" and "/app/"
// are different locations to nginx and both are legitimate.
func TestGeneratorKeepsGenuinelyDifferentPaths(t *testing.T) {
	g, out, dir := clashFixture(t, "app", "app/", 8443, 8443)
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	s := string(mustRead(t, out))
	if !strings.Contains(s, "location /app {") {
		t.Error("/app was dropped")
	}
	if !strings.Contains(s, "location /app/") {
		t.Error("/app/ was dropped — it is a different location from /app")
	}
	if strings.Contains(s, "SKIPPED") {
		t.Error("two legitimately different paths were treated as a clash")
	}
	if ok, msg := realNginxAccepts(t, dir, out); !ok {
		t.Errorf("real nginx rejected the config:\n%s", msg)
	}
}

// Deduplication must be deterministic: the same input must always drop the
// same service, or two runs of Generate produce different files.
func TestDeduplicationIsStableAcrossRuns(t *testing.T) {
	g, out, _ := clashFixture(t, "/", "/", 8443, 8443)
	var first string
	for i := 0; i < 6; i++ {
		if _, err := g.Generate(); err != nil {
			t.Fatal(err)
		}
		got := stripTimestamp(string(mustRead(t, out)))
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatal("two runs of Generate produced different files")
		}
	}
	if !strings.Contains(first, "127.0.0.1:3000") {
		t.Error(`the surviving service should be "first" (alphabetically)`)
	}
}
