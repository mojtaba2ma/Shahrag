package nginx

// Emitting HTTP/2 in the syntax the running nginx actually wants.
//
// nginx changed how HTTP/2 is enabled in 1.25.1:
//
//	before 1.25.1   listen 443 ssl http2;      <- the only way
//	1.25.1 onwards  listen 443 ssl;            <- and a separate
//	                http2 on;                     directive
//
// The old form still WORKS on new nginx, but it emits
// "the \"listen ... http2\" directive is deprecated" on every single
// `nginx -t` and every reload. That warning is noise on its own, and it is
// worse than noise here: it fills the panel's own error log, it appears in
// the Logs page, and an operator reasonably reads a warning printed during
// a failed save as the cause of the failure.
//
// The new form is not an option either — it is a syntax error on anything
// older, and the user's own server runs nginx 1.18. So the generator asks
// the binary which it is and emits accordingly.
//
// Detected once and cached: `nginx -v` forks, and the answer cannot change
// while this process is running without the package being replaced, which
// restarts the panel.

import (
	"regexp"
	"strconv"
	"sync"
)

var (
	http2Once   sync.Once
	http2Modern bool
)

var reNginxVersion = regexp.MustCompile(`nginx/(\d+)\.(\d+)\.(\d+)`)

// http2SeparateDirective reports whether this nginx wants `http2 on;`
// rather than the `listen ... http2` parameter.
func http2SeparateDirective() bool {
	http2Once.Do(func() {
		http2Modern = versionAtLeast(Version(), 1, 25, 1)
	})
	return http2Modern
}

// versionAtLeast parses `nginx -v` output and compares.
//
// Returns FALSE when the version cannot be determined. That is the safe
// direction: the old syntax works everywhere and merely warns on new
// versions, whereas the new syntax is a hard failure on old ones — and a
// hard failure means nginx will not start.
func versionAtLeast(s string, wantMaj, wantMin, wantPatch int) bool {
	m := reNginxVersion.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	switch {
	case maj != wantMaj:
		return maj > wantMaj
	case min != wantMin:
		return min > wantMin
	default:
		return patch >= wantPatch
	}
}

// listenSuffix returns the HTTP/2 part of a listen directive, empty on
// nginx 1.25.1 and later.
func listenSuffix() string {
	if http2SeparateDirective() {
		return ""
	}
	return " http2"
}

// http2Line returns the standalone directive, empty on older nginx.
func http2Line(indent string) string {
	if http2SeparateDirective() {
		return indent + "http2 on;\n"
	}
	return ""
}

// resetHTTP2Detection clears the cache. Test-only: a test needs to exercise
// both branches within one process.
func resetHTTP2Detection() {
	http2Once = sync.Once{}
	http2Modern = false
}
