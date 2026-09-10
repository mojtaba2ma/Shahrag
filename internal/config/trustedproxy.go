package config

// Trusted front proxies, and the crawlers that must never be banned.
//
// This exists because of a real incident: with the panel behind Cloudflare,
// the honeypot and the ban engine started recording Cloudflare and Google
// addresses. That is not a false positive in the detector — the detector is
// working perfectly. It is that the address it can see is the PROXY's, not
// the visitor's, so a single scanner arriving through Cloudflare gets
// Cloudflare banned, and Cloudflare is the whole site.
//
// There are two separate problems and they need two separate answers.
//
// ── 1. Seeing the real visitor ───────────────────────────────
//
// When a CDN sits in front, $remote_addr is the CDN's edge. The CDN passes
// the original address in a header (CF-Connecting-IP for Cloudflare,
// X-Forwarded-For generally), and nginx's realip module can substitute it —
// but ONLY for connections that genuinely came from the CDN.
//
// That restriction is the entire security of the mechanism. Verified
// against a real nginx: with set_real_ip_from covering the caller, a
// request carrying "CF-Connecting-IP: 1.2.3.4" is reported as coming from
// 1.2.3.4. If the trusted list were wide, anyone who could reach the origin
// directly could claim any address they liked and walk past every ban, IP
// lock and honeypot exemption in the panel. So the list is the CDN's
// published ranges and nothing else.
//
// ── 2. Not banning crawlers ──────────────────────────────────
//
// Googlebot does not request /wp-login.php, so it should never trip the
// trap. But it can accumulate 404s on a site that has moved pages, and the
// 404-flood rule would ban it. A banned Googlebot is a de-indexed site.
//
// The operator's own observation is the important one though: these ranges
// are PUBLIC. Anyone can rent a Google Cloud instance. So an allow-list
// entry has to be narrow enough to be worth having:
//
//   - Cloudflare's ranges are genuinely only Cloudflare's edge, because
//     nothing else is allowed to originate from them. Safe to trust.
//   - Googlebot's published crawler ranges are Google's crawling
//     infrastructure, not Google Cloud. A tenant cannot get an address in
//     them. Safe to trust.
//   - Google Cloud, AWS and the like are NOT included, and must not be:
//     that is where scanners actually come from.
//
// Every list ships disabled and is switched on individually, because the
// right answer depends on whether that proxy is actually in front of this
// server.

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// ProxyList is one named set of ranges.
type ProxyList struct {
	// ID is stable and machine-readable; the UI translates it.
	ID string `json:"id"`
	// Kind is what the ranges are for:
	//   "proxy"   — a CDN in front; trust its forwarded-for header
	//   "crawler" — a search engine; never ban it
	Kind string `json:"kind"`
	// Header is the request header carrying the original address, for a
	// proxy list. Empty for a crawler list.
	Header string `json:"header,omitempty"`
	// CIDRs are the published ranges.
	CIDRs []string `json:"cidrs"`
}

// Kinds.
const (
	ProxyKindProxy   = "proxy"
	ProxyKindCrawler = "crawler"
)

// Built-in list identifiers.
const (
	ProxyCloudflare  = "cloudflare"
	ProxyGooglebot   = "googlebot"
	ProxyBingbot     = "bingbot"
	ProxyUptimeRobot = "uptimerobot"
)

