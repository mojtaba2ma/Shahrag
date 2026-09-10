package web

// Static assertions on the shipped assets for the three UI changes:
// the service on/off switch, the log copy buttons, and the key generator.
// Behaviour is covered by the real-Chromium sweep; these catch a file that
// was edited without its counterpart.

import (
	"strings"
	"testing"
)

func asset(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestServiceListHasAToggle(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	for _, want := range []string{
		`function powerToggle(`,
		`role="switch"`,
		`aria-checked=`,
		`data-toggle=`,
		`data-toggle-kind=`,
		`/toggle`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the services page is missing %q", want)
		}
	}
	// Both record types get one. Asserted through the shared row builder
	// rather than by matching two literal call sites: since r49 the list
	// is rendered by ListView from one flat array covering both kinds, so
	// there is a single call and the KIND comes from the row.
	if !strings.Contains(js, "powerToggle(r.name, r.kind, r.enabled") {
		t.Error("the list does not render a switch per row")
	}
	if !strings.Contains(js, `kind: "http"`) || !strings.Contains(js, `kind: "sni"`) {
		t.Error("the row array does not carry both record types")
	}
}

// The list and the dialog must be driven by the same painting function, or
// they will drift apart the first time one of them is edited.
func TestListAndDialogShareToggleState(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	if !strings.Contains(js, "function applyToggleState(") {
		t.Fatal("there is no shared function for painting a switch")
	}
	if strings.Count(js, "applyToggleState(") < 3 {
		t.Error("the shared painter is not used by both the list and the dialog")
	}
	if !strings.Contains(js, "function syncRowToggle(") {
		t.Error("toggling inside the dialog does not update the row behind it")
	}
	// Scoped by kind as well as name: the two record types may share a name.
	if !strings.Contains(js, `[data-toggle-kind="${kind}"]`) {
		t.Error("the row lookup is not scoped by record type")
	}
}

// The switch must not paint itself before the server agrees.
func TestToggleWaitsForTheServer(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	// The wiring moved into wireToggles(root) in r49, because ListView
	// re-renders the table and every handler has to be reattached.
	i := strings.Index(js, `root.querySelectorAll("[data-toggle]")`)
	if i < 0 {
		t.Fatal("the list toggle is not wired")
	}
	block := js[i : i+1600]
	if !strings.Contains(block, "applyToggleState(b, res.enabled)") {
		t.Error("the switch is painted from local state rather than the server's answer")
	}
	if !strings.Contains(block, "b.disabled = true") {
		t.Error("the switch is not locked while the request is in flight")
	}
}

// A failed reload must be reported, never swallowed.
func TestToggleReportsAFailedReload(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	if strings.Count(js, "res.applied === false") < 2 {
		t.Error("a rejected nginx config is not surfaced in both the list and the dialog")
	}
	if !strings.Contains(js, "services.apply_failed") {
		t.Error("there is no message for a rejected configuration")
	}
}

func TestLogsHaveCopyButtons(t *testing.T) {
	js := asset(t, "js/pages/logs.js")
	for _, want := range []string{
		`id="lg-copy-all"`,
		`class="log-entry"`,
		`log-copy`,
		"function entryText(",
		"ShahragWireCopy",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the logs page is missing %q", want)
		}
	}
	// Copy-all must reflect what is on screen, not the whole file.
	if !strings.Contains(js, "shown.map(entryText)") {
		t.Error(`"copy all" does not copy exactly the filtered, limited view`)
	}
	// A log line is full of double quotes; the attribute must be escaped.
	if !strings.Contains(js, "function escapeAttr(") {
		t.Error("the copy payload is not attribute-escaped, so quoted log lines will be truncated")
	}
	if !strings.Contains(js, `data-copy="${escapeAttr(`) {
		t.Error("the per-entry payload is not escaped")
	}
}

