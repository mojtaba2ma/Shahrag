package web

// Static assertions on the r55 fixes. Behaviour is covered by the
// real-Chromium sweep; these catch the specific mistakes that were made,
// because each one was silent in a way a reviewer would not notice.

import (
	"regexp"
	"strings"
	"testing"
)

// Every ShahragCharts method a page calls must actually be exported.
//
// stats.js called ShahragCharts.configure(), which exists inside the module
// but was not on the returned object. Every click on the chart button in a
// breakdown row threw "window.ShahragCharts.configure is not a function" and
// no chart appeared. Neither Go nor `node --check` can see this.
func TestEveryChartsMethodCalledIsExported(t *testing.T) {
	charts := asset(t, "js/charts.js")
	m := regexp.MustCompile(`return \{([^}]*)\};`).FindStringSubmatch(charts)
	if m == nil {
		t.Fatal("charts.js has no return object")
	}
	exported := map[string]bool{}
	for _, name := range strings.Split(m[1], ",") {
		name = strings.TrimSpace(name)
		if i := strings.Index(name, ":"); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name != "" {
			exported[name] = true
		}
	}
	used := regexp.MustCompile(`ShahragCharts\.([a-zA-Z_][a-zA-Z0-9_]*)`)
	for _, page := range []string{
		"js/pages/stats.js", "js/pages/health.js", "js/pages/dashboard.js",
		"js/pages/autoban.js", "js/pages/status.js",
	} {
		js, err := staticFS.ReadFile("static/" + page)
		if err != nil {
			continue
		}
		for _, call := range used.FindAllStringSubmatch(string(js), -1) {
			if !exported[call[1]] {
				t.Errorf("%s calls ShahragCharts.%s, which charts.js does not export "+
					"— this throws at run time and no chart appears", page, call[1])
			}
		}
	}
}

// A selected row has to be obvious; 7% of the accent was invisible.
func TestSelectedRowIsClearlyMarked(t *testing.T) {
	css := asset(t, "css/app.css")
	i := strings.Index(css, "tr.lv-sel")
	if i < 0 {
		t.Fatal("selected rows have no styling")
	}
	block := css[i:]
	if j := strings.Index(block, "@media (prefers-contrast"); j > 0 {
		block = block[:j+220]
	}
	strongest := 0
	for _, p := range regexp.MustCompile(`var\(--accent\) (\d+)%`).FindAllStringSubmatch(block, -1) {
		n := 0
		for _, c := range p[1] {
			n = n*10 + int(c-'0')
		}
		if n > strongest {
			strongest = n
		}
	}
	if strongest < 15 {
		t.Errorf("the selected-row tint is only %d%% of the accent; an operator "+
			"cannot tell which rows are ticked", strongest)
	}
	if !strings.Contains(block, "box-shadow") {
		t.Error("a selected row has no cue other than its background tint")
	}
	if !strings.Contains(block, `[dir="rtl"]`) {
		t.Error("the selection bar is not mirrored for RTL")
	}
}

// One styling fix covers every list only because every list shares ListView.
func TestEveryListUsesTheSharedListView(t *testing.T) {
	for _, page := range []string{
		"js/pages/services.js", "js/pages/domains.js", "js/pages/certs.js",
		"js/pages/ports.js", "js/pages/autoban.js", "js/pages/honeypot.js",
		"js/pages/realsite.js",
	} {
		if !strings.Contains(asset(t, page), "ListView.create") {
			t.Errorf("%s does not use the shared ListView, so it will not get "+
				"selection highlighting", page)
		}
	}
	if !strings.Contains(asset(t, "js/listview.js"), `selected.has(k) ? "lv-sel" : ""`) {
		t.Error("ListView no longer marks selected rows with lv-sel")
	}
}

