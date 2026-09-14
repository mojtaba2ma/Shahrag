package config

// The real site (سایت واقعی).
//
// What problem this solves
// ────────────────────────
// A proxy server whose only public face is a loading spinner is a proxy
// server that announces itself. Anyone who visits the address — a scanner, a
// curious ISP, an automated classifier — sees a page that exists for no
// reason other than to occupy a domain. The defence is to serve an actual
// website: several pages, internal links, a stylesheet, error pages that
// match, robots.txt, a sitemap. Nothing about that costs the proxy anything,
// because nginx serves the files from disk and the proxied services keep
// their own locations.
//
// Why it is PER DOMAIN
// ────────────────────
// Operators run several domains through one panel and they are not
// interchangeable: one is the public-looking front, another is the panel's
// own host, a third is a passthrough used only for TLS. Forcing them to
// share one site would either expose the wrong thing or force the operator
// to run several panels. So every domain carries its own RealSite block:
// its own template, its own text, its own error pages, its own switch.
//
// Why it is OFF by default
// ────────────────────────
// Enabling it changes what the world sees at a live address. That is never
// something a configuration upgrade should do by itself. `Enabled` is
// deliberately the positive field here (unlike Service.Disabled): an old
// config has the zero value false and therefore no real site, which is
// exactly the previous behaviour, byte for byte.
//
// Inheritance
// ───────────
// Most people want one site across every domain, and a few want a different
// one per domain. Both are one model: a Defaults block at the top level and
// a per-domain block that can either inherit it or override it. `Mode`
// decides which: "inherit" (the default), "custom", or "off".

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Real-site modes for a domain.
const (
	// RealSiteInherit uses the panel-wide defaults. This is what a domain
	// gets when nothing was said about it.
	RealSiteInherit = "inherit"
	// RealSiteCustom uses this domain's own template and text.
	RealSiteCustom = "custom"
	// RealSiteOff serves no real site on this domain even when the
	// panel-wide default is on — the escape hatch for the domain that
	// hosts the panel itself.
	RealSiteOff = "off"
)

// ErrorPageModes.
const (
	// ErrorPagesTemplate serves the chosen template's error pages.
	ErrorPagesTemplate = "template"
	// ErrorPagesCustom serves a different template's error pages, or
	// hand-written HTML per status.
	ErrorPagesCustom = "custom"
	// ErrorPagesOff leaves nginx's built-in responses alone. Useful when
	// something upstream (Cloudflare) already supplies them.
	ErrorPagesOff = "off"
)

// ErrorPage is the handling of one status code.
type ErrorPage struct {
	// Disabled turns this single status back to nginx's default while
	// leaving the rest of the set in place. Disabled rather than Enabled
	// so the zero value means "use the set", which is what someone who
	// never opened this screen wants.
	Disabled bool `json:"disabled,omitempty"`
	// Template overrides which template supplies this page. Empty means
	// the set's template.
	Template string `json:"template,omitempty"`
	// HTML, when non-empty, is served verbatim for this status and beats
	// both templates.
	HTML string `json:"html,omitempty"`
}

// RealSiteContent is the text poured into a template's placeholders. Every
// field is optional; an empty one falls back to a neutral default so a
// half-filled form still produces a complete page rather than a page with
// {{HERO_TITLE}} printed on it.
type RealSiteContent struct {
	SiteName string `json:"site_name,omitempty"`
	Tagline  string `json:"tagline,omitempty"`
	Language string `json:"language,omitempty"` // BCP-47, decides lang= and dir=
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Address  string `json:"address,omitempty"`
	SiteURL  string `json:"site_url,omitempty"`

	HeroTitle string `json:"hero_title,omitempty"`
	HeroText  string `json:"hero_text,omitempty"`

	// Extra is any other placeholder the operator wants to set, keyed by
	// the placeholder name without braces. Lets a downloaded template
	// define its own fields without the panel needing to know them.
	Extra map[string]string `json:"extra,omitempty"`

	// NoIndex adds noindex/nofollow and a Disallow to robots.txt. On by
	// default for a decoy: the point is to look real to a visitor, not to
	// get the address into a search index where it is easy to enumerate.
	NoIndex bool `json:"noindex,omitempty"`
}

