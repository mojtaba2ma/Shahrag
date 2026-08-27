package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shahrag/internal/config"
)

// buildFixture generates a realistic config: one service bound to two
// domains (so it produces two location blocks), one single-domain service,
// and two SNI rules.
func buildFixture(t *testing.T) (gateway, stream string) {
	t.Helper()
	dir := t.TempDir()
	config.ConfigPath = filepath.Join(dir, "config.json")
	config.LockPath = filepath.Join(dir, "config.lock")
	mgr := config.New()
	if _, err := mgr.Mutate(func(c *config.Config) error {
		c.Domains["a.example.com"] = config.Domain{Cert: "/c1.pem", Key: "/k1.pem"}
		c.Domains["b.example.com"] = config.Domain{Cert: "/c2.pem", Key: "/k2.pem"}
		c.ListenPorts = []int{80, 443}
		c.Services["xray"] = config.Service{
			LocalPort: 4628, ListenPort: 443, Path: "take", PathOwned: true,
			Bindings: []config.Binding{
				{Domain: "a.example.com", Subdomain: "one"},
				{Domain: "b.example.com", Subdomain: "two"},
			},
		}
		c.Services["adguard"] = config.Service{
			LocalPort: 3000, ListenPort: 443, Path: "dash", PathOwned: false,
			Bindings: []config.Binding{{Domain: "a.example.com", Subdomain: "dash"}},
		}
		c.Reality.Enabled = true
		c.Reality.HTTPPort = 6038
		c.Reality.Services["chess"] = config.RealityService{
			SNI: "chess.com", LocalPort: 49026, Ports: []int{2053},
		}
		c.Reality.Services["unblock"] = config.RealityService{
			SNI: "*.epicgames.com", LocalPort: 443, Ports: []int{443},
			Target: config.PassthroughTarget,
		}
		c.Nginx.OutputPath = filepath.Join(dir, "gateway.conf")
		c.Nginx.StreamOutputPath = filepath.Join(dir, "stream.conf")
		c.Nginx.FakeDir = filepath.Join(dir, "fake")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(mgr)
	g.NginxConf = filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(g.NginxConf, []byte("# fixture\nhttp {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := g.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	gw, _ := os.ReadFile(res.HTTPPath)
	st, _ := os.ReadFile(res.StreamPath)
	return string(gw), string(st)
}

// A service's extracted nginx must contain ITS blocks and nothing else — the
// whole point is not having to find them in a 400-line file.
func TestExtractServiceNginx(t *testing.T) {
	gw, _ := buildFixture(t)

	got, n := ExtractServiceNginx(gw, "xray")
	if n != 2 {
		t.Fatalf("xray is bound to two domains, so it has 2 blocks, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "location /take {") {
		t.Errorf("the location block is missing:\n%s", got)
	}
	if strings.Contains(got, "location /dash") || strings.Contains(got, "3000") {
		t.Errorf("another service leaked into the extract:\n%s", got)
	}
	if strings.Contains(got, "ssl_certificate") {
		t.Errorf("server-level directives must not be included:\n%s", got)
	}
	// Both server_names must be identifiable, otherwise the two blocks
	// cannot be told apart when editing.
	if !strings.Contains(got, "one.a.example.com") || !strings.Contains(got, "two.b.example.com") {
		t.Errorf("each block must name the server it belongs to:\n%s", got)
	}

	// A single-binding service produces exactly one block.
	got1, n1 := ExtractServiceNginx(gw, "adguard")
	if n1 != 1 {
		t.Errorf("adguard should have 1 block, got %d", n1)
	}
	if !strings.Contains(got1, "location /dash/ {") {
		t.Errorf("the path-strip location is missing:\n%s", got1)
	}

	// An unknown service yields nothing rather than a wrong guess.
	if _, n2 := ExtractServiceNginx(gw, "nope"); n2 != 0 {
		t.Error("an unknown service must yield no blocks")
	}
}

// Editing must splice back byte-exactly: an unchanged round trip has to
// reproduce the original file, or every save would drift the config.
func TestReplaceServiceNginxRoundTrip(t *testing.T) {
	gw, _ := buildFixture(t)

	for _, name := range []string{"xray", "adguard"} {
		extracted, _ := ExtractServiceNginx(gw, name)
		out, err := ReplaceServiceNginx(gw, name, extracted)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != gw {
			t.Errorf("%s: an unchanged round trip must reproduce the file exactly", name)
		}
	}
}

func TestReplaceServiceNginxAppliesEdits(t *testing.T) {
	gw, _ := buildFixture(t)
	extracted, _ := ExtractServiceNginx(gw, "xray")

	edited := strings.ReplaceAll(extracted, "127.0.0.1:4628", "127.0.0.1:9999")
	out, err := ReplaceServiceNginx(gw, "xray", edited)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "127.0.0.1:4628") {
		t.Error("the edit was not applied to every block")
	}
	if strings.Count(out, "127.0.0.1:9999") != strings.Count(extracted, "127.0.0.1:4628") {
		t.Error("not all occurrences were replaced")
	}
	// Everything outside the service must be untouched.
	if !strings.Contains(out, "location /dash/ {") || !strings.Contains(out, "3000") {
		t.Error("an unrelated service was damaged by the splice")
	}
}

// Dropping a separator would make it impossible to know which server block
// each piece belongs to, so it must be refused, not guessed.
func TestReplaceServiceNginxRefusesWrongBlockCount(t *testing.T) {
	gw, _ := buildFixture(t)
	extracted, _ := ExtractServiceNginx(gw, "xray")

	// Keep only the first block.
	idx := strings.Index(extracted[1:], "# ── block")
	if idx < 0 {
		t.Fatal("fixture should contain two separators")
	}
	truncated := extracted[:idx+1]

	if _, err := ReplaceServiceNginx(gw, "xray", truncated); err == nil {
		t.Error("a missing block must be refused")
	}
	if _, err := ReplaceServiceNginx(gw, "xray", "location /take { }"); err == nil {
		t.Error("text without separators must be refused")
	}
	if _, err := ReplaceServiceNginx(gw, "unknown", extracted); err == nil {
		t.Error("an unknown service must be refused")
	}
}

// SNI rules live in the stream map; the same guarantees apply.
func TestExtractAndReplaceSNI(t *testing.T) {
	_, st := buildFixture(t)

	got, ok := ExtractSNINginx(st, "unblock")
	if !ok {
		t.Fatalf("the SNI rule was not found:\n%s", st)
	}
	if !strings.Contains(got, "epicgames") || !strings.Contains(got, "$ssl_preread_server_name") {
		t.Errorf("the extracted entry is wrong:\n%s", got)
	}
	if strings.Contains(got, "chess.com") {
		t.Errorf("another rule leaked in:\n%s", got)
	}

	// Round trip.
	out, err := ReplaceSNINginx(st, "unblock", got)
	if err != nil {
		t.Fatal(err)
	}
	if out != st {
		t.Error("an unchanged SNI round trip must reproduce the file exactly")
	}

	// A real edit.
	edited := strings.ReplaceAll(got, ":443;", ":8443;")
	out2, err := ReplaceSNINginx(st, "unblock", edited)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, ":8443;") {
		t.Error("the edit was not applied")
	}
	if !strings.Contains(out2, "chess.com    127.0.0.1:49026;") {
		t.Error("the other rule was damaged")
	}

	if _, err := ReplaceSNINginx(st, "missing", got); err == nil {
		t.Error("an unknown rule must be refused")
	}
}
