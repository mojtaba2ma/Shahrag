package config

// The honeypot (تلهٔ عسل).
//
// Scanners walk the internet asking every address for /wp-login.php,
// /.env, /phpmyadmin and a few hundred other well-known paths. A real
// visitor never asks for any of them. So a request for one is a free,
// zero-false-positive signal that the client is hostile — no fingerprinting,
// no heuristics, no chance of catching a customer.
//
// What is done with that signal matters enormously here, and the default is
// deliberately NOT a block.
//
// Iranian network filtering targets servers whose addresses are observed
// refusing connections: a server that answers a probe with a hard 403, or
// worse drops the packet, is exactly the fingerprint that gets an IP
// blackholed. So the default response is to SLOW the offender down with
// nginx's own limit_req, which looks to an observer like an ordinary busy
// server, and to serve the same neutral page everything else gets. The
// scanner gives up because it is not worth its time; nobody outside learns
// that anything was detected.
//
// A hard block is available for operators who want it and know their
// exposure, and can be set to a few hours or to permanent. It is never the
// default.

import (
	"fmt"
	"sort"
	"strings"
)

// Honeypot response modes.
const (
	// HoneypotThrottle slows an offender to a crawl using limit_req. This
	// is the default: it costs a scanner its time budget while looking,
	// from outside, exactly like a server under load.
	HoneypotThrottle = "throttle"

	// HoneypotDecoy serves a plausible but empty page and throttles. Some
	// scanners record a 200 and move on rather than escalating.
	HoneypotDecoy = "decoy"

	// HoneypotBlock refuses the request outright with 403. Honest about
	// the trade-off: this is the mode that makes the server look like it
	// is filtering, which is precisely what draws attention in some
	// networks. It also only refuses the request, not the client — a
	// lasting per-address ban needs state nginx does not keep, which is
	// the next feature and reads this trap's log.
	HoneypotBlock = "block"
)

// NormalizeHoneypotMode maps stored or user input onto a known mode.
// Anything unrecognised becomes the throttle default rather than the
// block: an unknown value must never silently make the server more
// conspicuous than the operator asked for.
func NormalizeHoneypotMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case HoneypotDecoy, "fake", "page":
		return HoneypotDecoy
	case HoneypotBlock, "deny", "ban":
		return HoneypotBlock
	}
	return HoneypotThrottle
}

// Honeypot configures the trap. Off unless explicitly enabled, so an
// existing installation generates byte-identical output until someone asks
// for it.
type Honeypot struct {
	Enabled bool `json:"enabled,omitempty"`

	// Mode is what happens to a client that trips the trap.
	Mode string `json:"mode,omitempty"`

	// Paths are the bait. Empty means DefaultHoneypotPaths, which is what
	// almost everyone should use — the list is only worth editing if you
	// genuinely serve one of those paths.
	Paths []string `json:"paths,omitempty"`

	// ExtraPaths are added to the defaults rather than replacing them, so
	// an operator can bait their own stack without having to restate the
	// whole list.
	ExtraPaths []string `json:"extra_paths,omitempty"`

	// AllowPaths are carved back OUT of the trap. If a site genuinely
	// serves /wp-admin, it must not trap its own users.
	AllowPaths []string `json:"allow_paths,omitempty"`

	// AllowIPs never trip the trap: an office address, a monitoring
	// system, a security scanner you run yourself.
	AllowIPs []string `json:"allow_ips,omitempty"`

	// RatePerMinute is the request budget a tripped client keeps in
	// throttle mode. Deliberately not zero: a scanner that gets nothing at
	// all knows it has been detected, whereas one crawling at two requests
	// a minute simply concludes the server is slow.
	RatePerMinute int `json:"rate_per_minute,omitempty"`

	// LogHits records offenders so the panel can show who tripped it.
	LogHits bool `json:"log_hits,omitempty"`

	// Configured records that an operator has saved this form at least
	// once.
	//
	// It exists to make one default work correctly. Recording who tripped
	// the trap should be ON the first time the trap is switched on —
	// enabling a detector and not keeping its findings is not a useful
	// state, and nobody thinks to tick a second box. But LogHits is a
	// bool, so a config that predates this feature is indistinguishable
	// from one where the operator deliberately turned logging OFF, and
	// forcing it back on every time would override a real decision.
	//
	// This flag separates the two: while it is false the API reports the
	// recommended default; once the form has been saved, whatever the
	// operator chose is what stands, for ever.
	Configured bool `json:"configured,omitempty"`
}

// Defaults.
const (
	// DefaultHoneypotRate is the throttled budget, in requests per minute.
	DefaultHoneypotRate = 2

	// MaxHoneypotPaths bounds how many locations the generator will emit.
	// Each one is an nginx location block; a config with thousands would
	// slow nginx's own startup and make the generated file unreadable.
	MaxHoneypotPaths = 200
)

