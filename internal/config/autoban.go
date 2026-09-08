package config

// Automatic banning (fail2ban, in-process).
//
// The honeypot identifies scanners; this decides what happens to one that
// keeps coming back. It watches nginx's own logs, counts offences per
// address inside a rolling window, and bans an address that crosses the
// threshold.
//
// Why not fail2ban itself: it needs iptables and a daemon, and an iptables
// DROP is the single most conspicuous thing a server can do — a probe that
// times out rather than answers is precisely the fingerprint that gets an
// address filtered in Iran. Bans here are enforced INSIDE nginx, by
// generating a map of banned addresses, so a banned client still gets a
// perfectly ordinary "404 not found". The server never looks like it is
// filtering anything.
//
// Everything is configurable because the right numbers depend entirely on
// what is being run: a public website wants a forgiving threshold, a
// private VPN endpoint wants a strict one.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// What a ban does to a matching request.
const (
	// BanActionNotFound answers 404 — indistinguishable from any empty
	// path. The default, and the quiet one.
	BanActionNotFound = "notfound"

	// BanActionThrottle lets the client through at a crawl. Useful when
	// false positives would be expensive and you would rather annoy a
	// scanner than lock out a customer on a shared address.
	BanActionThrottle = "throttle"

	// BanActionForbidden answers 403. Honest and explicit, and the mode
	// that makes the server look like it filters.
	BanActionForbidden = "forbidden"
)

// NormalizeBanAction maps input onto a known action, defaulting to the
// quiet one. An unrecognised value must never make the server louder than
// the operator asked for.
func NormalizeBanAction(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case BanActionThrottle, "slow", "limit":
		return BanActionThrottle
	case BanActionForbidden, "deny", "403", "block":
		return BanActionForbidden
	}
	return BanActionNotFound
}

// AutoBanRule is one trigger. Rules are independent: an address can be
// banned by any of them.
type AutoBanRule struct {
	// Enabled lets a rule be switched off without losing its numbers.
	Enabled bool `json:"enabled"`
	// Hits is how many offences inside Window trigger a ban.
	Hits int `json:"hits"`
	// WindowMinutes is the rolling window the hits are counted in.
	WindowMinutes int `json:"window_minutes"`
	// BanMinutes is how long the ban lasts. Negative means permanent.
	BanMinutes int `json:"ban_minutes"`
}

// AutoBan is the whole feature's configuration.
type AutoBan struct {
	Enabled bool `json:"enabled,omitempty"`

	// Action is what a banned address receives.
	Action string `json:"action,omitempty"`

	// Honeypot bans addresses that trip the trap. The strongest signal
	// available: no legitimate client ever requests a bait path, so one
	// hit is already conclusive. The default is still 2, to survive a
	// browser that retries.
	Honeypot AutoBanRule `json:"honeypot"`

	// AuthFail bans addresses that fail to log into the PANEL. This is
	// the classic fail2ban case.
	AuthFail AutoBanRule `json:"auth_fail"`

	// NotFound bans addresses generating a flood of 404s — the signature
	// of a directory scanner walking a wordlist. Deliberately forgiving:
	// a broken page template can produce a burst of 404s from a real
	// visitor, so this needs a high threshold or it catches customers.
	NotFound AutoBanRule `json:"not_found"`

	// ErrorRate bans addresses producing a flood of 4xx/5xx of any kind.
	ErrorRate AutoBanRule `json:"error_rate"`

	// AllowIPs are never banned, whatever they do. An office address, a
	// monitoring probe, your own phone.
	AllowIPs []string `json:"allow_ips,omitempty"`

	// ThrottleRate is the per-minute budget in throttle mode.
	ThrottleRate int `json:"throttle_rate,omitempty"`

	// MaxBans caps the generated map. Each entry is a line in the nginx
	// config; an unbounded list would eventually slow nginx's startup and
	// bloat the file.
	MaxBans int `json:"max_bans,omitempty"`

	// LogBans records every ban and release to a file, so "who was
	// blocked, when, and why" survives a restart of the panel and can be
	// read from the Logs page months later. The in-memory list only shows
	// bans that are still ACTIVE; a four-hour ban that expired last night
	// has vanished from it, and that is exactly the one being asked about
	// when somebody complains they could not reach the site.
	LogBans bool `json:"log_bans,omitempty"`

	// Configured records that the form has been saved at least once, so
	// LogBans can default to ON the first time without ever overriding a
	// later decision to turn it off. Same reasoning as Honeypot.Configured.
	Configured bool `json:"configured,omitempty"`
}

// Defaults, chosen to be useful without being trigger-happy.
const (
	DefaultBanMinutes   = 240 // 4 hours
	DefaultThrottleRate = 2
	DefaultMaxBans      = 5000
	maxReasonableBans   = 20000
)

