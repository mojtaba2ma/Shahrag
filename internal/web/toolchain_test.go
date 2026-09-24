package web

// The Go toolchain the installer uses.
//
// govulncheck on the r58 tree found 29 vulnerabilities in the Go standard
// library that this code actually calls — not "in a module we require", but
// on real call paths through crypto/x509, crypto/tls and net/http. All of
// them were fixed in patch releases of Go 1.25; the installer was compiling
// with 1.25.0.
//
// The dangerous part was the fallback. install.sh asks go.dev for the latest
// version and falls back to a hardcoded one when that request fails — which
// is precisely what happens on the filtered networks this panel exists for.
// So the operators most in need of a patched toolchain were the only ones
// guaranteed to get the unpatched one.
//
// Re-measured after the fix: 29 vulnerabilities to 0, with go.mod unchanged.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// minGoPatch is the lowest 1.25.x that carries the security fixes. Raise it
// when a new advisory lands, never lower it.
const minGoPatch = 14

func TestInstallerFallsBackToAPatchedToolchain(t *testing.T) {
	b, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Skip("install.sh not reachable from here")
	}
	s := string(b)

	m := regexp.MustCompile(`GO_VER_FALLBACK="go1\.(\d+)\.(\d+)"`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("install.sh has no pinned Go fallback; on a filtered network it " +
			"will fall back to whatever literal is in the case statement")
	}
	minor, _ := strconv.Atoi(m[1])
	patch, _ := strconv.Atoi(m[2])

	if minor == 25 && patch < minGoPatch {
		t.Errorf("the fallback is go1.%d.%d, which has 29 standard-library "+
			"vulnerabilities this code reaches; 1.25.%d or later is required",
			minor, patch, minGoPatch)
	}
	if minor < 25 {
		t.Errorf("the fallback go1.%d.%d is older than the language version "+
			"go.mod requires", minor, patch)
	}

	// The literal 1.25.0 must not survive anywhere as a fallback.
	if strings.Contains(s, `GO_VER_DL="go1.25.0"`) {
		t.Error("install.sh still falls back to go1.25.0, the vulnerable release")
	}
}

// go.mod must keep its language version, whatever compiler is used.
//
// The `go` directive is a minimum LANGUAGE version, not a compiler version.
// Raising it makes the go command try to fetch a newer toolchain at build
// time, which fails behind a filter and takes the whole install with it. A
// patched compiler fixes the standard library without touching this line —
// that is the entire reason the fix is where it is.
func TestGoModLanguageVersionIsPinned(t *testing.T) {
	b, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Skip("go.mod not reachable from here")
	}
	s := string(b)

	if !regexp.MustCompile(`(?m)^go 1\.25\.0$`).MatchString(s) {
		t.Errorf("go.mod no longer says 'go 1.25.0'; a higher directive makes "+
			"the build download a toolchain, which is fatal on a filtered "+
			"network:\n%s", s)
	}
	// A `toolchain` line does the same damage by a different route.
	if regexp.MustCompile(`(?m)^toolchain `).MatchString(s) {
		t.Error("go.mod has a toolchain directive; it will trigger a toolchain " +
			"download at build time")
	}
}
