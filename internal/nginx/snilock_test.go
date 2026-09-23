package nginx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// An SNI rule restricted to a list of addresses.
//
// The operator asked for the same address lock on SNI rules that HTTP
// services already had, and was right that it was missing: RealityService
// had no AllowIPs field at all, so the UI had nothing to show and the
// generator had nothing to enforce.
//
// It cannot reuse the HTTP lock. An SNI rule is resolved in the stream
// module, before any http block exists, so the http-level geo guard never
// sees this traffic.
func TestSNIRuleCanBeLockedToAddresses(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("SHAHRAG_CONFIG", cfgPath)
	mgr := config.New()
	streamOut := filepath.Join(dir, "stream.conf")

	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Nginx.StreamOutputPath = streamOut
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services["locked"] = config.RealityService{
			SNI: "private.example.com", LocalPort: 5001, Ports: []int{443},
			AllowIPs: []string{"203.0.113.7", "10.0.0.0/24"},
		}
		c.Reality.Services["open"] = config.RealityService{
			SNI: "public.example.com", LocalPort: 5002, Ports: []int{443},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	g := NewGenerator(mgr)
	g.NginxConf = filepath.Join(dir, "nginx.conf")
	os.WriteFile(g.NginxConf, []byte("events{}\nhttp{}\n"), 0o644)
	c, _ := mgr.Read()
	if err := g.generateStreamTo(c, streamOut); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(streamOut)
	s := string(conf)

	// The lock exists and lists both entries.
	if !strings.Contains(s, "geo $shg_sni_ok_locked") {
		t.Errorf("no geo block for the locked rule:\n%s", s)
	}
	for _, ip := range []string{"203.0.113.7 1;", "10.0.0.0/24 1;"} {
		if !strings.Contains(s, ip) {
			t.Errorf("allowed address %q is not in the geo block", ip)
		}
	}
	if !strings.Contains(s, "default 0;") {
		t.Error("the geo block does not default to denied")
	}
	// The unlocked rule must be untouched — this feature must not change
	// what an existing config generates.
	// mapKeyForSNI emits a plain hostname unquoted — asserting the quoted
	// form was a mistake in the first version of this test, not a change
	// in the generator.
	if !strings.Contains(s, "public.example.com    127.0.0.1:5002;") {
		t.Errorf("the unrestricted rule was altered:\n%s", s)
	}
	if strings.Contains(s, "$shg_sni_ok_open") {
		t.Error("a rule with no address list got a lock it did not ask for")
	}
	// A refusal must NOT appear: a dropped or refused connection is the
	// fingerprint that gets a server filtered in Iran.
	for _, bad := range []string{"deny ", "return 444", "drop"} {
		if strings.Contains(s, bad) {
			t.Errorf("the lock refuses connections (%q); it must fall through quietly", bad)
		}
	}
}

// The generated file has to be valid nginx, which only nginx can confirm.
func TestSNILockPassesRealNginx(t *testing.T) {
	bin := nginxBin(t)
	if bin == "" {
		t.Skip("nginx is not installed")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("SHAHRAG_CONFIG", cfgPath)
	mgr := config.New()
	streamOut := filepath.Join(dir, "stream.conf")

	mgr.Mutate(func(c *config.Config) error {
		c.Nginx.StreamOutputPath = streamOut
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		// Every shape at once: a plain name, a wildcard, and a rule that
		// is locked as well as being a wildcard.
		c.Reality.Services["plain"] = config.RealityService{
			SNI: "a.example.com", LocalPort: 5001, Ports: []int{443}}
		c.Reality.Services["wild"] = config.RealityService{
			SNI: "*.wild.example", LocalPort: 5002, Ports: []int{443},
			AllowIPs: []string{"198.51.100.0/24"}}
		c.Reality.Services["odd-name.1"] = config.RealityService{
			SNI: "c.example.com", LocalPort: 5003, Ports: []int{8443},
			AllowIPs: []string{"203.0.113.9"}}
		return nil
	})
	g := NewGenerator(mgr)
	g.NginxConf = filepath.Join(dir, "nginx.conf")
	os.WriteFile(g.NginxConf, []byte("events{}\nhttp{}\n"), 0o644)
	c, _ := mgr.Read()
	if err := g.generateStreamTo(c, streamOut); err != nil {
		t.Fatal(err)
	}
	streamBody, _ := os.ReadFile(streamOut)

	// A rule name may contain characters an nginx variable may not; the
	// generator has to sanitise them or nginx refuses the whole file.
	if strings.Contains(string(streamBody), "$shg_sni_ok_odd-name.1") {
		t.Error("a rule name was used verbatim in a variable name")
	}

	// The stream module is a separate shared object on Debian. Without
	// load_module, nginx reports `unknown directive "stream"` — which was a
	// mistake in this fixture, not in the generated config.
	loadStream := ""
	if _, err := os.Stat("/usr/lib/nginx/modules/ngx_stream_module.so"); err == nil {
		loadStream = "load_module /usr/lib/nginx/modules/ngx_stream_module.so;\n"
	}
	main := filepath.Join(dir, "nginx.conf")
	os.WriteFile(main, []byte(
		loadStream+
			"events { worker_connections 64; }\n"+
			"http {}\n"+
			"stream {\n"+string(streamBody)+"\n}\n"), 0o644)
	out, err := exec.Command(bin, "-t", "-c", main, "-p", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("nginx rejected the generated stream config: %v\n%s\n--- config ---\n%s",
			err, out, streamBody)
	}
	t.Logf("nginx -t: %s", strings.TrimSpace(string(out)))
}

func nginxBin(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/sbin/nginx", "/usr/local/sbin/nginx", "/usr/bin/nginx"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	p, _ := exec.LookPath("nginx")
	return p
}
