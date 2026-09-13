package config

// The closed sets behind the UI preferences.
//
// Both of these are closed because the FRONT END makes them closed: every
// colour in the panel comes from a custom property defined under a
// [data-theme="..."] selector, and every string comes from a dictionary
// keyed by language. A value outside the set is not "unsupported but
// harmless" — it makes the panel unusable.
//
// An unknown theme was applied to the html element regardless, no CSS rule
// matched it, and every variable resolved to nothing: text rendered
// invisible, borders vanished, and an SVG fill computed to black. Found by
// storing "dark", which sounds like it ought to exist and does not.

import "strings"

// Themes are the values [data-theme] can take. Kept in the same order the
// settings page lists them.
var Themes = []string{
	"midnight", "aurora", "sunset", "forest", "light", "high-contrast",
}

// Languages are the dictionaries shipped in internal/web/static/js/i18n.
var Languages = []string{
	"fa", "en", "ar", "tr", "zh", "ja", "ko", "pt", "es", "ru",
}

// DefaultTheme is what an unconfigured install gets. It matches the value
// hard-coded in index.html, so the page does not flash a different palette
// before the stored preference is applied.
const DefaultTheme = "midnight"

// ValidTheme reports whether a theme exists.
func ValidTheme(v string) bool { return inSet(v, Themes) }

// ValidLanguage reports whether a dictionary exists.
func ValidLanguage(v string) bool { return inSet(v, Languages) }

func inSet(v string, set []string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// EffectiveTheme returns the theme to apply, falling back to the default
// for an empty or unknown stored value.
//
// Belt and braces alongside the API validation: a config file edited by
// hand, or one written by an older build before the check existed, must
// not be able to produce an unstyled panel.
func (u UISettings) EffectiveTheme() string {
	if ValidTheme(u.Theme) {
		return strings.ToLower(strings.TrimSpace(u.Theme))
	}
	return DefaultTheme
}

// EffectiveLanguage returns the language to use.
func (u UISettings) EffectiveLanguage() string {
	if ValidLanguage(u.Language) {
		return strings.ToLower(strings.TrimSpace(u.Language))
	}
	return "fa"
}