func TestCopyAnimationKeepsALabelledButtonIntact(t *testing.T) {
	js := asset(t, "js/app.js")
	if !strings.Contains(js, `b.querySelector(".btn-label")`) {
		t.Error("the copy animation replaces the whole button, so a labelled button collapses to a bare tick")
	}
	if !strings.Contains(js, `b.classList.add("copied")`) {
		t.Error("the copied animation class is gone")
	}
}

func TestGateKeyGeneratorIsWired(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	for _, want := range []string{
		`id="s-gate-gen"`,
		`/api/services/gate-secret`,
		`Icons.svg("dice"`,
		`services.gate_key_gen`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the key generator is missing %q", want)
		}
	}
	icons := asset(t, "js/icons.js")
	for _, name := range []string{"power:", "dice:"} {
		if !strings.Contains(icons, name) {
			t.Errorf("icon %q is not defined", name)
		}
	}
}

func TestToggleStylesExist(t *testing.T) {
	css := asset(t, "css/app.css")
	for _, sel := range []string{
		".pw-toggle", ".pw-toggle.on .pw-thumb", `[dir="rtl"] .pw-toggle.on .pw-thumb`,
		".row-off", ".badge-off", ".power-row", ".log-copy",
	} {
		if !strings.Contains(css, sel) {
			t.Errorf("%s has no rule", sel)
		}
	}
	// The switch must be reachable and visible on a touch screen.
	if !strings.Contains(css, "@media (hover: none)") {
		t.Error("the per-entry copy button is hover-only, so it is unreachable on a phone")
	}
}

// The panel's own advice must be correct. Two of the messages the user
// sees most often were being explained wrongly:
//
//   - "recv() failed (104)" was matched by the generic certificate rule and
//     answered with "check your certificate", which is unrelated. 104 is the
//     peer hanging up, and on an upgraded (WebSocket) connection that is the
//     normal way such a connection ends.
//   - "bad key share" is a TLS parameter mismatch decided before any
//     certificate is used, so blaming the certificate sent people hunting a
//     fault that does not exist.
func TestLogTipsDiagnoseTheCommonErrorsCorrectly(t *testing.T) {
	js := asset(t, "js/pages/logs.js")

	i104 := strings.Index(js, `recv\(\) failed \(104`)
	iCert := strings.Index(js, "SSL_do_handshake|handshake failed|certificate")
	if i104 < 0 {
		t.Fatal("there is no rule for the most common error in the log")
	}
	if iCert >= 0 && i104 > iCert {
		t.Error("the 104 rule sits after the generic certificate rule, so it never runs")
	}
	iKey := strings.Index(js, "bad key share")
	if iKey < 0 {
		t.Error("no rule explains a TLS parameter mismatch")
	}
	if iCert >= 0 && iKey > iCert {
		t.Error("the key-share rule is shadowed by the certificate rule")
	}
	// The wording itself now lives in the translation files, so assert it
	// where it actually is rather than in the page module.
	en := asset(t, "js/i18n/en.js")
	if !strings.Contains(en, "NOT a certificate fault") {
		t.Error("the key-share advice does not say it is not a certificate fault")
	}
	fa := asset(t, "js/i18n/fa.js")
	if !strings.Contains(fa, "tip_tls_mismatch") {
		t.Error("the key-share advice has no Persian translation")
	}
	// It must distinguish an upgraded connection from an ordinary one:
	// they mean different things to whoever is reading.
	if !strings.Contains(js, "upgraded connection/.test(msg)") {
		t.Error("the 104 advice does not distinguish a WebSocket close from a mid-response drop")
	}
}

// Every language must carry the new keys; a missing one falls back to
// English, which reads as a bug in a Persian UI.
func TestToggleKeysExistInEveryLanguage(t *testing.T) {
	keys := []string{
		"enabled:", "enabled_help:", "state_on:", "state_off:",
		"enable:", "disable:", "disabled:", "enabled_ok:", "disabled_ok:",
		"apply_failed:", "gate_key_gen:", "gate_key_gen_hint:",
		"copy_all:", "copy_all_hint:", "copy_one:",
	}
	for _, l := range []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"} {
		s := asset(t, "js/i18n/"+l+".js")
		for _, k := range keys {
			if !strings.Contains(s, k) {
				t.Errorf("%s is missing %s", l, k)
			}
		}
	}
}

