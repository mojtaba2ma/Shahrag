package web

// Static assertions on the r54 assets. Behaviour is covered by the
// real-Chromium sweep; these catch a file edited without its counterpart,
// and one class of bug that a unit test CAN catch and a reviewer cannot:
// calling a shared helper with the wrong signature.

import (
	"regexp"
	"strings"
	"testing"
)

// confirmDialog is (message, onConfirm, opts) and returns NOTHING.
//
// Awaiting it never resolves, so the code after the await never runs and the
// modal stays on screen with a click-blocking overlay over the whole page.
// That is exactly what happened when these pages were first written, and the
// symptom — "the panel freezes after you confirm" — looks nothing like the
// cause. Every call site is checked here because the failure is silent: no
// exception, no console error, just a page that stops responding.
func TestConfirmDialogIsCalledWithTheRightSignature(t *testing.T) {
	awaited := regexp.MustCompile(`await\s+confirmDialog\s*\(`)
	objectArg := regexp.MustCompile(`confirmDialog\s*\(\s*\{`)
	for _, page := range []string{
		"js/pages/realsite.js", "js/pages/templates.js",
		"js/pages/autoban.js", "js/pages/honeypot.js",
		"js/pages/services.js", "js/pages/domains.js", "js/pages/certs.js",
	} {
		js, err := staticFS.ReadFile("static/" + page)
		if err != nil {
			continue // not every page exists in every build
		}
		s := string(js)
		if loc := awaited.FindStringIndex(s); loc != nil {
			t.Errorf("%s awaits confirmDialog, which returns nothing — "+
				"the code after the await never runs and the modal never closes", page)
		}
		if loc := objectArg.FindStringIndex(s); loc != nil {
			t.Errorf("%s calls confirmDialog with an object; the signature is "+
				"(message, onConfirm, opts)", page)
		}
	}
}

// The gallery must work with no repository at all, which means the built-in
// templates have to reach the page from the local list, not from index.json.
func TestGalleryRendersLocalTemplates(t *testing.T) {
	js := asset(t, "js/pages/templates.js")
	for _, want := range []string{
		`api("/api/templates")`,
		`x.installed`,
		`tpl.state_offline`,
		`data-install=`,
		`data-detail=`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the gallery is missing %q", want)
		}
	}
	// A thumbnail must go through the panel. Linking jsDelivr directly
	// would make the operator's BROWSER talk to a CDN — a leak, and a
	// broken image on the network this panel exists for.
	if strings.Contains(js, "jsdelivr") || strings.Contains(js, "raw.githubusercontent") {
		t.Error("the gallery links a mirror directly; assets must be proxied by the panel")
	}
	if !strings.Contains(js, `src="api/templates/`) {
		t.Error("thumbnails are not proxied through the panel")
	}
	// Arbitrary HTML from a repository in an iframe needs the sandbox.
	if !strings.Contains(js, `sandbox=""`) {
		t.Error("the preview iframe is not sandboxed")
	}
}

// The switch semantics are the whole safety story of this feature.
func TestRealSitePageWiring(t *testing.T) {
	js := asset(t, "js/pages/realsite.js")
	for _, want := range []string{
		`/api/realsite`,
		`data-toggle=`,
		`data-edit=`,
		`/toggle`,
		`rs.confirm_on_body`,
		`data-code-on=`,
		`#rd-mode`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the real-site page is missing %q", want)
		}
	}
	// Only installed templates may be offered; a remote id would save a
	// name the renderer cannot resolve and the domain would silently drop
	// back to the fake page.
	if !strings.Contains(js, "filter(x => x.installed)") {
		t.Error("the template dropdown does not filter to installed templates")
	}
}

// The fake page must still be reachable: after an upgrade it is what every
// domain is serving, so hiding it would strand the people who need it.
func TestFakePageIsStillReachable(t *testing.T) {
	js := asset(t, "js/pages/site.js")
	if !strings.Contains(js, `page: "fakesite"`) {
		t.Error("the fake page is no longer reachable from the menu")
	}
	app := asset(t, "js/app.js")
	if !strings.Contains(app, `{ id: "site", icon: "globe" }`) {
		t.Error("the Site entry is not in the navigation")
	}
}

// Every icon named must exist, or the button renders empty and the operator
// sees a blank square.
func TestRealSiteIconsExist(t *testing.T) {
	icons := asset(t, "js/icons.js")
	used := regexp.MustCompile(`Icons\.svg\("([a-z0-9_]+)"`)
	for _, page := range []string{"js/pages/realsite.js", "js/pages/templates.js", "js/pages/site.js"} {
		js := asset(t, page)
		for _, m := range used.FindAllStringSubmatch(js, -1) {
			if !regexp.MustCompile(`(^|\s)` + m[1] + `:`).MatchString(icons) {
				t.Errorf("%s uses icon %q which icons.js does not define", page, m[1])
			}
		}
	}
}

// A fixed pixel width in a grid is a horizontal scrollbar on a 320 px phone.
// minmax(min(100%, N), 1fr) is the form that collapses instead of overflowing.
func TestGalleryGridCollapsesOnAPhone(t *testing.T) {
	css := asset(t, "css/app.css")
	i := strings.Index(css, ".tp-grid")
	if i < 0 {
		t.Fatal("the gallery grid has no styles")
	}
	block := css[i : i+240]
	if !strings.Contains(block, "min(100%") {
		t.Errorf("the gallery grid does not collapse below its track size:\n%s", block)
	}
	// The drawer must not be wider than the viewport.
	j := strings.Index(css, ".tp-drawer-panel")
	if j < 0 || !strings.Contains(css[j:j+240], "min(100%") {
		t.Error("the drawer is not width-capped to the viewport")
	}
}

// All ten languages must carry the new keys, or a panel in Korean shows
// "rs.title" where a heading should be.
func TestRealSiteKeysExistInEveryLanguage(t *testing.T) {
	need := []string{
		"nav.site", "site.tab_real", "site.tab_templates", "site.tab_fake",
		"tpl.title", "tpl.add", "tpl.details", "tpl.builtin",
		"rs.title", "rs.turn_on", "rs.turn_off", "rs.mode_inherit",
		"rs.mode_custom", "rs.mode_off", "rs.errors", "rs.template",
	}
	for _, lang := range []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"} {
		js := asset(t, "js/i18n/"+lang+".js")
		for _, key := range need {
			parts := strings.SplitN(key, ".", 2)
			// Both formats: multi-line `  key: "v",` and minified `key:"v"`.
			re := regexp.MustCompile(`(^|[\s,{])` + parts[1] + `\s*:\s*"`)
			sec := sectionOf(js, parts[0])
			if sec == "" {
				t.Errorf("%s: no %q section at all", lang, parts[0])
				break
			}
			if !re.MatchString(sec) {
				t.Errorf("%s is missing %s", lang, key)
			}
		}
	}
}

// sectionOf returns the body of a top-level i18n section in either format.
func sectionOf(js, name string) string {
	for _, anchor := range []string{"\n  " + name + ": {", "," + name + ":{"} {
		i := strings.Index(js, anchor)
		if i < 0 {
			continue
		}
		start := i + len(anchor)
		depth := 1
		for j := start; j < len(js); j++ {
			switch js[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return js[start:j]
				}
			}
		}
	}
	return ""
}