// RealSite is one domain's real-site configuration, and also the shape of the
// panel-wide defaults.
type RealSite struct {
	// Enabled is the switch. Absent = off.
	Enabled bool `json:"enabled,omitempty"`
	// Mode is inherit/custom/off. Empty means inherit.
	Mode string `json:"mode,omitempty"`

	// Template is the site template id.
	Template string `json:"template,omitempty"`

	Content RealSiteContent `json:"content,omitempty"`

	// ErrorMode is template/custom/off. Empty means template.
	ErrorMode string `json:"error_mode,omitempty"`
	// ErrorTemplate supplies error pages when ErrorMode is custom.
	ErrorTemplate string `json:"error_template,omitempty"`
	// ErrorPages is the per-status overrides, keyed by code ("404").
	ErrorPages map[string]ErrorPage `json:"error_pages,omitempty"`

	// Root, when set, serves the operator's OWN directory instead of a
	// rendered template. For someone who already has a website and just
	// wants nginx pointed at it.
	Root string `json:"root,omitempty"`

	// ExtraConfig is raw nginx directives inserted into this domain's
	// real-site location. Validated by `nginx -t` like everything else;
	// a bad line is caught before it reaches a running server.
	ExtraConfig string `json:"extra_config,omitempty"`

	// Index is the index file list. Empty means "index.html".
	Index string `json:"index,omitempty"`

	// CacheAssets adds an expires header for static assets. Off by
	// default because a decoy that caches for a year looks odd when its
	// text changes; on, it saves real bandwidth.
	CacheAssets bool `json:"cache_assets,omitempty"`

	// Configured records that a human has been through this screen, so
	// the UI can tell "never set up" from "set up and turned off".
	Configured bool `json:"configured,omitempty"`
}

// RealSiteSettings is the panel-wide block.
type RealSiteSettings struct {
	// Defaults is what an inheriting domain gets.
	Defaults RealSite `json:"defaults,omitempty"`

	// Repo is where downloadable templates come from, "owner/name".
	Repo string `json:"repo,omitempty"`
	// Ref is the branch.
	Ref string `json:"ref,omitempty"`
	// Mirror is an optional operator-hosted base URL tried before the
	// public mirrors — for someone inside a filtered network who keeps a
	// copy somewhere reachable.
	Mirror string `json:"mirror,omitempty"`
	// CacheHours is the catalogue cache lifetime. 0 means the default.
	CacheHours int `json:"cache_hours,omitempty"`

	// SitesDir is where rendered sites are written. Empty means
	// /var/www/shahrag-sites.
	SitesDir string `json:"sites_dir,omitempty"`

	Configured bool `json:"configured,omitempty"`
}

// DefaultSitesDir is where a rendered real site is written. Deliberately not
// the fake-site directory: the two features coexist, and a domain with no
// real site must keep serving exactly what it served before.
const DefaultSitesDir = "/var/www/shahrag-sites"

// NormalizeRealSiteMode maps input onto a known mode, defaulting to inherit.
func NormalizeRealSiteMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case RealSiteCustom:
		return RealSiteCustom
	case RealSiteOff, "disabled", "none":
		return RealSiteOff
	}
	return RealSiteInherit
}

// NormalizeErrorMode maps input onto a known error mode.
func NormalizeErrorMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case ErrorPagesCustom:
		return ErrorPagesCustom
	case ErrorPagesOff, "none", "disabled":
		return ErrorPagesOff
	}
	return ErrorPagesTemplate
}

// SitesDir returns the rendered-site root.
func (r RealSiteSettings) SitesDirOrDefault() string {
	if d := strings.TrimSpace(r.SitesDir); d != "" {
		return d
	}
	return DefaultSitesDir
}

// EffectiveRealSite resolves what a domain actually serves, applying
// inheritance. The second return is false when this domain serves no real
// site at all — which is the case for every domain on an untouched install.
//
// Resolution order, and why:
//  1. mode "off" wins outright. An operator who switched one domain off must
//     not have it switched back on by a change to the defaults.
//  2. mode "custom" uses the domain's own block, but still falls back to the
//     defaults for any field it left empty, so overriding the template does
//     not force re-typing the address and phone number.
//  3. mode "inherit" uses the defaults, and the defaults' Enabled decides.
func (c *Config) EffectiveRealSite(domain string) (RealSite, bool) {
	def := c.RealSites.Defaults
	site, has := c.DomainSites[strings.ToLower(strings.TrimSpace(domain))]
	if !has {
		if !def.Enabled {
			return RealSite{}, false
		}
		out := def
		out.Mode = RealSiteInherit
		return out, true
	}
	switch NormalizeRealSiteMode(site.Mode) {
	case RealSiteOff:
		return RealSite{}, false
	case RealSiteCustom:
		if !site.Enabled {
			return RealSite{}, false
		}
		return mergeRealSite(def, site), true
	default: // inherit
		// An inheriting domain may still carry its own Enabled flag,
		// which lets someone turn the site on for ONE domain without
		// touching the defaults at all — the common first step, and the
		// one the toggle button on the list performs.
		if !site.Enabled && !def.Enabled {
			return RealSite{}, false
		}
		if !site.Enabled && def.Enabled {
			out := def
			out.Mode = RealSiteInherit
			return out, true
		}
		// "Inherit" means fill in what this domain did NOT say, not
		// throw away what it did. Returning the defaults wholesale here
		// silently discarded the template the toggle had just chosen,
		// so a domain switched on from the list rendered nothing and
		// quietly kept serving the fake page — the feature appeared to
		// do nothing at all. Found by the browser sweep, not by the
		// unit test, which only checked that the domain was "active".
		out := mergeRealSite(def, site)
		out.Enabled = true
		out.Mode = RealSiteInherit
		return out, true
	}
}