// The notes on the logs page collapse to one line with a translated control.
// The requirement was explicit that it keep working for notes added later,
// which is why the handler is delegated rather than attached per note.
func TestLogNotesCollapse(t *testing.T) {
	js := asset(t, "js/pages/logs.js")
	for _, want := range []string{
		"log-tip-clamped", "log-tip-text", "log-tip-more",
		`t("logs.tip_more")`, `t("logs.tip_less")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the logs page is missing %q", want)
		}
	}
	// Delegation, not a per-note handler: the list repaints on every poll.
	if !strings.Contains(js, "addEventListener") || !strings.Contains(js, "closest(\".log-tip-more\")") {
		t.Error("the more/less control is not delegated, so it will stop working " +
			"on notes painted by a later refresh")
	}
	if !strings.Contains(js, "dataset.tipWired") {
		t.Error("the delegated listener is not guarded, so it will be attached " +
			"again on every render")
	}
	// The control must hide itself when the note already fits.
	if !strings.Contains(js, "scrollHeight") {
		t.Error("the control is not hidden for notes that fit on one line")
	}

	css := asset(t, "css/app.css")
	if !strings.Contains(css, "-webkit-line-clamp") {
		t.Error("the note is not clamped at a line boundary")
	}
	if !strings.Contains(css, ".log-tip-more[hidden]") {
		t.Error("the hidden state of the control is not styled, so [hidden] " +
			"will not hide an inline-block")
	}
}

// The gradient is the only colour on the map that carries meaning rather
// than identity, and it had no key at all.
func TestMapLegendExplainsTheGradient(t *testing.T) {
	js := asset(t, "js/pages/map.js")
	if !strings.Contains(js, `t("map.legend_gradient")`) {
		t.Error("the map legend has no entry for the gradient")
	}
	css := asset(t, "css/app.css")
	i := strings.Index(css, ".mp-sw-grad")
	if i < 0 {
		t.Fatal("the gradient legend swatch has no styling")
	}
	block := css[i : i+320]
	if !strings.Contains(block, "linear-gradient") {
		t.Error("the swatch is not drawn with a gradient")
	}
	// A bare var() with no fallback resolves EMPTY inside a gradient and
	// makes the whole declaration invalid — the r52 black-box bug.
	for _, v := range regexp.MustCompile(`var\(([^)]*)\)`).FindAllStringSubmatch(block, -1) {
		if !strings.Contains(v[1], ",") {
			t.Errorf("var(%s) has no literal fallback; inside a gradient that "+
				"makes the declaration invalid and the swatch renders as nothing", v[1])
		}
	}
}

// The heading was renamed in every language, not just the two the
// maintainer reads.
func TestEntryPortsHeadingIsTranslatedEverywhere(t *testing.T) {
	for _, lang := range []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"} {
		js := asset(t, "js/i18n/"+lang+".js")
		m := regexp.MustCompile(`col_ports\s*:\s*"([^"]*)"`).FindStringSubmatch(js)
		if m == nil {
			t.Errorf("%s has no col_ports", lang)
			continue
		}
		if strings.TrimSpace(m[1]) == "" {
			t.Errorf("%s: col_ports is empty", lang)
		}
		// The two the maintainer can verify by eye.
		if lang == "fa" && m[1] != "پورت‌های ورودی" {
			t.Errorf("fa col_ports = %q, want the entry-ports wording", m[1])
		}
		if lang == "en" && !strings.Contains(strings.ToLower(m[1]), "entry") {
			t.Errorf("en col_ports = %q, want it to say entry", m[1])
		}
	}
}

// Both new keys must exist in all ten languages or a panel in Korean shows
// the raw key where a button label belongs.
func TestR55KeysExistInEveryLanguage(t *testing.T) {
	for _, lang := range []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"} {
		js := asset(t, "js/i18n/"+lang+".js")
		for _, key := range []string{"legend_gradient", "tip_more", "tip_less"} {
			if !regexp.MustCompile(`(^|[\s,{])` + key + `\s*:\s*"[^"]+"`).MatchString(js) {
				t.Errorf("%s is missing %s", lang, key)
			}
		}
	}
}