// BuiltinProxyLists are the ranges shipped with the panel.
//
// Deliberately SHORT. Every entry here is a set of addresses the panel will
// either trust to tell it who the client is, or refuse to ban — both of
// which are powers worth being stingy with. A list is only included when
// the operator cannot get an address in it by renting a server.
//
// Ranges are as published by each operator. They change rarely; the panel
// shows when its copy was taken and the operator can edit any of them.
var BuiltinProxyLists = []ProxyList{
	{
		ID:     ProxyCloudflare,
		Kind:   ProxyKindProxy,
		Header: "CF-Connecting-IP",
		// https://www.cloudflare.com/ips/ — taken 2026-09.
		CIDRs: []string{
			"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
			"103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
			"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
			"198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
			"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
			"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32",
			"2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29",
			"2c0f:f248::/32",
		},
	},
	{
		ID:   ProxyGooglebot,
		Kind: ProxyKindCrawler,
		// Google's CRAWLER ranges, from
		// https://developers.google.com/search/apis/ipranges/googlebot.json
		// — NOT Google Cloud. A tenant cannot obtain an address here,
		// which is what makes the entry safe; including Google Cloud
		// would hand every scanner on that platform a free pass.
		CIDRs: []string{
			"66.249.64.0/19", "66.249.64.0/27", "66.249.79.0/24",
			"34.100.182.96/28", "34.101.50.144/28", "34.118.66.0/28",
			"34.118.254.0/28", "34.126.178.96/28", "34.146.150.144/28",
			"34.147.110.144/28", "34.151.74.144/28", "34.152.50.64/28",
			"34.154.180.64/28", "34.155.98.32/28", "34.165.18.176/28",
			"34.175.160.64/28", "34.176.130.16/28", "34.22.85.0/27",
			"34.64.82.64/28", "34.65.242.112/28", "34.80.50.80/28",
			"34.88.194.0/28", "34.89.10.80/28", "34.89.198.80/28",
			"34.96.162.48/28", "35.247.243.240/28",
			"2001:4860:4801::/48",
		},
	},
	{
		ID:   ProxyBingbot,
		Kind: ProxyKindCrawler,
		// https://www.bing.com/toolbox/bingbot.json
		CIDRs: []string{
			"157.55.39.0/24", "207.46.13.0/24", "40.77.167.0/24",
			"13.66.139.0/24", "13.66.144.0/24", "52.167.144.0/24",
			"13.67.10.16/28", "13.69.66.240/28", "13.71.172.224/28",
			"139.217.52.0/28", "191.233.204.224/28", "20.36.108.32/28",
			"20.43.120.16/28", "40.79.131.208/28", "40.79.186.176/28",
			"52.231.148.0/28", "20.79.107.240/28", "51.105.67.0/28",
			"20.125.163.80/28", "40.77.188.0/22", "65.55.210.0/24",
			"199.30.24.0/23", "40.77.202.0/24",
		},
	},
	{
		ID:   ProxyUptimeRobot,
		Kind: ProxyKindCrawler,
		// A monitoring service. Included because a monitor that gets
		// banned produces a false outage alert at three in the morning,
		// and because its ranges are dedicated to monitoring.
		CIDRs: []string{
			"69.162.124.224/28", "63.143.42.240/28", "216.245.221.80/28",
			"208.115.199.16/28", "104.131.107.63/32", "122.248.234.23/32",
			"188.226.183.141/32", "216.144.250.150/32", "46.137.190.132/32",
		},
	},
}

// TrustedProxies is the operator's configuration.
type TrustedProxies struct {
	// Enabled ids, from BuiltinProxyLists or from Custom below. Nothing is
	// trusted unless it is named here: an absent list is off, and the
	// shipped default enables nothing at all.
	//
	// A map rather than a slice so a list that is later removed from the
	// build cannot linger as a mystery entry.
	Enabled map[string]bool `json:"enabled,omitempty"`

	// Custom lists the operator added — another CDN, an office range, a
	// monitoring system of their own.
	Custom []ProxyList `json:"custom,omitempty"`

	// ExtraTrustedCIDRs are added to the realip trust list directly.
	// For an operator whose CDN is not one of the built-ins.
	ExtraTrustedCIDRs []string `json:"extra_trusted_cidrs,omitempty"`

	// ExtraNeverBanCIDRs are never banned, whatever they do.
	ExtraNeverBanCIDRs []string `json:"extra_never_ban_cidrs,omitempty"`
}

// AllLists returns the built-ins plus the operator's own.
func (t TrustedProxies) AllLists() []ProxyList {
	out := make([]ProxyList, 0, len(BuiltinProxyLists)+len(t.Custom))
	out = append(out, BuiltinProxyLists...)
	out = append(out, t.Custom...)
	return out
}

// ListEnabled reports whether one list is switched on.
func (t TrustedProxies) ListEnabled(id string) bool {
	return t.Enabled != nil && t.Enabled[id]
}