// DefaultAutoBan returns the shipped configuration. Off, but with numbers
// that make sense the moment it is switched on.
func DefaultAutoBan() AutoBan {
	return AutoBan{
		Enabled: false,
		Action:  BanActionNotFound,
		// One bait hit is already conclusive; two survives a retry.
		Honeypot: AutoBanRule{Enabled: true, Hits: 2, WindowMinutes: 60, BanMinutes: 240},
		// Five bad passwords in ten minutes is a person having a bad day
		// at four, and a script at five.
		AuthFail: AutoBanRule{Enabled: true, Hits: 5, WindowMinutes: 10, BanMinutes: 60},
		// High on purpose: a broken template can produce a burst of 404s
		// from a real browser.
		NotFound: AutoBanRule{Enabled: false, Hits: 60, WindowMinutes: 5, BanMinutes: 120},
		// Off by default: too blunt to enable without thinking.
		ErrorRate:    AutoBanRule{Enabled: false, Hits: 120, WindowMinutes: 5, BanMinutes: 60},
		ThrottleRate: DefaultThrottleRate,
		MaxBans:      DefaultMaxBans,
	}
}

// EffectiveAction returns the normalised action.
func (a AutoBan) EffectiveAction() string { return NormalizeBanAction(a.Action) }

// EffectiveThrottleRate returns the per-minute budget for a throttled ban.
func (a AutoBan) EffectiveThrottleRate() int {
	if a.ThrottleRate <= 0 {
		return DefaultThrottleRate
	}
	return a.ThrottleRate
}

// EffectiveMaxBans returns the cap on generated entries.
func (a AutoBan) EffectiveMaxBans() int {
	if a.MaxBans <= 0 {
		return DefaultMaxBans
	}
	if a.MaxBans > maxReasonableBans {
		return maxReasonableBans
	}
	return a.MaxBans
}

// Window returns a rule's window as a duration, with a sane floor.
func (r AutoBanRule) Window() time.Duration {
	if r.WindowMinutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(r.WindowMinutes) * time.Minute
}

// Duration returns how long a ban from this rule lasts. A negative
// BanMinutes means permanent, represented as a very long duration so the
// caller needs no special case.
func (r AutoBanRule) Duration() time.Duration {
	switch {
	case r.BanMinutes < 0:
		return 100 * 365 * 24 * time.Hour // effectively permanent
	case r.BanMinutes == 0:
		return DefaultBanMinutes * time.Minute
	default:
		return time.Duration(r.BanMinutes) * time.Minute
	}
}

// Permanent reports whether this rule bans forever.
func (r AutoBanRule) Permanent() bool { return r.BanMinutes < 0 }

// Threshold returns the hit count that triggers a ban, with a floor of 1.
func (r AutoBanRule) Threshold() int {
	if r.Hits < 1 {
		return 1
	}
	return r.Hits
}

// ValidateAutoBan checks operator input.
func ValidateAutoBan(a AutoBan) error {
	if !a.Enabled {
		return nil
	}
	rules := map[string]AutoBanRule{
		"honeypot": a.Honeypot, "auth_fail": a.AuthFail,
		"not_found": a.NotFound, "error_rate": a.ErrorRate,
	}
	names := make([]string, 0, len(rules))
	for n := range rules {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := rules[n]
		if !r.Enabled {
			continue
		}
		if r.Hits < 1 {
			return fmt.Errorf("the %s rule needs at least 1 hit to trigger", n)
		}
		if r.WindowMinutes < 1 {
			return fmt.Errorf("the %s rule needs a window of at least 1 minute", n)
		}
		if r.BanMinutes == 0 {
			continue // means "use the default"
		}
		if r.BanMinutes > 0 && r.BanMinutes < 1 {
			return fmt.Errorf("the %s rule's ban is too short to matter", n)
		}
	}
	if a.ThrottleRate < 0 {
		return fmt.Errorf("the throttle budget cannot be negative")
	}
	// At least one rule has to be able to fire, or the feature is on but
	// inert — which looks like protection and is not.
	if !a.Honeypot.Enabled && !a.AuthFail.Enabled &&
		!a.NotFound.Enabled && !a.ErrorRate.Enabled {
		return fmt.Errorf("automatic banning is on but every rule is off, so nothing can ever be banned")
	}
	return nil
}

// AnyRuleEnabled reports whether the feature can actually do anything.
func (a AutoBan) AnyRuleEnabled() bool {
	return a.Honeypot.Enabled || a.AuthFail.Enabled ||
		a.NotFound.Enabled || a.ErrorRate.Enabled
}