// DefaultHoneypotPaths is the bait list.
//
// Chosen on one rule: a legitimate visitor to a proxy or a VPN panel never
// requests these. Anything a real site might plausibly serve — /admin,
// /login, /api — is deliberately ABSENT, because a false positive here
// punishes a customer.
var DefaultHoneypotPaths = []string{
	// WordPress: the single most scanned software on the internet.
	"/wp-login.php", "/wp-admin", "/wp-config.php", "/wp-content/plugins",
	"/xmlrpc.php",
	// Leaked credentials and version control.
	"/.env", "/.env.local", "/.git/config", "/.git/HEAD", "/.svn/entries",
	"/.aws/credentials", "/.ssh/id_rsa", "/config.json.bak", "/.DS_Store",
	// Database and admin consoles.
	"/phpmyadmin", "/pma", "/adminer.php", "/myadmin", "/mysql",
	// Application frameworks with known probe paths.
	"/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php",
	"/cgi-bin/luci", "/boaform/admin/formLogin", "/HNAP1",
	"/solr/admin/info/system", "/actuator/env", "/api/jsonws/invoke",
	// Shells left behind by earlier compromises.
	"/shell.php", "/c99.php", "/r57.php", "/wso.php", "/alfa.php",
	"/uploads/shell.php",
	// Backups someone forgot to delete.
	"/backup.sql", "/database.sql", "/dump.sql", "/backup.zip", "/www.zip",
	// Panels and remote management.
	"/cpanel", "/whm", "/webmail", "/roundcube", "/plesk",
	"/manager/html", "/jenkins/script", "/_ignition/execute-solution",
	// Cloud metadata, probed through a misconfigured proxy.
	"/latest/meta-data", "/computeMetadata/v1",
}

// HoneypotEnabled reports whether the trap should be generated.
func (h Honeypot) HoneypotEnabled() bool { return h.Enabled }

// EffectiveMode returns the normalised response mode.
func (h Honeypot) EffectiveMode() string { return NormalizeHoneypotMode(h.Mode) }

// EffectiveRate returns the throttled request budget per minute.
func (h Honeypot) EffectiveRate() int {
	if h.RatePerMinute <= 0 {
		return DefaultHoneypotRate
	}
	return h.RatePerMinute
}

// EffectivePaths returns the bait list actually used: the configured list
// (or the defaults), plus any extras, minus the allow list, de-duplicated
// and sorted so the generated file is stable across runs.
func (h Honeypot) EffectivePaths() []string {
	base := h.Paths
	if len(base) == 0 {
		base = DefaultHoneypotPaths
	}
	allow := map[string]bool{}
	for _, p := range h.AllowPaths {
		if n := NormalizeHoneypotPath(p); n != "" {
			allow[strings.ToLower(n)] = true
		}
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(h.ExtraPaths))
	for _, p := range append(append([]string{}, base...), h.ExtraPaths...) {
		n := NormalizeHoneypotPath(p)
		if n == "" {
			continue
		}
		k := strings.ToLower(n)
		if allow[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	sort.Strings(out)
	if len(out) > MaxHoneypotPaths {
		out = out[:MaxHoneypotPaths]
	}
	return out
}

// NormalizeHoneypotPath cleans one bait entry.
//
// The value is written into a generated nginx location, so anything that
// could terminate a directive or introduce a variable is refused outright
// rather than escaped: a bait path is operator input, and there is no
// legitimate reason for one to contain a brace or a dollar sign.
func NormalizeHoneypotPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Strip a query string: nginx matches locations on the path alone, so
	// keeping "?x=1" would produce a location that can never match.
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	// Collapse duplicate slashes, which nginx normalises away anyway.
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if p == "/" {
		// Trapping the site root would trap every visitor.
		return ""
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	if strings.ContainsAny(p, "$'\"{};\\ \t\r\n") {
		return ""
	}
	return p
}

// ValidateHoneypot checks operator input and returns a readable error.
func ValidateHoneypot(h Honeypot) error {
	if !h.Enabled {
		return nil
	}
	for _, p := range append(append([]string{}, h.Paths...), h.ExtraPaths...) {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if NormalizeHoneypotPath(p) == "" {
			return fmt.Errorf("%q is not a usable trap path", p)
		}
	}
	for _, p := range h.AllowPaths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if NormalizeHoneypotPath(p) == "" {
			return fmt.Errorf("%q is not a usable exception path", p)
		}
	}
	if len(h.EffectivePaths()) == 0 {
		return fmt.Errorf("the trap has no paths left once the exceptions are applied")
	}
	if h.RatePerMinute < 0 {
		return fmt.Errorf("the request budget cannot be negative")
	}
	return nil
}

// HoneypotConflicts reports trap paths that a real service would otherwise
// serve.
//
// This matters: a trap silently shadowing a working path would break the
// site in a way that is very hard to diagnose, because the location looks
// fine in the panel and the traffic simply never arrives.
func HoneypotConflicts(c *Config) []string {
	if c == nil || !c.Honeypot.Enabled {
		return nil
	}
	bait := map[string]bool{}
	for _, p := range c.Honeypot.EffectivePaths() {
		bait[strings.ToLower(p)] = true
	}
	var out []string
	names := make([]string, 0, len(c.Services))
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc := c.Services[n]
		if !svc.IsEnabled() {
			continue
		}
		p := NormalizeHoneypotPath("/" + NormalizePath(svc.Path))
		if p == "" {
			continue
		}
		if bait[strings.ToLower(p)] {
			out = append(out, fmt.Sprintf("%s (%s)", p, n))
		}
	}
	return out
}