// RealIPRanges returns the CIDRs whose forwarded-for header may be trusted,
// grouped by the header they use.
//
// Only "proxy" lists contribute. A crawler list says "do not ban these",
// which is a completely different power: trusting a crawler's headers would
// let anyone who can reach the origin claim to be Googlebot.
func (t TrustedProxies) RealIPRanges() map[string][]string {
	out := map[string][]string{}
	for _, l := range t.AllLists() {
		if l.Kind != ProxyKindProxy || !t.ListEnabled(l.ID) {
			continue
		}
		h := l.Header
		if h == "" {
			h = "X-Forwarded-For"
		}
		out[h] = append(out[h], l.CIDRs...)
	}
	for _, c := range t.ExtraTrustedCIDRs {
		if c = strings.TrimSpace(c); c != "" {
			out["X-Forwarded-For"] = append(out["X-Forwarded-For"], c)
		}
	}
	for h := range out {
		sort.Strings(out[h])
	}
	return out
}

// NeverBanRanges returns every CIDR that must not be banned.
//
// Both kinds contribute: a CDN's own edge must never be banned either, or
// banning one visitor takes the whole site off the air. That is exactly the
// failure being fixed — with Cloudflare in front, one scanner got
// Cloudflare's edge banned.
func (t TrustedProxies) NeverBanRanges() []string {
	var out []string
	for _, l := range t.AllLists() {
		if !t.ListEnabled(l.ID) {
			continue
		}
		out = append(out, l.CIDRs...)
	}
	for _, c := range t.ExtraNeverBanCIDRs {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// ValidateTrustedProxies checks operator input.
func ValidateTrustedProxies(t TrustedProxies) error {
	check := func(field string, list []string) error {
		for _, c := range list {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if _, _, err := net.ParseCIDR(c); err != nil && net.ParseIP(c) == nil {
				return fmt.Errorf("%s: %q is not a valid address or range", field, c)
			}
			// A wildcard in the TRUST list is catastrophic: it would let
			// anyone who reaches the origin claim any address at all,
			// bypassing every ban and IP lock. Refused outright rather
			// than warned about.
			if field == "trusted" && (c == "0.0.0.0/0" || c == "::/0") {
				return fmt.Errorf("trusting %s would let anybody claim any "+
					"address and bypass every ban and IP lock", c)
			}
		}
		return nil
	}
	if err := check("trusted", t.ExtraTrustedCIDRs); err != nil {
		return err
	}
	if err := check("never-ban", t.ExtraNeverBanCIDRs); err != nil {
		return err
	}
	for _, l := range t.Custom {
		if strings.TrimSpace(l.ID) == "" {
			return fmt.Errorf("a custom list needs a name")
		}
		if l.Kind != ProxyKindProxy && l.Kind != ProxyKindCrawler {
			return fmt.Errorf("list %q: kind must be %q or %q",
				l.ID, ProxyKindProxy, ProxyKindCrawler)
		}
		if err := check(l.Kind, l.CIDRs); err != nil {
			return err
		}
		if l.Kind == ProxyKindProxy && strings.ContainsAny(l.Header, "${}';\"\\ \t\r\n") {
			return fmt.Errorf("list %q: the header name contains an illegal character", l.ID)
		}
	}
	return nil
}

// TrustedProxyWarnings returns advice about the CURRENT combination.
func TrustedProxyWarnings(t TrustedProxies, c *Config) []string {
	var out []string
	if c == nil {
		return nil
	}
	anyProxy := false
	for _, l := range t.AllLists() {
		if l.Kind == ProxyKindProxy && t.ListEnabled(l.ID) {
			anyProxy = true
		}
	}
	// The situation that caused the incident: protection is on, a CDN is
	// almost certainly in front, and nothing is trusted — so every ban
	// lands on the CDN rather than on the visitor.
	if !anyProxy && (c.AutoBan.Enabled || c.Honeypot.Enabled) {
		out = append(out, "no_proxy_trusted")
	}
	if len(t.ExtraTrustedCIDRs) > 0 {
		out = append(out, "custom_trust_is_powerful")
	}
	return out
}
