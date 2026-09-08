package nginx

// The tuning directives, checked against a REAL nginx.
//
// Every value here ends up in a file nginx has to parse, and a single
// misplaced directive means the server will not start. Asserting the
// generated text with string comparisons would prove only that the
// generator does what the generator does. So these tests write real files
// and run the real `nginx -t` binary over them.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// tunedConfig returns a config with every tunable set to its recommendation
// for a small box — the values the panel will actually produce.
func tunedConfig(t *testing.T) *config.Config {
	t.Helper()
	c := config.Default()
	si := config.SysInfo{Cores: 2, RAMBytes: 1003 << 20,
		AvailableBytes: 420 << 20, NofileHard: 524288}
	tn := config.ApplyRecommendations(config.Tuning{}, config.Recommend(si, config.ProfileSmall))
	tn.Enabled = true
	c.NginxSettings.Tuning = tn
	c.NginxSettings.WorkerConnections = 1024
	return c
}

// A disabled tuning must emit absolutely nothing. This is what makes the
// feature safe to ship to an existing installation: the generated file is
// byte-identical to what it was before.
func TestDisabledTuningEmitsNothing(t *testing.T) {
	c := config.Default()
	for _, got := range []string{
		TuningHTTPBlock(c), TuningMainBlock(c),
		TuningEventsBlock(c), TuningUpstreamKeepalive(c),
	} {
		if got != "" {
			t.Errorf("a disabled tuning emitted:\n%s", got)
		}
	}
}

// An ENABLED but empty tuning must also emit nothing but the header — every
// field zero means "leave nginx's defaults alone".
func TestAnEmptyEnabledTuningEmitsNoDirectives(t *testing.T) {
	c := config.Default()
	c.NginxSettings.Tuning = config.Tuning{Enabled: true}

	got := TuningHTTPBlock(c)
	for _, line := range strings.Split(got, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		t.Errorf("an all-zero tuning emitted the directive: %s", line)
	}
	if TuningMainBlock(c) != "" {
		t.Errorf("main block emitted:\n%s", TuningMainBlock(c))
	}
}

// The real test: nginx must accept the generated http block.
func TestGeneratedHTTPBlockPassesNginxT(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	c := tunedConfig(t)
	body := TuningHTTPBlock(c)
	if strings.TrimSpace(body) == "" {
		t.Fatal("nothing was generated to test")
	}

	dir := t.TempDir()
	conf := filepath.Join(dir, "nginx.conf")
	full := "events { worker_connections 64; }\n" +
		"http {\n" + indent(body) + "\n" +
		"  server { listen 127.0.0.1:18099; return 204; }\n}\n"
	if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(bin, "-t", "-c", conf,
		"-p", dir, "-e", filepath.Join(dir, "error.log")).CombinedOutput()
	text := string(out)
	// Unprivileged runs cannot bind or write the system log; those are
	// environment failures, not config errors. "syntax is ok" is the
	// assertion that matters.
	if !strings.Contains(text, "syntax is ok") {
		t.Fatalf("nginx rejected the generated tuning:\n%s\n--- config ---\n%s",
			text, full)
	}
	t.Logf("nginx accepted %d directives", countDirectives(body))
	_ = err
}

// And the main-context block, which is the one that would stop nginx
// starting at all if a directive were in the wrong context.
func TestGeneratedMainBlockPassesNginxT(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	c := tunedConfig(t)
	main := TuningMainBlock(c)
	events := TuningEventsBlock(c)
	if strings.TrimSpace(main) == "" {
		t.Fatal("nothing was generated to test")
	}

	dir := t.TempDir()
	conf := filepath.Join(dir, "nginx.conf")
	full := main +
		"events {\n  worker_connections 64;\n" + events + "}\n" +
		"http { server { listen 127.0.0.1:18098; return 204; } }\n"
	if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _ := exec.Command(bin, "-t", "-c", conf,
		"-p", dir, "-e", filepath.Join(dir, "error.log")).CombinedOutput()
	if !strings.Contains(string(out), "syntax is ok") {
		t.Fatalf("nginx rejected the main-context tuning:\n%s\n--- config ---\n%s",
			out, full)
	}
}

// Each directive individually, so a failure names the culprit instead of
// making someone bisect thirty lines by hand.
func TestEachDirectiveIsAcceptedIndividually(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	c := tunedConfig(t)

	for _, line := range strings.Split(TuningHTTPBlock(c), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dir := t.TempDir()
		conf := filepath.Join(dir, "nginx.conf")
		full := "events { worker_connections 64; }\nhttp {\n  " + line +
			"\n  server { listen 127.0.0.1:18097; return 204; }\n}\n"
		if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _ := exec.Command(bin, "-t", "-c", conf,
			"-p", dir, "-e", filepath.Join(dir, "error.log")).CombinedOutput()
		if !strings.Contains(string(out), "syntax is ok") {
			t.Errorf("nginx rejected: %s\n%s", line, out)
		}
	}
}

// The specific fixes for the user's real errors must actually appear.
func TestTheRealWorldFixesAreEmitted(t *testing.T) {
	c := tunedConfig(t)
	got := TuningHTTPBlock(c)

	// "recv() failed (104) while proxying upgraded connection" — nginx's
	// 60s default killing an idle tunnel.
	if !strings.Contains(got, "proxy_read_timeout 3600s;") {
		t.Error("no long proxy_read_timeout: idle WebSocket tunnels would still be killed")
	}
	// "SSL routines::bad key share" — the client offered a group the
	// server was not configured to accept.
	if !strings.Contains(got, "ssl_ecdh_curve X25519:") {
		t.Error("no explicit ssl_ecdh_curve: 'bad key share' would still occur")
	}
	// The upstream pool, which stops every proxied request opening a new
	// TCP connection to the backend.
	if TuningUpstreamKeepalive(c) == "" {
		t.Error("no upstream keepalive pool")
	}
}

// limit_conn must answer 429, never a refusal that makes the server look
// like it filters.
func TestConnectionLimitAnswersQuietly(t *testing.T) {
	c := tunedConfig(t)
	got := TuningHTTPBlock(c)
	if !strings.Contains(got, "limit_conn_status 429;") {
		t.Error("the connection limit does not set a quiet status")
	}
	if strings.Contains(got, "limit_conn_status 403") {
		t.Error("the connection limit answers 403, which makes the server conspicuous")
	}
}

// The access log warning must be in the generated file, not only in the UI.
// Somebody reading the config six months later needs to know why their
// statistics are empty.
func TestSwitchingTheAccessLogOffIsDocumentedInTheConfig(t *testing.T) {
	c := tunedConfig(t)
	c.NginxSettings.Tuning.AccessLogOff = true
	got := TuningHTTPBlock(c)
	if !strings.Contains(got, "access_log off;") {
		t.Fatal("the directive was not emitted")
	}
	if !strings.Contains(got, "WARNING") {
		t.Error("the generated config does not explain that statistics are now blind")
	}
}

// Injection: the curve list is the only free-text field that reaches the
// config, and config.ValidateTuning refuses anything dangerous. Confirm the
// generator would not paper over a validation gap.
func TestCurveListIsTheOnlyFreeTextAndItIsValidated(t *testing.T) {
	bad := config.Tuning{Enabled: true, SSLECDHCurve: "X25519; return 200 'pwned'"}
	if err := config.ValidateTuning(bad); err == nil {
		t.Fatal("an injected curve list was accepted by validation")
	}
}

func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func countDirectives(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}
