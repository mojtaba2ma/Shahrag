package nginx

// Enforcing bans inside nginx.
//
// The ban list is written as a `geo` block and consulted at the top of
// every server block. No iptables: a dropped packet makes a probe time out,
// and a server whose probes time out is exactly the fingerprint that gets
// an address filtered on some networks. A banned client here receives an
// ordinary 404 — the same answer any empty path gives — so nothing about
// the server looks unusual from outside.
//
// The list is data, not logic: regenerating after a ban rewrites one block
// and reloads. nginx reloads without dropping a connection, so banning
// somebody costs an established session nothing.

import (
	"fmt"
	"strings"

	"shahrag/internal/config"
)

const (
	banVar  = "$shg_banned"
	banZone = "shg_ban"
)

// BanListProvider supplies the current bans. An interface so the generator
// does not depend on the banner package, which would be a cycle.
type BanListProvider interface {
	BannedIPs(max int) []string
}

// banProvider is set by the server at startup. Nil in tests and in the CLI,
// where no engine is running, and the generator then emits nothing.
var banProvider BanListProvider

// SetBanProvider wires the running ban engine into the generator.
func SetBanProvider(p BanListProvider) { banProvider = p }

// AutoBanEnabled reports whether anything should be emitted.
func AutoBanEnabled(c *config.Config) bool {
	return c != nil && c.AutoBan.Enabled && c.AutoBan.AnyRuleEnabled()
}

// AutoBanPrelude emits the ban list and, in throttle mode, its zone.
func AutoBanPrelude(c *config.Config) string {
	if !AutoBanEnabled(c) {
		return ""
	}
	ab := c.AutoBan
	var ips []string
	if banProvider != nil {
		ips = banProvider.BannedIPs(ab.EffectiveMaxBans())
	}

	var b strings.Builder
	b.WriteString("# ── Automatic bans ────────────────────────────────────────\n")
	b.WriteString("#    Addresses that tripped a rule. Enforced in nginx, not in\n")
	b.WriteString("#    iptables: a dropped packet makes a probe time out, which is\n")
	b.WriteString("#    what makes a server look like it filters. A banned client\n")
	b.WriteString("#    just gets an ordinary not-found.\n")
	fmt.Fprintf(&b, "#    Currently banned: %d\n", len(ips))
	fmt.Fprintf(&b, "geo %s {\n", banVar)
	b.WriteString("    default 0;\n")
	// Loopback can never be banned, whatever the list says. Locking the
	// server out of itself would be unrecoverable from the panel.
	b.WriteString("    127.0.0.1/32 0;\n")
	b.WriteString("    ::1/128 0;\n")
	for _, ip := range ips {
		if strings.TrimSpace(ip) == "" {
			continue
		}
		fmt.Fprintf(&b, "    %s 1;\n", ip)
	}
	b.WriteString("}\n\n")

	if ab.EffectiveAction() == config.BanActionThrottle {
		// The key is empty for anyone not banned, and nginx skips
		// limit_req entirely when its key is empty — the documented way to
		// apply a limiter to a subset.
		fmt.Fprintf(&b, "map %s $shg_ban_key {\n", banVar)
		b.WriteString("    1 $binary_remote_addr;\n")
		b.WriteString("    default \"\";\n")
		b.WriteString("}\n")
		fmt.Fprintf(&b, "limit_req_zone $shg_ban_key zone=%s:1m rate=%dr/m;\n\n",
			banZone, ab.EffectiveThrottleRate())
	}
	return b.String()
}

// AutoBanServerGuard is emitted at the top of every server block.
//
// At server level rather than per location, so a ban covers everything the
// host serves — including paths added later, which a per-location guard
// would silently miss.
func AutoBanServerGuard(c *config.Config) string {
	if !AutoBanEnabled(c) {
		return ""
	}
	var b strings.Builder
	switch c.AutoBan.EffectiveAction() {
	case config.BanActionForbidden:
		fmt.Fprintf(&b, "    if (%s = 1) { return 403; }\n\n", banVar)
	case config.BanActionThrottle:
		// No `if` here: limit_req is applied unconditionally and the empty
		// key makes it a no-op for everyone who is not banned. That avoids
		// the "if is evil" footgun entirely.
		//
		// One honest limitation, measured against a real nginx: limit_req
		// runs in the PREACCESS phase, so it cannot slow a location whose
		// answer comes from the earlier REWRITE phase — a bare `return`.
		// Every location that actually serves a client (proxy_pass, the
		// fake site's try_files, the honeypot) runs later and is limited
		// normally; the only things it misses are the port-80 redirect and
		// the gate's challenge page, neither of which reaches a backend or
		// costs anything to serve. Choose "not found" or "forbidden" if a
		// ban must cover literally every response.
		fmt.Fprintf(&b, "    limit_req zone=%s burst=1 nodelay;\n", banZone)
		b.WriteString("    limit_req_status 404;\n\n")
	default:
		fmt.Fprintf(&b, "    if (%s = 1) { return 404; }\n\n", banVar)
	}
	return b.String()
}

// BanCount reports how many addresses are currently banned, for the panel.
func BanCount() int {
	if banProvider == nil {
		return 0
	}
	return len(banProvider.BannedIPs(0))
}
