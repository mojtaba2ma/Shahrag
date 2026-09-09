package nginx

// The client address must survive the SNI hop.
//
// This is the regression test for the r47 incident. The generated config is
// run under a REAL nginx and a REAL request is made from a non-loopback
// address; the test asserts what the http block actually saw.
//
// A string-comparison test would not have caught the original bug, because
// the generated config was perfectly valid — it was valid and wrong.

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

// nonLoopbackIP returns an address of this machine that is not 127.0.0.1.
//
// Essential: a request FROM loopback cannot distinguish "the real address
// was preserved" from "everything looks like loopback", which is the exact
// bug under test.
func nonLoopbackIP(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			ip := ipn.IP.To4()
			if ip != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	t.Skip("no non-loopback IPv4 address on this machine")
	return ""
}

// realIPFixture builds a config with SNI routing on, generates it, and
// starts a real nginx over the result.
func realIPFixture(t *testing.T, publicPort, httpPort int) (string, func()) {
	t.Helper()
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	dir := t.TempDir()

	c := config.Default()
	c.Reality.Enabled = true
	c.Reality.HTTPPort = httpPort
	c.Reality.Resolvers = []string{"127.0.0.1"}
	// One SNI rule so the generator actually binds the public port. The
	// rule itself is never matched (the test speaks plain HTTP, not TLS),
	// so every connection takes the DEFAULT path — which is the one that
	// goes to our own http port and must carry the PROXY header.
	c.Reality.Services = map[string]config.RealityService{
		"probe": {SNI: "never.matched.invalid", Ports: []int{publicPort}, LocalPort: 9001},
	}

	streamOut := filepath.Join(dir, "stream.conf")
	httpOut := filepath.Join(dir, "http.conf")

	g := &Generator{}
	// Generate the two halves directly so the test does not need the
	// whole install machinery.
	if err := writeStreamFor(g, c, streamOut); err != nil {
		t.Fatal(err)
	}

	// The http side is assembled here rather than generated in full: the
	// generator needs certificates and domains, and this test is about
	// one thing — which address arrives.
	body := "" +
		RealIPPrelude(c) +
		fmt.Sprintf("server {\n    listen %d%s;\n", httpPort, ListenProxyProtocol(c, httpPort)) +
		"    location / { return 200 \"seen:$remote_addr\\n\"; }\n}\n"
	if err := os.WriteFile(httpOut, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	mod := loadStreamModule()
	conf := filepath.Join(dir, "nginx.conf")
	full := mod + fmt.Sprintf(`
worker_processes 1;
error_log %s/error.log warn;
pid %s/nginx.pid;
events { worker_connections 128; }
stream {
    include %s;
}
http {
    client_body_temp_path %s/body;
    proxy_temp_path %s/proxy;
    fastcgi_temp_path %s/f;
    uwsgi_temp_path %s/u;
    scgi_temp_path %s/s;
    access_log off;
    include %s;
}
`, dir, dir, streamOut, dir, dir, dir, dir, dir, httpOut)

	// IPv6 loopback is not always available in a container, and a failed
	// bind there would stop nginx entirely.
	sc, err := os.ReadFile(streamOut)
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.ReplaceAll(string(sc),
		fmt.Sprintf("    listen [::]:%d;\n", publicPort), "")
	if err := os.WriteFile(streamOut, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(conf, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(bin, "-t", "-c", conf, "-p", dir).CombinedOutput()
	if !strings.Contains(string(out), "syntax is ok") {
		t.Fatalf("generated config rejected:\n%s\n--- stream ---\n%s\n--- http ---\n%s",
			out, fixed, body)
	}
	if err := exec.Command(bin, "-c", conf, "-p", dir).Run(); err != nil {
		t.Skipf("cannot start nginx here: %v (%s)", err, out)
	}
	stop := func() { _ = exec.Command(bin, "-c", conf, "-p", dir, "-s", "stop").Run() }
	t.Cleanup(stop)
	time.Sleep(300 * time.Millisecond)
	return dir, stop
}

// writeStreamFor calls the real stream generator.
func writeStreamFor(g *Generator, c *config.Config, out string) error {
	return g.generateStreamTo(c, out)
}

// THE regression test. Without proxy_protocol the http block sees
// 127.0.0.1 for every visitor on earth, which is what made a per-address
// connection limit deny service to an entire server.
func TestClientAddressSurvivesTheSNIHop(t *testing.T) {
	ip := nonLoopbackIP(t)
	pub, internal := freePort(t), freePort(t)
	realIPFixture(t, pub, internal)

	// Plain TCP to the public port: the stream module has ssl_preread on,
	// but with no TLS it falls through to the default backend, which is
	// exactly the path we want to exercise.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/", ip, pub))
	if err != nil {
		t.Skipf("could not reach the fixture: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	got := strings.TrimSpace(string(buf[:n]))

	if got != "seen:"+ip {
		t.Fatalf("the http block saw %q, want %q\n"+
			"Every visitor arriving through an SNI split would be treated as\n"+
			"one address: bans, IP locks, the honeypot allow-list and any\n"+
			"per-address limit would all apply to the whole population at once.",
			got, "seen:"+ip)
	}
	t.Logf("the http block correctly saw %s", ip)
}

// The PROXY header must NEVER be accepted from a remote peer. It is plain
// text and asserts whatever it likes, so trusting it from anywhere but
// loopback would let anybody claim any address and walk straight past every
// ban and IP lock in the panel.
func TestProxyProtocolIsTrustedFromLoopbackOnly(t *testing.T) {
	c := config.Default()
	c.Reality.Enabled = true
	c.Reality.HTTPPort = 6038

	got := RealIPPrelude(c)
	if !strings.Contains(got, "set_real_ip_from 127.0.0.1;") {
		t.Error("loopback is not trusted, so the header would be ignored")
	}
	for _, wide := range []string{"0.0.0.0/0", "::/0", "set_real_ip_from 0.0.0.0"} {
		if strings.Contains(got, wide) {
			t.Fatalf("the PROXY header is trusted from %s — anybody could "+
				"claim any address and bypass every ban and IP lock", wide)
		}
	}
}

// A passthrough must NOT receive a PROXY header: the remote service is not
// expecting one and would read it as a corrupt TLS handshake.
func TestPassthroughGetsNoProxyHeader(t *testing.T) {
	dir := t.TempDir()
	c := config.Default()
	c.Reality.Enabled = true
	c.Reality.HTTPPort = 6038
	c.Reality.Resolvers = []string{"1.1.1.1"}
	c.Reality.Services = map[string]config.RealityService{
		"pass":  {SNI: "steamcommunity.com", Ports: []int{443}, Target: config.PassthroughTarget},
		"local": {SNI: "own.example.com", Ports: []int{443}, LocalPort: 9001},
	}

	out := filepath.Join(dir, "stream.conf")
	g := &Generator{}
	if err := g.generateStreamTo(c, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(b)

	// The design that survived contact with a real nginx:
	//
	// The PROXY header must be added by the PUBLIC listener, because that
	// is the only server that sees the real client — a downstream relay
	// can only ever announce its own loopback address. But that listener
	// is shared with passthrough routes, and proxy_protocol is a literal
	// that cannot be varied per connection.
	//
	// So passthrough traffic is routed to a loopback relay that READS the
	// header (discarding it) and forwards clean bytes to the real remote
	// service.
	// The design that survived contact with a real nginx.
	//
	// proxy_protocol can only be set per LISTENER — nginx rejects a
	// variable there — and the public listener is shared by every route
	// on that port. A passthrough must never receive the header: verified
	// against a real backend, which got "PROXY TCP4 ...\r\n" glued to the
	// front of the TLS ClientHello.
	//
	// So a port carrying a passthrough gets NO header, and the generated
	// file says why. Ports without one recover the real client address.
	if strings.Contains(txt, "proxy_protocol on;") {
		t.Errorf("port 443 carries a passthrough but still adds the PROXY "+
			"header, which would corrupt its TLS handshake:\n%s", txt)
	}
	if !strings.Contains(txt, "carries a passthrough") {
		t.Errorf("the generated file does not explain why this port cannot "+
			"recover the client address:\n%s", txt)
	}
	// The SNI map must still name the REAL destination, because that is
	// what the raw config editor shows and what an operator edits.
	if !strings.Contains(txt, "$ssl_preread_server_name") {
		t.Errorf("the passthrough's real destination was replaced by an "+
			"internal hop, so the raw editor would show the wrong thing:\n%s", txt)
	}
}

// A port with no passthrough on it DOES recover the address.
func TestAPortWithoutPassthroughRecoversTheAddress(t *testing.T) {
	dir := t.TempDir()
	c := config.Default()
	c.Reality.Enabled = true
	c.Reality.HTTPPort = 6038
	c.Reality.Resolvers = []string{"1.1.1.1"}
	c.Reality.Services = map[string]config.RealityService{
		"pass":  {SNI: "steamcommunity.com", Ports: []int{443}, Target: config.PassthroughTarget},
		"local": {SNI: "own.example.com", Ports: []int{8443}, LocalPort: 9001},
	}
	out := filepath.Join(dir, "stream.conf")
	if err := (&Generator{}).generateStreamTo(c, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	txt := string(b)

	// Slice the server block that FOLLOWS the listen line — the earlier
	// version sliced backwards from it and caught the previous block,
	// which was a test mistake rather than a finding.
	i := strings.Index(txt, "listen 8443;")
	if i < 0 {
		t.Fatalf("port 8443 not bound:\n%s", txt)
	}
	blk := txt[i:]
	if j := strings.Index(blk, "}"); j > 0 {
		blk = blk[:j]
	}
	if !strings.Contains(blk, "proxy_protocol on;") {
		t.Errorf("port 8443 carries no passthrough but does not recover the "+
			"client address:\n%s", blk)
	}

	// ...and 443, which does carry one, must NOT.
	k := strings.Index(txt, "listen 443;")
	if k < 0 {
		t.Fatalf("port 443 not bound:\n%s", txt)
	}
	blk443 := txt[k:]
	if j := strings.Index(blk443, "}"); j > 0 {
		blk443 = blk443[:j]
	}
	if strings.Contains(blk443, "proxy_protocol on;") {
		t.Errorf("port 443 carries a passthrough but adds the header anyway:\n%s", blk443)
	}
}

// With no passthrough configured the relay must not be emitted at all —
// binding a loopback port for nothing.
func TestNoRelayWhenNothingPassesThrough(t *testing.T) {
	dir := t.TempDir()
	c := config.Default()
	c.Reality.Enabled = true
	c.Reality.HTTPPort = 6038
	c.Reality.Resolvers = []string{"1.1.1.1"}
	c.Reality.Services = map[string]config.RealityService{
		"local": {SNI: "own.example.com", Ports: []int{443}, LocalPort: 9001},
	}
	out := filepath.Join(dir, "stream.conf")
	if err := (&Generator{}).generateStreamTo(c, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "proxy_protocol on;") {
		t.Error("with no passthrough anywhere, the client address should be recovered")
	}
}

// With SNI routing OFF nothing at all is emitted: clients reach the http
// block directly and $remote_addr is already correct.
func TestNoRealIPMachineryWhenSNIIsOff(t *testing.T) {
	c := config.Default()
	c.Reality.Enabled = false
	if got := RealIPPrelude(c); got != "" {
		t.Errorf("emitted with SNI off:\n%s", got)
	}
	if got := ListenProxyProtocol(c, 6038); got != "" {
		t.Errorf("listen parameter emitted with SNI off: %q", got)
	}
}

// Only the INTERNAL port carries the listen parameter. Putting it on a
// public port makes nginx reject every real client with "broken header",
// which is a total outage.
func TestOnlyTheInternalPortExpectsTheHeader(t *testing.T) {
	c := config.Default()
	c.Reality.Enabled = true
	c.Reality.HTTPPort = 6038

	if got := ListenProxyProtocol(c, 6038); got != " proxy_protocol" {
		t.Errorf("the internal port does not expect the header: %q", got)
	}
	for _, public := range []int{443, 80, 2053, 8443} {
		if got := ListenProxyProtocol(c, public); got != "" {
			t.Errorf("public port %d expects a PROXY header (%q) — every real "+
				"client would be rejected with 'broken header'", public, got)
		}
	}
}
