package nginx

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionParsing(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		// The user's own server.
		{"nginx version: nginx/1.18.0 (Ubuntu)", false},
		{"nginx version: nginx/1.24.0", false},
		{"nginx version: nginx/1.25.0", false},
		// The exact release that introduced the separate directive.
		{"nginx version: nginx/1.25.1", true},
		{"nginx version: nginx/1.25.3", true},
		{"nginx version: nginx/1.26.3", true},
		{"nginx version: nginx/2.0.0", true},
		// Unknown must be treated as OLD: the old syntax merely warns on
		// new nginx, while the new syntax makes old nginx fail to start.
		{"", false},
		{"nginx not found", false},
		{"garbage", false},
	} {
		if got := versionAtLeast(tc.out, 1, 25, 1); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.out, got, tc.want)
		}
	}
}

// The two syntaxes must be mutually exclusive: emitting both would be a
// duplicate, and emitting neither loses HTTP/2 entirely.
func TestExactlyOneHTTP2SyntaxIsUsed(t *testing.T) {
	suffix := listenSuffix()
	line := http2Line("    ")
	if suffix != "" && line != "" {
		t.Fatal("both the listen parameter AND the standalone directive were emitted")
	}
	if suffix == "" && line == "" {
		t.Fatal("neither form was emitted, so HTTP/2 is silently off")
	}
}

// The real check: whatever this nginx is, the generated listen line must not
// produce a deprecation warning.
//
// This is the bug the r47 browser test surfaced — the generator always used
// `listen ... http2`, so on nginx 1.25.1+ every single reload printed a
// deprecation warning into the panel's own error log, and an operator
// reasonably read a warning printed during a failed save as its cause.
func TestGeneratedListenLineProducesNoWarning(t *testing.T) {
	bin := NginxBinary()
	if bin == "" {
		t.Skip("no nginx binary available")
	}
	resetHTTP2Detection()

	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir)

	conf := dir + "/nginx.conf"
	body := "events { worker_connections 64; }\n" +
		"http {\n  server {\n" +
		"    listen 127.0.0.1:18077 ssl" + listenSuffix() + ";\n" +
		http2Line("    ") +
		"    ssl_certificate " + cert + ";\n" +
		"    ssl_certificate_key " + key + ";\n" +
		"    return 204;\n  }\n}\n"
	if err := writeFile(conf, body); err != nil {
		t.Fatal(err)
	}

	out, _ := exec.Command(bin, "-t", "-c", conf, "-p", dir,
		"-e", dir+"/error.log").CombinedOutput()
	text := string(out)

	if !strings.Contains(text, "syntax is ok") {
		t.Fatalf("nginx rejected the generated listen line:\n%s\n--- config ---\n%s",
			text, body)
	}
	if strings.Contains(text, "deprecated") {
		t.Fatalf("the generated listen line is deprecated on this nginx (%s):\n%s\n"+
			"--- config ---\n%s", Version(), text, body)
	}
	t.Logf("nginx %s accepted %q with no warning",
		strings.TrimPrefix(Version(), "nginx version: "),
		strings.TrimSpace("listen ... ssl"+listenSuffix()))
}

// writeSelfSigned makes a throwaway certificate, because an `ssl` listener
// will not pass `nginx -t` without one.
func writeSelfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"test.local"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