// ── r39: icon sizes, copy placement, translated tips ────────────────

// The icons shipped in r38 were too small to see. These pin the sizes so a
// future edit cannot quietly shrink them again.
func TestIconsAreLargeEnoughToSee(t *testing.T) {
	css := asset(t, "css/app.css")
	// The per-entry copy button was 26px wide while .btn adds 14px of
	// horizontal padding, leaving 2px of usable width: the icon rendered as
	// a 2x18px sliver. Measured in a real browser, not guessed.
	i := strings.Index(css, ".log-copy {")
	if i < 0 {
		t.Fatal("the log copy button has no rule")
	}
	block := css[i : i+strings.Index(css[i:], "}")]
	if !strings.Contains(block, "padding: 0") {
		t.Error("the copy button still inherits button padding, which squashes its icon")
	}
	if !strings.Contains(css, ".log-copy svg { width: 16px") {
		t.Error("the per-entry copy icon is not given an explicit, visible size")
	}
	// "Copy all" carries a label, and the icon was measured at 0px wide.
	if !strings.Contains(css, "#lg-copy-all svg") {
		t.Error("the copy-all icon has no size rule, so the label squeezes it to nothing")
	}
	// The switch keeps its original 38x22 geometry: it is a control, not an
	// icon, and it reads best at that size. What must hold is that the
	// thumb's travel matches the track, or the thumb slides out of it.
	//
	//   travel = track width - thumb width - 2 * inset
	//          = 38 - 16 - 4 = 18 = (--pw-w) - 20 ... expressed as -22px
	//            because translateX starts from the 2px inset.
	if !strings.Contains(css, "--pw-w: 38px") {
		t.Error("the switch is not at its intended 38px width")
	}
	if !strings.Contains(css, ".pw-toggle .pw-thumb svg { width: 10px") {
		t.Error("the switch icon size changed unintentionally")
	}
	if !strings.Contains(css, "var(--pw-w) - 22px") {
		t.Error("the thumb travel does not match the track geometry")
	}
	if strings.Contains(css, "var(--pw-w) - 26px") {
		t.Error("a stale travel value would push the thumb outside its track")
	}
}

// The switch icon is deliberately smaller than the row action icons: it
// sits inside a 16px thumb. Pinned so it is not "fixed" again by mistake.
func TestSwitchIconStaysInsideItsThumb(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	if !strings.Contains(js, `Icons.svg("power", 10)`) {
		t.Error("the switch icon no longer matches the 16px thumb it sits in")
	}
}

func TestServiceRowIconsMatchTheRestOfThePanel(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	for _, small := range []string{`Icons.svg("edit", 13)`, `Icons.svg("trash", 13)`, `Icons.svg("copy", 13)`} {
		if strings.Contains(js, small) {
			t.Errorf("%s is still at the shrunken size", small)
		}
	}
	for _, want := range []string{`Icons.svg("edit", 15)`, `Icons.svg("trash", 15)`} {
		if !strings.Contains(js, want) {
			t.Errorf("expected %s", want)
		}
	}
}

// The copy button sat between the timestamp and the level badge, which put
// it in the middle of the header. It belongs at the far end, with the level
// badge beside it.
func TestCopyButtonSitsAfterTheMessageLevel(t *testing.T) {
	js := asset(t, "js/pages/logs.js")
	iCopy := strings.Index(js, "log-copy")
	iLevel := strings.Index(js, `<span class="log-level`)
	if iCopy < 0 || iLevel < 0 {
		t.Fatal("the log header is missing the copy button or the level badge")
	}
	if iCopy > iLevel {
		t.Error("the copy button is still rendered after the level badge")
	}
	css := asset(t, "css/app.css")
	if !strings.Contains(css, ".log-head .log-copy ~ .log-level { margin-inline-start: 0; }") {
		t.Error("the level badge still claims the auto margin, so the two fight for position")
	}
}

