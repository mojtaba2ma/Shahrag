package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `nginx -t` only PARSES the config; it never binds a socket. A valid test
// plus an inactive service therefore usually means another daemon owns a
// port. PortsRequiredByNginx must extract every port nginx will try to bind,
// in all the spellings the generator emits.
func TestPortsRequiredByNginx(t *testing.T) {
	dir := t.TempDir()
	gw := filepath.Join(dir, "gateway.conf")
	st := filepath.Join(dir, "stream.conf")
	if err := os.WriteFile(gw, []byte(`
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
}
server {
    listen 6038 ssl http2 default_server;
    listen [::]:6038 ssl http2 default_server;
    server_name example.com *.example.com;
}
server {
    listen 127.0.0.1:8081;
    server_name _;
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st, []byte(`
server { listen 443; listen [::]:443; proxy_pass $reality_backend; ssl_preread on; }
server { listen 2053; listen [::]:2053; proxy_pass $reality_backend; ssl_preread on; }
server { listen 8443; listen [::]:8443; proxy_pass $reality_backend; ssl_preread on; }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := PortsRequiredByNginx(gw, st)
	for _, want := range []int{80, 443, 2053, 6038, 8443, 8081} {
		if _, ok := got[want]; !ok {
			t.Errorf("port %d not detected; got %v", want, keysOf(got))
		}
	}
	// The source file must be attributed so the report can name it.
	if srcs := got[2053]; len(srcs) == 0 || !strings.Contains(srcs[0], "stream") {
		t.Errorf("port 2053 should be attributed to the stream file, got %v", srcs)
	}
}

func keysOf(m map[int][]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Duplicate server names are what nginx reports as
// "conflicting server name ... ignored" — it keeps the FIRST block and drops
// the rest, silently taking those services offline. Shahrag's own output must
// never contain duplicates, so any hit points at a leftover foreign file.
func TestDuplicateServerNamesDetection(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "gateway.conf")
	leftover := filepath.Join(dir, "old-panel.conf")

	if err := os.WriteFile(mine, []byte(`
server {
    listen 6038 ssl http2 default_server;
    server_name example.com *.example.com;
}
server {
    listen 6038 ssl http2;
    server_name a.other.com b.other.com;
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// No duplicates within Shahrag's own file.
	if d := DuplicateServerNames(mine); len(d) != 0 {
		t.Errorf("the generated file must never collide with itself: %v", d)
	}

	if err := os.WriteFile(leftover, []byte(`
server {
    listen 6038 ssl http2;
    server_name example.com;
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := DuplicateServerNames(mine, leftover)
	if len(d) == 0 {
		t.Fatal("a leftover file claiming the same name must be detected")
	}
	key := "example.com on port 6038"
	files, ok := d[key]
	if !ok {
		t.Fatalf("expected key %q, got %v", key, d)
	}
	joined := strings.Join(files, " ")
	if !strings.Contains(joined, "gateway.conf") || !strings.Contains(joined, "old-panel.conf") {
		t.Errorf("both files must be named so the operator knows what to delete: %v", files)
	}

	// A name on a DIFFERENT port is not a conflict.
	other := filepath.Join(dir, "other-port.conf")
	if err := os.WriteFile(other, []byte("server {\n listen 8443 ssl;\n server_name example.com;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, bad := DuplicateServerNames(mine, other)["example.com on port 8443"]; bad {
		t.Error("the same name on a different port must not be reported as a conflict")
	}
}

// splitServerBlocks must handle nested braces (location blocks, if blocks).
func TestSplitServerBlocksHandlesNesting(t *testing.T) {
	txt := `
server {
    listen 443 ssl;
    server_name a.example.com;
    location /x {
        if ($host != "a.example.com") { return 302 /; }
        proxy_pass http://127.0.0.1:1234;
    }
}
server {
    listen 443 ssl;
    server_name b.example.com;
}
`
	blocks := splitServerBlocks(txt)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 server blocks, got %d: %q", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0], "a.example.com") || strings.Contains(blocks[0], "b.example.com") {
		t.Errorf("the first block leaked into the second: %q", blocks[0])
	}
}

// A port conflict is only actionable when the operator learns WHICH Reality
// service asked for the port. This reproduces the reported server state:
// Reality service "Test3" wants 8443, which another daemon already holds.
func TestRealityPortOwners(t *testing.T) {
	owners := RealityPortOwners(map[string][]int{
		"SugerDoodR": {2053},
		"Test2":      {443},
		"Test3":      {8443},
	})
	if got := owners[8443]; len(got) != 1 || got[0] != "Test3" {
		t.Errorf("port 8443 should be attributed to Test3, got %v", got)
	}
	if got := owners[2053]; len(got) != 1 || got[0] != "SugerDoodR" {
		t.Errorf("port 2053 should be attributed to SugerDoodR, got %v", got)
	}
	if _, ok := owners[6038]; ok {
		t.Error("the Reality HTTP port is not a Reality listen port")
	}
	// Two services sharing a port must both be named, deterministically.
	shared := RealityPortOwners(map[string][]int{"b": {443}, "a": {443}})
	if len(shared[443]) != 2 || shared[443][0] != "a" || shared[443][1] != "b" {
		t.Errorf("shared port owners must be sorted: %v", shared[443])
	}
}
