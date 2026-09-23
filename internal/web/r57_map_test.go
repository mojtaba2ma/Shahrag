package web

// The map changes in r57. Each of these encodes a mistake that was made and
// caught by looking at a rendered diagram, not by reading the code.

import (
	"regexp"
	"strings"
	"testing"
)

// A port must only be joined to rules that really listen on it.
//
// The first attempt drew every port to every SNI rule. On the operator's own
// config that is sixteen crossing lines, and worse it is FALSE: Test2 listens
// on 8443 alone, so a line from 443 to Test2 depicts a route that does not
// exist. A diagram that invents routes is worse than a busy one, because an
// operator reasons about their server from it.
func TestPortEdgesFollowTheRulesRealPorts(t *testing.T) {
	js := asset(t, "js/pages/map.js")
	i := strings.Index(js, "const sniTargets")
	if i < 0 {
		t.Fatal("the port-to-rule edge builder is gone")
	}
	block := js[i : i+700]
	if !strings.Contains(block, "ports.indexOf(pn.port.port)") {
		t.Error("ports are not filtered against the rule's own port list, so the " +
			"map will draw routes that do not exist")
	}
	// A rule with no port list answers everywhere; dropping it would be the
	// opposite error.
	if !strings.Contains(block, "ports.length === 0") {
		t.Error("a rule with no explicit ports is not treated as listening on all " +
			"of them, so real routes will be missing")
	}
}

// The fallback gets one line per port, not one per service.
func TestFallbackGetsOneLinePerPort(t *testing.T) {
	js := asset(t, "js/pages/map.js")
	i := strings.Index(js, "if (httpCount > 0)")
	if i < 0 {
		t.Fatal("the HTTP fallback edge builder is gone")
	}
	block := js[i : i+900]
	// The loop was the bug: sixteen strokes onto one box, each too short
	// for the gradient to be visible.
	if regexp.MustCompile(`for \([^)]*i < httpCount`).MatchString(block) {
		t.Error("the fallback still draws one line per service; that stacks a bundle " +
			"on one box and leaves the gradient no room to travel")
	}
	if !strings.Contains(block, `nginxHTTPBox.id, "in-http"`) {
		t.Error("the fallback line no longer reaches the Nginx-HTTP box")
	}
}

// The hop is the line the operator asked about. It depicts the same event as
// an in-http edge — TLS being handed over to HTTP — so it must carry the same
// gradient rather than a flat colour.
func TestHopCarriesTheGradient(t *testing.T) {
	js := asset(t, "js/pages/map.js")
	i := strings.Index(js, `if (kind === "in-http" || kind === "hop")`)
	if i < 0 {
		t.Fatal("the hop does not get a gradient")
	}
	block := js[i : i+1400]
	// A hand-routed path travels in both axes; using the straight-line
	// endpoints would compress the whole colour change into a few pixels.
	if !strings.Contains(block, "Math.min(...xs)") {
		t.Error("the hop's gradient does not span the path's real extent, so the " +
			"colour change will not be visible along it")
	}
	if !strings.Contains(block, "gradientUnits=\"userSpaceOnUse\"") {
		t.Error("objectBoundingBox degenerates on a near-horizontal path")
	}
	if !strings.Contains(block, "if (rtl)") {
		t.Error("the gradient direction is not mirrored for RTL")
	}
}

// Every port line ending on the container wall, plus a fan from that wall to
// the rules, would double each stroke now that ports reach rules directly.
func TestNoDuplicateSNIFan(t *testing.T) {
	js := asset(t, "js/pages/map.js")
	if regexp.MustCompile(`sniStageNodes\.forEach\([^)]*\)\s*=>\s*\{[^}]*"sni-fan"`).MatchString(js) {
		t.Error("the old container-to-every-rule fan is back; it doubles every " +
			"port line now that ports reach the rules themselves")
	}
	// The fallback box is the one thing no port chooses, so it keeps its
	// line from the wall.
	if !strings.Contains(js, `edges.push(["box:sni", nginxHTTPBox.id, "sni-fan"])`) {
		t.Error("the fallback box lost its line from the SNI stage")
	}
}
