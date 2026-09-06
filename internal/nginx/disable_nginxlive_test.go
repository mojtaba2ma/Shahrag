package nginx

// The generated file must be accepted by a REAL nginx in every state a
// service can be in.
//
// This is the test that justifies not commenting blocks out. A commenting
// implementation can produce a file that looks right in a string comparison
// and is still rejected by nginx — or, worse, accepted while meaning
// something different. Only a real `nginx -t` settles it.
//
// Skipped when nginx is not installed, so the suite still runs anywhere.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// loadStreamModule returns a load_module line when nginx ships stream as a
// dynamic module (Debian/Ubuntu do), so the stream config can be checked
// for real instead of skipped.
func loadStreamModule() string {
	for _, p := range []string{
		"/usr/lib/nginx/modules/ngx_stream_module.so",
		"/usr/lib64/nginx/modules/ngx_stream_module.so",
	} {
		if _, err := os.Stat(p); err == nil {
			return "load_module " + p + ";\n"
		}
	}
	return ""
}

func nginxBinary() string {
	for _, p := range []string{"nginx", "/usr/sbin/nginx", "/usr/local/sbin/nginx"} {
		if full, err := exec.LookPath(p); err == nil {
			return full
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// selfSignedPair writes a throwaway certificate so the server blocks nginx
// parses are complete ones.
func selfSignedPair(t *testing.T, dir string) (string, string) {
	t.Helper()
	crt := filepath.Join(dir, "t.pem")
	key := filepath.Join(dir, "t.key")
	cmd := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
		"-keyout", key, "-out", crt, "-days", "2", "-subj", "/CN=example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("openssl unavailable: %v %s", err, out)
	}
	return crt, key
}

// checkWithNginx wraps the generated file in a minimal nginx.conf and runs
// a real syntax check over it.
func checkWithNginx(t *testing.T, bin, dir, httpOut, streamOut string) (bool, string) {
	t.Helper()
	root := filepath.Join(dir, "root")
	_ = os.MkdirAll(filepath.Join(root, "logs"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "tmp"), 0o755)

	conf := filepath.Join(dir, "check.conf")
	body := "" +
		"pid " + filepath.Join(root, "n.pid") + ";\n" +
		"error_log " + filepath.Join(root, "logs", "error.log") + ";\n" +
		"events { worker_connections 64; }\n" +
		"http {\n" +
		"    client_body_temp_path " + filepath.Join(root, "tmp") + ";\n" +
		"    proxy_temp_path " + filepath.Join(root, "tmp") + "/p;\n" +
		"    fastcgi_temp_path " + filepath.Join(root, "tmp") + "/f;\n" +
		"    uwsgi_temp_path " + filepath.Join(root, "tmp") + "/u;\n" +
		"    scgi_temp_path " + filepath.Join(root, "tmp") + "/s;\n" +
		"    access_log " + filepath.Join(root, "logs", "access.log") + ";\n" +
		"    include " + httpOut + ";\n" +
		"}\n"
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-c", conf, "-p", root).CombinedOutput()
	txt := string(out)
	// `nginx -t` both PARSES the config and tries to bind its listen
	// sockets. Running as an ordinary user, binding port 80 fails with
	// EACCES — which says nothing about the configuration. The syntax
	// verdict is the line that matters here, so accept it and treat a
	// bind failure as the environment's problem, not the file's.
	if err != nil && strings.Contains(txt, "syntax is ok") &&
		strings.Contains(txt, "Permission denied") {
		return true, txt
	}
	return err == nil, txt
}

// The whole point: valid before, valid after, valid again.
func TestRealNginxAcceptsEveryToggleState(t *testing.T) {
	bin := nginxBinary()
	if bin == "" {
		t.Skip("nginx is not installed")
	}

	g, mgr, httpOut, streamOut := genFixture(t)
	dir := filepath.Dir(httpOut)
	crt, key := selfSignedPair(t, dir)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Domains["example.com"] = config.Domain{Cert: crt, Key: key}
		// A gate on one service, so the disabled path also has to remove a
		// map block and an internal location correctly.
		svc := c.Services["alpha"]
		svc.Gate = config.GateSecret
		svc.GateSecret = "TestKey_abcdef123456"
		c.Services["alpha"] = svc
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	states := []struct {
		name  string
		alpha bool // disabled?
		beta  bool
	}{
		{"both enabled", false, false},
		{"alpha disabled", true, false},
		{"both disabled", true, true},
		{"beta disabled only", false, true},
		{"both enabled again", false, false},
	}

	for _, st := range states {
		setDisabled(t, mgr, "alpha", st.alpha)
		setDisabled(t, mgr, "beta", st.beta)
		if _, err := g.Generate(); err != nil {
			t.Fatalf("[%s] generate: %v", st.name, err)
		}
		ok, out := checkWithNginx(t, bin, dir, httpOut, streamOut)
		if !ok {
			t.Errorf("[%s] real nginx REJECTED the generated config:\n%s", st.name, out)
			continue
		}
		t.Logf("[%-20s] nginx -t OK", st.name)

		// And the routing must match the state, not merely parse.
		body, _ := os.ReadFile(httpOut)
		s := string(body)
		if got := strings.Contains(s, "location /alpha"); got == st.alpha {
			t.Errorf("[%s] alpha present=%v but disabled=%v", st.name, got, st.alpha)
		}
		if got := strings.Contains(s, "location /beta"); got == st.beta {
			t.Errorf("[%s] beta present=%v but disabled=%v", st.name, got, st.beta)
		}
	}
}

// A disabled service's port must not be listened on by the stream config,
// and the stream file must still be valid nginx.
func TestRealNginxAcceptsDisabledSNIRule(t *testing.T) {
	bin := nginxBinary()
	if bin == "" {
		t.Skip("nginx is not installed")
	}
	g, mgr, httpOut, streamOut := genFixture(t)
	dir := filepath.Dir(httpOut)
	crt, key := selfSignedPair(t, dir)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Domains["example.com"] = config.Domain{Cert: crt, Key: key}
		r := c.Reality.Services["sni1"]
		r.Disabled = true
		c.Reality.Services["sni1"] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate(); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(dir, "sroot")
	_ = os.MkdirAll(filepath.Join(root, "logs"), 0o755)
	conf := filepath.Join(dir, "stream-check.conf")
	body := loadStreamModule() +
		"pid " + filepath.Join(root, "n.pid") + ";\n" +
		"error_log " + filepath.Join(root, "logs", "error.log") + ";\n" +
		"events { worker_connections 64; }\n" +
		"stream {\n    include " + streamOut + ";\n}\n"
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-c", conf, "-p", root).CombinedOutput()
	txt := string(out)
	if err != nil {
		// The stream module may be absent; that is not a product failure.
		if strings.Contains(txt, "unknown directive") {
			t.Skipf("nginx has no stream module: %s", out)
		}
		// The generated file logs to /var/log/nginx, which an unprivileged
		// test cannot open. That is the sandbox, not the configuration.
		if strings.Contains(txt, "syntax is ok") && strings.Contains(txt, "Permission denied") {
			t.Logf("syntax accepted; log path not writable in the test sandbox")
		} else {
			t.Fatalf("real nginx rejected the stream config:\n%s", out)
		}
	}
	if !strings.Contains(txt, "syntax is ok") {
		t.Fatalf("nginx did not report the stream config as valid:\n%s", out)
	}
	t.Log("stream config accepted by real nginx with a disabled rule")
}