// mergeRealSite fills empty fields of over from base.
func mergeRealSite(base, over RealSite) RealSite {
	out := over
	if strings.TrimSpace(out.Template) == "" {
		out.Template = base.Template
	}
	if strings.TrimSpace(out.ErrorMode) == "" {
		out.ErrorMode = base.ErrorMode
	}
	if strings.TrimSpace(out.ErrorTemplate) == "" {
		out.ErrorTemplate = base.ErrorTemplate
	}
	if strings.TrimSpace(out.Index) == "" {
		out.Index = base.Index
	}
	if len(out.ErrorPages) == 0 {
		out.ErrorPages = base.ErrorPages
	}
	out.Content = mergeContent(base.Content, out.Content)
	return out
}

func mergeContent(base, over RealSiteContent) RealSiteContent {
	out := over
	str := func(a *string, b string) {
		if strings.TrimSpace(*a) == "" {
			*a = b
		}
	}
	str(&out.SiteName, base.SiteName)
	str(&out.Tagline, base.Tagline)
	str(&out.Language, base.Language)
	str(&out.Email, base.Email)
	str(&out.Phone, base.Phone)
	str(&out.Address, base.Address)
	str(&out.SiteURL, base.SiteURL)
	str(&out.HeroTitle, base.HeroTitle)
	str(&out.HeroText, base.HeroText)
	if len(base.Extra) > 0 {
		merged := make(map[string]string, len(base.Extra)+len(out.Extra))
		for k, v := range base.Extra {
			merged[k] = v
		}
		for k, v := range out.Extra {
			if strings.TrimSpace(v) != "" {
				merged[k] = v
			}
		}
		out.Extra = merged
	}
	if !out.NoIndex {
		out.NoIndex = base.NoIndex
	}
	return out
}

// AnyRealSite reports whether any domain serves a real site. The generator
// uses it to skip the whole feature — and write byte-identical output to a
// version that never had it — when nobody has turned it on.
func (c *Config) AnyRealSite() bool {
	if c.RealSites.Defaults.Enabled {
		// Only true if at least one domain is not explicitly off.
		for name := range c.Domains {
			if _, ok := c.EffectiveRealSite(name); ok {
				return true
			}
		}
		return false
	}
	for name := range c.DomainSites {
		if _, ok := c.EffectiveRealSite(name); ok {
			return true
		}
	}
	return false
}

// RealSiteDomains lists, sorted, the domains that serve a real site.
func (c *Config) RealSiteDomains() []string {
	var out []string
	for name := range c.Domains {
		if _, ok := c.EffectiveRealSite(name); ok {
			out = append(out, strings.ToLower(name))
		}
	}
	sort.Strings(out)
	return out
}

// validCode matches a three-digit HTTP status.
var validCode = regexp.MustCompile(`^[1-5][0-9][0-9]$`)

// ValidErrorCode reports whether a status may have a page. nginx's
// error_page only makes sense for 3xx-5xx plus a handful of 4xx, and a
// nonsense code would be written straight into the config.
func ValidErrorCode(code string) bool {
	code = strings.TrimSpace(code)
	if !validCode.MatchString(code) {
		return false
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		return false
	}
	// 1xx and 2xx are not errors and nginx rejects them here; 3xx is
	// allowed by nginx but a redirect page is not what this screen is
	// for, so the panel stops at 400.
	return n >= 400 && n <= 599
}

