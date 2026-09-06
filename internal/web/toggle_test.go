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
	// Both record types get one.
	if !strings.Contains(js, `powerToggle(n, "http"`) {
		t.Error("HTTP services have no switch")
	}
	if !strings.Contains(js, `powerToggle(n, "sni"`) {
		t.Error("SNI rules have no switch")
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
	i := strings.Index(js, `container.querySelectorAll("[data-toggle]")`)
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
	if !strings.Contains(js, "NOT a") {
		t.Error("the key-share advice does not say it is not a certificate fault")
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
