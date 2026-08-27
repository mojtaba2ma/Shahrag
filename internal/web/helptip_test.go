package web

// Regression tests for the hover-help rework.
//
// Long explanatory paragraphs used to sit permanently under the form fields
// (`<span class="hint">${t("reality.target_hint")}</span>` and friends), and
// the pass-through checkbox was labelled with a parenthetical. Both are gone:
// the explanations now live behind a small question-mark icon that shows a
// tooltip on hover. These tests read the EMBEDDED assets, so they check what
// the binary actually ships, not a copy on disk.

import (
	"regexp"
	"strings"
	"testing"
)

func readAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

var i18nFiles = []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"}

// The tooltip machinery must exist end to end: an icon builder, a bubble
// renderer, and the styling for both. A missing piece would render a "?"
// that does nothing.
func TestHelpTooltipPlumbingExists(t *testing.T) {
	icons := readAsset(t, "js/icons.js")
	for _, want := range []string{"function help(", "help-tip", "data-tip", "help,"} {
		if !strings.Contains(icons, want) {
			t.Errorf("icons.js is missing %q", want)
		}
	}
	// The icon must be exported from the module, otherwise Icons.help is
	// undefined and every page that calls it throws while rendering.
	if !regexp.MustCompile(`return\s*\{[^}]*\bhelp\b`).MatchString(icons) {
		t.Error("icons.js does not export help()")
	}

	app := readAsset(t, "js/app.js")
	for _, want := range []string{"tip-bubble", "function showTip", "function hideTip", `closest(".help-tip")`} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js is missing %q", want)
		}
	}
	// The bubble is appended to <body> on purpose: inside .modal-body
	// (overflow-y:auto) or .table-wrap it would be clipped away.
	if !strings.Contains(app, "document.body.appendChild(tipEl)") {
		t.Error("the tooltip must be attached to <body> or it gets clipped by scroll boxes")
	}
	// Closing a modal must drop a bubble that is parented to <body>,
	// otherwise it survives the dialog and floats over the page.
	if !regexp.MustCompile(`window\.closeModal = \(\) => \{[^}]*hideTip\(\)`).MatchString(app) {
		t.Error("closeModal must hide a visible tooltip")
	}

	// Regression: the first version hid the bubble on ANY scroll event.
	// Browsers scroll a field into view before focusing/hovering it, so the
	// bubble was created and destroyed in the same tick and the tooltip
	// simply never appeared on a page taller than the viewport (reproduced
	// with real Chromium on Settings → lock minutes). It must re-anchor
	// instead, and only give up once the icon leaves the viewport.
	if strings.Contains(app, `addEventListener("scroll", hideTip`) {
		t.Error("scrolling must reposition the tooltip, not destroy it")
	}
	if !strings.Contains(app, "function repositionTip") ||
		!strings.Contains(app, `addEventListener("scroll", repositionTip, true)`) {
		t.Error("app.js must re-anchor the tooltip while scrolling")
	}

	// The English fallback keeps a tooltip readable in the languages whose
	// dictionaries do not carry every hint key; without it the bubble showed
	// the raw dotted path.
	if !strings.Contains(app, "lookup(window.I18N.en, parts)") {
		t.Error("t() must fall back to English before giving up on a key")
	}

	css := readAsset(t, "css/app.css")
	for _, want := range []string{".help-tip", ".tip-bubble", ".tip-bubble.visible"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %q", want)
		}
	}
	// A tooltip that captures the pointer would immediately trigger mouseout
	// on the icon underneath and flicker.
	if !regexp.MustCompile(`(?s)\.tip-bubble\s*\{[^}]*pointer-events:\s*none`).MatchString(css) {
		t.Error(".tip-bubble must set pointer-events:none")
	}
	// It has to draw above the modal overlay (z-index 500/1000 in this sheet).
	m := regexp.MustCompile(`(?s)\.tip-bubble\s*\{[^}]*z-index:\s*(\d+)`).FindStringSubmatch(css)
	if m == nil {
		t.Fatal(".tip-bubble has no z-index")
	}
	if m[1] < "1000" || len(m[1]) < 4 {
		t.Errorf(".tip-bubble z-index %s is not above the modal overlay", m[1])
	}
}

// The explanations the user asked to remove from the page body must not be
// rendered as inline paragraphs any more — they belong to a help icon.
func TestExplanationsMovedBehindHelpIcons(t *testing.T) {
	cases := []struct {
		file string
		key  string
	}{
		{"js/pages/services.js", "reality.target_hint"},
		{"js/pages/services.js", "reality.sni_hint"},
		{"js/pages/settings.js", "reality.resolvers_hint"},
		{"js/pages/settings.js", "settings.lock_minutes_hint"},
	}
	inlineHint := func(src, key string) bool {
		// <span class="hint">${t("key")}</span> or the same in a <div>
		re := regexp.MustCompile(`<(span|div) class="hint"[^>]*>\$\{t\("` + regexp.QuoteMeta(key) + `"\)\}`)
		return re.MatchString(src)
	}
	for _, c := range cases {
		src := readAsset(t, c.file)
		if inlineHint(src, c.key) {
			t.Errorf("%s still renders %s as an inline paragraph", c.file, c.key)
		}
		if !strings.Contains(src, `Icons.help(t("`+c.key+`"))`) {
			t.Errorf("%s does not expose %s through a help icon", c.file, c.key)
		}
	}

	// The DNS/AdGuard note was dropped entirely: it explained an external
	// product, not this panel's field.
	settings := readAsset(t, "js/pages/settings.js")
	if strings.Contains(settings, "adguard_note") {
		t.Error("settings.js still renders the AdGuard note")
	}

	// The dead SNI page was removed when routing merged into Services; if it
	// comes back it will ship the old inline paragraphs again.
	if _, err := staticFS.ReadFile("static/js/pages/reality.js"); err == nil {
		t.Error("pages/reality.js is dead code and must not be shipped")
	}
}

// "عبور مستقیم به اینترنت (تحریم‌شکن)" → "عبور مستقیم": the parenthetical is
// gone from the checkbox label, and the detail it carried moved to a tooltip
// key that every language must define (a missing key renders as the raw
// dotted path, e.g. "reality.target_pass_help").
func TestPassThroughLabelIsShortAndHasHelpInEveryLanguage(t *testing.T) {
	for _, lang := range i18nFiles {
		src := readAsset(t, "js/i18n/"+lang+".js")

		m := regexp.MustCompile(`target_pass:\s*"([^"]*)"`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("%s.js has no target_pass", lang)
		}
		if strings.ContainsAny(m[1], "()（）") {
			t.Errorf("%s.js target_pass still carries a parenthetical: %q", lang, m[1])
		}
		if strings.Contains(m[1], "تحریم") || strings.Contains(strings.ToLower(m[1]), "unblock") {
			t.Errorf("%s.js target_pass was not shortened: %q", lang, m[1])
		}

		if !regexp.MustCompile(`target_pass_help:\s*"[^"]+"`).MatchString(src) {
			t.Errorf("%s.js is missing target_pass_help", lang)
		}
	}
}