// ValidateRealSite checks a block before it is stored, so a bad value fails
// in the API with a clear message instead of in `nginx -t` with a cryptic
// one, or worse, at request time.
func ValidateRealSite(r RealSite) error {
	switch NormalizeRealSiteMode(r.Mode) {
	case RealSiteCustom, RealSiteOff, RealSiteInherit:
	default:
		return fmt.Errorf("unknown mode %q", r.Mode)
	}
	if root := strings.TrimSpace(r.Root); root != "" {
		if !strings.HasPrefix(root, "/") {
			return fmt.Errorf("root must be an absolute path")
		}
		if strings.Contains(root, "..") {
			return fmt.Errorf("root must not contain ..")
		}
		// A root of / or /etc would hand the whole filesystem to the
		// internet through a single misconfiguration.
		switch strings.TrimRight(root, "/") {
		case "", "/etc", "/root", "/home", "/var", "/usr", "/boot", "/proc", "/sys", "/dev":
			return fmt.Errorf("root %q is not a safe document root", root)
		}
	}
	if idx := strings.TrimSpace(r.Index); idx != "" {
		for _, f := range strings.Fields(idx) {
			if strings.ContainsAny(f, "/;{} \t") || strings.Contains(f, "..") {
				return fmt.Errorf("index file %q is not a plain file name", f)
			}
		}
	}
	for code, ep := range r.ErrorPages {
		if !ValidErrorCode(code) {
			return fmt.Errorf("%q is not a status code this page can handle (400–599)", code)
		}
		if len(ep.HTML) > 256*1024 {
			return fmt.Errorf("custom HTML for %s is larger than 256 KB", code)
		}
	}
	if len(r.ExtraConfig) > 64*1024 {
		return fmt.Errorf("extra nginx configuration is larger than 64 KB")
	}
	// Directives that would escape the location and reconfigure the
	// server, or hand out arbitrary files. nginx would accept them; the
	// operator almost certainly did not mean them, and a template
	// downloaded from a repository must never be able to suggest one.
	for _, line := range strings.Split(r.ExtraConfig, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		word := l
		if i := strings.IndexAny(l, " \t;{"); i > 0 {
			word = l[:i]
		}
		switch strings.ToLower(word) {
		case "server", "http", "stream", "events", "include", "load_module", "user", "pid":
			return fmt.Errorf("%q is not allowed in the real-site block", word)
		}
	}
	if err := validateContent(r.Content); err != nil {
		return err
	}
	return nil
}

func validateContent(c RealSiteContent) error {
	// Length caps only. The content is escaped when rendered, so the
	// concern is a config file that grows without bound, not injection.
	lim := map[string]struct {
		v string
		n int
	}{
		"site name":  {c.SiteName, 120},
		"tagline":    {c.Tagline, 300},
		"email":      {c.Email, 254},
		"phone":      {c.Phone, 64},
		"address":    {c.Address, 300},
		"site url":   {c.SiteURL, 300},
		"hero title": {c.HeroTitle, 200},
		"hero text":  {c.HeroText, 1000},
	}
	names := make([]string, 0, len(lim))
	for k := range lim {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if len(lim[k].v) > lim[k].n {
			return fmt.Errorf("%s is longer than %d characters", k, lim[k].n)
		}
	}
	if len(c.Extra) > 64 {
		return fmt.Errorf("no more than 64 extra placeholders")
	}
	for k, v := range c.Extra {
		if len(k) > 48 || len(v) > 2000 {
			return fmt.Errorf("extra placeholder %q is too large", k)
		}
	}
	return nil
}

// RealSiteWarnings returns non-fatal notes for the UI: things that are
// allowed but that the operator probably wants to know about.
func RealSiteWarnings(c *Config) []string {
	var out []string
	if !c.RealSites.Defaults.Enabled && len(c.DomainSites) == 0 {
		return nil
	}
	for _, d := range c.RealSiteDomains() {
		site, _ := c.EffectiveRealSite(d)
		if strings.TrimSpace(site.Template) == "" && strings.TrimSpace(site.Root) == "" {
			out = append(out, fmt.Sprintf("%s: no template chosen, so the previous fake page is still served", d))
		}
		// A real site at / collides with a service that also claims /.
		for name, svc := range c.Services {
			if !svc.IsEnabled() || svc.Path != "/" {
				continue
			}
			for _, b := range svc.Bindings {
				if strings.EqualFold(b.Domain, d) {
					out = append(out, fmt.Sprintf(
						"%s: service %q serves / on this domain, so it wins and the real site is only reachable under its own paths",
						d, name))
					break
				}
			}
		}
	}
	sort.Strings(out)
	return out
}