// The tips are the panel talking to its operator, so they must follow the
// interface language. The log line itself must stay verbatim English.
func TestLogTipsAreTranslated(t *testing.T) {
	js := asset(t, "js/pages/logs.js")

	// No finished English sentence may remain in the page module.
	for _, leftover := range []string{
		"nginx routed this correctly",
		"104 means the other end closed",
		"is NOT a certificate fault",
		"Another process already holds this port",
	} {
		if strings.Contains(js, leftover) {
			t.Errorf("a hard-coded English tip is still in the code: %q", leftover)
		}
	}
	if !strings.Contains(js, `t("logs.tip_" + key)`) {
		t.Error("the tips are not looked up through the translation table")
	}
	if !strings.Contains(js, "function tipFor(msg, byPort, t)") {
		t.Error("tipFor does not receive the translator")
	}

	// The message itself must stay ltr; only the tip follows the UI.
	css := asset(t, "css/app.css")
	i := strings.Index(css, ".log-tip {")
	block := css[i : i+strings.Index(css[i:], "}")]
	if strings.Contains(block, "direction: ltr") {
		t.Error("the tip is still forced left-to-right, which breaks a Persian sentence")
	}
	if !strings.Contains(css, ".log-msg {") {
		t.Fatal("the message rule is gone")
	}
	j := strings.Index(css, ".log-msg {")
	if !strings.Contains(css[j:j+strings.Index(css[j:], "}")], "direction: ltr") {
		t.Error("the log message must stay left-to-right — it is nginx's own output")
	}
	// A Latin command inside a right-to-left sentence needs isolation.
	if !strings.Contains(css, "unicode-bidi: isolate") {
		t.Error("embedded commands are not bidi-isolated, so their punctuation reorders")
	}
}

// The tip became markup, which introduces two hazards: values taken from
// the log line must be escaped, and the clipboard must get plain text.
func TestTipMarkupIsSafeAndCopiesAsText(t *testing.T) {
	js := asset(t, "js/pages/logs.js")
	if !strings.Contains(js, "out = escapeHTML(out)") {
		t.Error("the translated template is not escaped before substitution")
	}
	if !strings.Contains(js, "escapeHTML(String(vars[k]))") {
		t.Error("values taken from the log line are injected without escaping")
	}
	if strings.Contains(js, "${escapeHTML(e.tip)}") {
		t.Error("the tip is still double-escaped, so its markup would be shown literally")
	}
	if !strings.Contains(js, "function stripHTML(") || !strings.Contains(js, "stripHTML(e.tip)") {
		t.Error("the clipboard would receive raw <code> tags instead of readable text")
	}
}

// Every language must define every tip key, and the placeholders must
// survive translation or the substitution silently does nothing.
func TestTipKeysAndPlaceholdersInEveryLanguage(t *testing.T) {
	keys := []string{
		"tip_upstream_down", "tip_upstream_down_local", "tip_upstream_down_real",
		"tip_check_with", "tip_conflicting", "tip_no_resolver", "tip_port_taken",
		"tip_reset_upgraded", "tip_reset_plain", "tip_reset_common",
		"tip_reset_backend", "tip_reset_when", "tip_tls_mismatch",
		"tip_tls_cert", "tip_worker_conns",
	}
	for _, l := range []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"} {
		s := asset(t, "js/i18n/"+l+".js")
		for _, k := range keys {
			if !strings.Contains(s, k+":") {
				t.Errorf("%s is missing %s", l, k)
			}
		}
		// The substituted placeholders are the whole point of these three.
		for _, pair := range [][2]string{
			{"tip_check_with", "%c"},
			{"tip_upstream_down", "%s"},
			{"tip_reset_backend", "%p"},
		} {
			i := strings.Index(s, pair[0]+":")
			if i < 0 {
				continue
			}
			end := i + 400
			if end > len(s) {
				end = len(s)
			}
			if !strings.Contains(s[i:end], pair[1]) {
				t.Errorf("%s: %s lost its %s placeholder", l, pair[0], pair[1])
			}
		}
	}
}