// The random-path control.
//
// The operator asked for a generator beside the path field, like the one
// beside the access key, plus a warning — because pressing it next to a
// service is a plausible moment to think it changes the PANEL's address.
func TestRandomPathControl(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	for _, want := range []string{
		`id="s-path-gen"`, `id="s-path-copy"`, `id="s-path-note"`,
		"function randomPath()", `t("services.path_gen_warn")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the services page is missing %q", want)
		}
	}
	// 22 base62 characters is ~131 bits. Anything materially shorter is
	// guessable, and the number is the whole point of the feature.
	if !strings.Contains(js, "PATH_LENGTH = 22") {
		t.Error("the generated path is not 22 characters")
	}
	// Math.random is seeded predictably; a path built from it can be
	// reproduced by anyone who sees a few outputs.
	//
	// Comments are stripped first. The file EXPLAINS at length why
	// Math.random is not used, and both earlier versions of this check
	// matched that explanation — a mistake in the test, twice, not a
	// finding in the code.
	if strings.Contains(stripJSComments(js), "Math.random") {
		t.Error("the path generator uses Math.random, which is predictable")
	}
	if !strings.Contains(js, "crypto.getRandomValues") {
		t.Error("the path generator does not use a cryptographic source")
	}
	// The panel is routinely reached over plain HTTP, where
	// crypto.getRandomValues does not exist. It must fall back, not break.
	if !strings.Contains(js, "window.crypto && window.crypto.getRandomValues") {
		t.Error("no fallback for a non-secure context, where crypto is absent")
	}
	// Taking a byte modulo 62 biases the first four letters. 248 = 4*62.
	if !strings.Contains(js, "248") {
		t.Error("the generator does not reject the biasing byte range")
	}
	css := asset(t, "css/app.css")
	if !strings.Contains(css, ".path-warn") {
		t.Error("the warning has no styling")
	}
}

// The address lock has to exist on BOTH kinds of service. It was present
// for HTTP and entirely absent for SNI — RealityService had no field for it.
func TestAddressLockOnBothServiceKinds(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	if !strings.Contains(js, `id="s-allow-ips"`) {
		t.Error("the HTTP form lost its address lock")
	}
	if !strings.Contains(js, `id="r-allow-ips"`) {
		t.Error("the SNI form has no address lock")
	}
	// Separate ids and separate readers: both tabs are in the DOM at once,
	// so a shared id would let the hidden tab's empty field wipe the value.
	if !strings.Contains(js, "function readSNIAllowIPs") {
		t.Error("the SNI tab has no reader of its own")
	}
	// Always sent, including empty — otherwise a lock could never be
	// cleared, because the handler treats an omitted field as "unchanged".
	if !strings.Contains(js, "allow_ips: readSNIAllowIPs(t)") {
		t.Error("saveSNI does not send the address lock")
	}
}

// Both generator buttons must be large enough to see. They were 15px.
func TestGeneratorIconsAreBigEnough(t *testing.T) {
	js := asset(t, "js/pages/services.js")
	// Scoped to the generator rows. A blanket search for a 15px copy icon
	// also matched the unrelated "raw config" button in the LIST, which
	// nobody complained about — a mistake in the first version of this
	// test, not a second undersized button.
	for _, id := range []string{"s-path-gen", "s-path-copy", "s-gate-gen", "s-gate-copy"} {
		i := strings.Index(js, id)
		if i < 0 {
			t.Errorf("%s is missing", id)
			continue
		}
		// The icon is rendered within a few lines of the id.
		end := i + 400
		if end > len(js) {
			end = len(js)
		}
		block := js[i:end]
		if strings.Contains(block, ", 15)") {
			t.Errorf("%s still renders a 15px icon, too small to see on a phone", id)
		}
	}
	if !strings.Contains(js, `Icons.svg("dice", 18)`) {
		t.Error("the generate icon is not 18px")
	}
}

// All ten languages, or a panel in Korean shows a raw key where a warning
// about the panel's own address belongs.
func TestPathKeysExistInEveryLanguage(t *testing.T) {
	for _, lang := range []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"} {
		js := asset(t, "js/i18n/"+lang+".js")
		for _, key := range []string{"path_gen", "path_gen_hint", "path_gen_warn", "path_help"} {
			if !regexp.MustCompile(`(^|[\s,{])` + key + `\s*:\s*"[^"]+"`).MatchString(js) {
				t.Errorf("%s is missing services.%s", lang, key)
			}
		}
	}
}

// stripJSComments removes // line comments and /* */ block comments, so an
// assertion about CODE is not satisfied or broken by prose. Written for the
// checks above after a comment explaining why something is not done twice
// registered as that thing being done.
func stripJSComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	inLine, inBlock, inStr := false, false, byte(0)
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				out.WriteByte(c)
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
		case inStr != 0:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == inStr {
				inStr = 0
			}
		case c == '"' || c == '\'' || c == '`':
			inStr = c
			out.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
