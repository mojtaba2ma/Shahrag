package web

// The stats range picker.
//
// Twelve ranges were rendered as twelve tab buttons in the page header.
// They filled the header edge to edge and wrapped onto a second row on
// anything narrower than a laptop, pushing the first chart below the fold.
// They are now a single dropdown with a clock icon.
//
// These are static assertions on the embedded asset, which is what the
// browser actually receives; the real-Chromium sweep covers behaviour.

import (
	"regexp"
	"strings"
	"testing"
)

func statsJS(t *testing.T) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/js/pages/stats.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatsRangeIsADropdownNotTabs(t *testing.T) {
	js := statsJS(t)

	if strings.Contains(js, `id="range-tabs"`) {
		t.Error("the range tabs are still rendered — the header stays crowded")
	}
	if !strings.Contains(js, `id="range-select"`) {
		t.Fatal("no range dropdown was rendered")
	}
	if !strings.Contains(js, `class="range-pick"`) {
		t.Error("the dropdown is missing its wrapper, so it gets no icon and no styling")
	}
	if !strings.Contains(js, `Icons.svg("clock"`) {
		t.Error("the dropdown has no icon beside it")
	}
	// It must remain reachable without sight of the icon.
	if !strings.Contains(js, `aria-label="${t("stats.timeframe")}"`) {
		t.Error("the dropdown has no accessible label")
	}
}

// Every range that existed as a tab must still be selectable, or the change
// would be a silent feature removal.
func TestEveryRangeSurvivedTheRework(t *testing.T) {
	js := statsJS(t)
	for _, mins := range []string{"2", "5", "15", "30", "60", "360", "1440",
		"10080", "43200", "129600", "259200", "525600"} {
		if !strings.Contains(js, "["+mins+",") {
			t.Errorf("the %s-minute range was dropped", mins)
		}
	}
}

// The options are grouped, so twelve entries read as two ideas rather than
// twelve numbers.
func TestRangesAreGrouped(t *testing.T) {
	js := statsJS(t)
	if !strings.Contains(js, "<optgroup") {
		t.Error("the ranges are not grouped")
	}
	for _, key := range []string{"stats.range_recent", "stats.range_history"} {
		if !strings.Contains(js, key) {
			t.Errorf("group label %s is missing", key)
		}
	}
}

// The labels must come from the translation files, not be hard-coded
// English abbreviations like "6mo" that no other language would show.
func TestRangeLabelsAreTranslated(t *testing.T) {
	js := statsJS(t)
	for _, bad := range []string{`"6mo"`, `"1y"`, `"30d"`, `"7d"`, `"90d"`} {
		if strings.Contains(js, bad) {
			t.Errorf("the range label %s is hard-coded English", bad)
		}
	}
	for _, key := range []string{"stats.days_7", "stats.days_30", "stats.days_90",
		"stats.months_6", "stats.year_1"} {
		if !strings.Contains(js, key) {
			t.Errorf("%s is not used", key)
		}
	}
}

// Changing the range must still drive both loaders. Only reloading the
// charts and forgetting the resources was a real bug once before.
func TestChangingTheRangeReloadsEverything(t *testing.T) {
	js := statsJS(t)
	re := regexp.MustCompile(`sel\.onchange\s*=\s*\(\)\s*=>\s*\{[^}]*mins\s*=\s*\+sel\.value;[^}]*load\(\);[^}]*loadResources\(\);`)
	if !re.MatchString(js) {
		t.Error("selecting a range does not reload both the charts and the resources")
	}
}

// The dropdown must carry the styling that keeps it off the native widget
// look and inside the viewport on a phone.
func TestRangePickerIsStyled(t *testing.T) {
	b, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, sel := range []string{".range-pick", ".range-pick select",
		`[dir="rtl"] .range-pick`, ".range-pick:focus-within"} {
		if !strings.Contains(css, sel) {
			t.Errorf("%s has no rule — the picker is unstyled or breaks in RTL", sel)
		}
	}
}

// The label inside the picker must not be clipped.
//
// A 32px-tall select inherits the base rule's 11px block padding, which
// together with the line box is taller than the control; `overflow: clip`
// then sliced the Persian text in half and the label rendered as the top
// halves of its glyphs. Caught by looking at a real screenshot, not by any
// assertion that existed at the time.
func TestRangePickerTextIsNotClipped(t *testing.T) {
	b, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	i := strings.Index(css, ".range-pick select {")
	if i < 0 {
		t.Fatal("the picker select has no rule")
	}
	block := css[i : i+strings.Index(css[i:], "}")]
	if !strings.Contains(block, "padding-block: 0") {
		t.Error("the inherited vertical padding is not cleared — the label will be clipped")
	}
	if !strings.Contains(block, "line-height: normal") {
		t.Error("the line height is not reset, so a tall script can still overflow the control")
	}
}

// Every language must define the new keys. A missing key falls back to
// English, which reads as a bug in a Persian UI.
func TestRangeKeysExistInEveryLanguage(t *testing.T) {
	langs := []string{"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru"}
	keys := []string{"days_7", "days_30", "days_90", "months_6", "year_1",
		"range_recent", "range_history"}
	for _, l := range langs {
		b, err := staticFS.ReadFile("static/js/i18n/" + l + ".js")
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		s := string(b)
		for _, k := range keys {
			if !strings.Contains(s, k+":") {
				t.Errorf("%s is missing %s", l, k)
			}
		}
	}
}
