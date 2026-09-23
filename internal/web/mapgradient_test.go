package web

// The hand-over gradient on the map.
//
// The operator reported twice that the gradient "cannot be seen". The first
// time it genuinely was not drawn. The second time it WAS drawn and was
// still invisible, because both of its stops borrowed palette variables and
// --chart-3 had drifted to orange — the same orange as the SNI container the
// line leaves, so the first half of the line disappeared into its own
// background. Measured in a real browser: stop one resolved to
// oklch(0.78 0.17 60).

import (
	"regexp"
	"strings"
	"testing"
)

// The violet end must be a literal, not a palette variable.
//
// It means one specific thing — "this connection is changing protocol" — so
// it has to stay distinct from the orange SNI stage and the green HTTP stage
// in every theme. A palette variable is free to be re-tuned for chart
// legibility and silently destroy that contrast, which is exactly what
// happened.
func TestHandoverGradientUsesALiteralViolet(t *testing.T) {
	css := asset(t, "css/app.css")
	i := strings.Index(css, ".mp-stop-sni")
	if i < 0 {
		t.Fatal("the gradient has no start colour")
	}
	rule := css[i : i+120]

	if strings.Contains(rule, "var(--chart-3") {
		t.Error("the gradient starts on --chart-3, which is the ORANGE the SNI " +
			"container is drawn in — the line vanishes into its own background")
	}
	if strings.Contains(rule, "var(--chart-4") {
		t.Error("the gradient starts on --chart-4, which is violet in only four of " +
			"the six themes and is already the backend-box colour")
	}
	if !regexp.MustCompile(`stop-color:\s*#[0-9a-fA-F]{6}`).MatchString(rule) {
		t.Errorf("the start colour is not a literal: %q", rule)
	}
}

// Every var() inside a gradient stop needs a literal fallback. A bare var()
// that resolves empty makes the whole declaration invalid — the r52 bug that
// painted five map elements solid black.
func TestGradientStopsHaveFallbacks(t *testing.T) {
	css := asset(t, "css/app.css")
	for _, sel := range []string{".mp-stop-sni", ".mp-stop-http"} {
		i := strings.Index(css, sel)
		if i < 0 {
			t.Errorf("%s is missing", sel)
			continue
		}
		rule := css[i : i+120]
		for _, v := range regexp.MustCompile(`var\(([^)]*)\)`).FindAllStringSubmatch(rule, -1) {
			if !strings.Contains(v[1], ",") {
				t.Errorf("%s uses var(%s) with no fallback; if it resolves empty the "+
					"stop-color is invalid and the gradient does not render", sel, v[1])
			}
		}
	}
}

// The hop must not carry a flat stroke of its own.
//
// The gradient is applied as an inline style and wins, so a stroke in the
// class is dead weight — except when the gradient fails, where it would hide
// the failure behind a plausible green line. Better that a broken gradient
// looks broken.
func TestHopHasNoFlatStrokeHidingAFailedGradient(t *testing.T) {
	css := asset(t, "css/app.css")
	i := strings.Index(css, ".mp-edge-hop {")
	if i < 0 {
		t.Fatal("the hop has no styling")
	}
	rule := css[i : i+160]
	if regexp.MustCompile(`[^-]stroke:\s*var\(|[^-]stroke:\s*#`).MatchString(rule) {
		t.Errorf("the hop sets a flat stroke, which would mask a gradient that "+
			"failed to resolve: %q", rule)
	}
	// It must still be the thickest edge: it is the only line crossing the
	// whole diagram and has to read as the primary route.
	m := regexp.MustCompile(`stroke-width:\s*([0-9.]+)`).FindStringSubmatch(rule)
	if m == nil {
		t.Fatal("the hop has no stroke-width")
	}
	if m[1] < "2" {
		t.Errorf("the hop is %spx wide; it should stand out from the 1.8px edges", m[1])
	}
}

// Both edge kinds that depict the TLS-to-HTTP hand-over must be painted
// with the gradient, or the two halves of one story look like two
// unrelated things.
func TestBothHandoverEdgeKindsGetTheGradient(t *testing.T) {
	js := asset(t, "js/pages/map.js")
	if !strings.Contains(js, `kind === "in-http" || kind === "hop"`) {
		t.Error("the gradient is no longer applied to both in-http and hop edges")
	}
	// And the legend must explain it, or the colour is decoration.
	if !strings.Contains(js, `t("map.legend_gradient")`) {
		t.Error("the legend has no entry for the gradient")
	}
}
