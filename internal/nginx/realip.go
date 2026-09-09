package nginx

// Keeping the real client address across the SNI hop.
//
// This is the bug underneath the r47 incident, and it is worth stating
// plainly because it is invisible until something depends on the address.
//
// When SNI routing is on, the stream module owns the public ports. It reads
// the TLS server name, then opens a NEW connection to the http block on
// 127.0.0.1:<reality_http_port> and copies bytes between the two. From the
// http block's point of view the client is 127.0.0.1 — always, for every
// visitor in the world.
//
// Everything in the panel that reasons about who a client is therefore
// breaks silently for traffic arriving through a split:
//
//	limit_conn per address     one shared counter for the whole internet
//	the automatic ban list     bans 127.0.0.1, which is exempt, so nothing
//	the honeypot allow-list    exempts everyone, or no one
//	per-service IP locks       either open to all or shut to all
//	the access log and stats   every visitor logged as 127.0.0.1
//
// The r47 symptom was the loudest of these: a per-address connection cap of
// 48, applied to a population that all looked like one address, refused
// service to everybody — including the panel, which is how the operator got
// locked out of the tool they needed to undo it.
//
// The fix is the PROXY protocol. The stream server announces the original
// peer in a short header; the http listener is told to expect it and to
// trust it from 127.0.0.1 only. Verified against a real nginx: without it
// the http block sees 127.0.0.1, with it the http block sees the actual
// client address.
//
// Why trusting only 127.0.0.1 matters: a PROXY header is plain text and
// asserts whatever it likes. Accepting one from a remote peer would let
// anybody claim any address — bypassing bans, IP locks and the honeypot
// allow-list in one line. set_real_ip_from is restricted to loopback, which
// is the only place our own stream module ever connects from.

import (
	"fmt"
	"strings"

	"shahrag/internal/config"
)

// RealIPEnabled reports whether the SNI hop needs address preservation.
//
// Only when the stream module is actually in the path. With Reality off,
// clients reach the http block directly and $remote_addr is already right.
func RealIPEnabled(c *config.Config) bool {
	return c != nil && c.Reality.Enabled && c.Reality.HTTPPort > 0
}

// realIPHopPort is the loopback port of the address-preserving listener.
//
// Derived from the http port rather than configured: one fewer thing for an
// operator to set, and it moves automatically if they change the http port.
// Offset by one and wrapped below 65535.
func realIPHopPort(c *config.Config) int {
	p := c.Reality.HTTPPort + 1
	if p > 65535 {
		p = c.Reality.HTTPPort - 1
	}
	return p
}

// StreamProxyProtocol returns the directive for a stream server block.
//
// Emitted for the fallback path only — the one that forwards to our own
// http port. A passthrough rule hands the connection to a real remote
// service that is not expecting a PROXY header, and injecting one there
// would corrupt the first bytes of the TLS handshake.
func StreamProxyProtocol(c *config.Config) string {
	if !RealIPEnabled(c) {
		return ""
	}
	return "    proxy_protocol on;\n"
}

// RealIPPrelude returns the http-level directives that read the header.
//
// set_real_ip_from is deliberately loopback-only: see the file comment.
func RealIPPrelude(c *config.Config) string {
	if !RealIPEnabled(c) {
		return ""
	}
	var b strings.Builder
	b.WriteString("# ── Real client address across the SNI hop ───────────────\n")
	b.WriteString("#    The stream module opens a fresh local connection to the\n")
	b.WriteString("#    port below, so without this every visitor would appear\n")
	b.WriteString("#    to come from 127.0.0.1 — breaking the ban list, the\n")
	b.WriteString("#    honeypot allow-list, per-service IP locks, the access\n")
	b.WriteString("#    log and any per-address limit.\n")
	b.WriteString("#\n")
	b.WriteString("#    Trusted from loopback ONLY. A PROXY header is plain text\n")
	b.WriteString("#    and asserts whatever it likes, so accepting one from a\n")
	b.WriteString("#    remote peer would let anybody claim any address.\n")
	b.WriteString("set_real_ip_from 127.0.0.1;\n")
	b.WriteString("set_real_ip_from ::1;\n")
	b.WriteString("real_ip_header proxy_protocol;\n\n")
	return b.String()
}

// ListenProxyProtocol returns the listen-directive parameter for a port.
//
// ONLY the internal fallback port carries it. A public port is reached
// directly by real clients, and a client does not send a PROXY header —
// adding the parameter there would make nginx reject every connection with
// "broken header", which is a total outage rather than a degraded one.
func ListenProxyProtocol(c *config.Config, port int) string {
	if !RealIPEnabled(c) {
		return ""
	}
	if port != c.Reality.HTTPPort {
		return ""
	}
	return " proxy_protocol"
}

// RealIPSummary describes the state for `shahrag doctor`.
func RealIPSummary(c *config.Config) string {
	if !RealIPEnabled(c) {
		return "not needed (SNI routing is off; clients reach nginx directly)"
	}
	return fmt.Sprintf("on: the stream module announces the client address to port %d",
		c.Reality.HTTPPort)
}

// anyPassthrough reports whether any enabled SNI rule forwards opaquely to
// a remote service.
//
// Only then is the header-stripping relay needed. Emitting it
// unconditionally would bind a port for nothing.
func anyPassthrough(c *config.Config) bool {
	if c == nil {
		return false
	}
	for _, svc := range c.Reality.Services {
		if svc.Disabled {
			continue
		}
		if svc.Target == config.PassthroughTarget {
			return true
		}
	}
	return false
}

// portHasPassthrough reports whether any enabled rule on this port
// forwards opaquely to a remote service.
//
// The PROXY header can only be added per LISTENER, and a passthrough must
// never receive one, so a port carrying even a single passthrough cannot
// have it. Ports that carry none get the real client address.
func portHasPassthrough(c *config.Config, port int) bool {
	if c == nil {
		return false
	}
	for _, svc := range c.Reality.Services {
		if svc.Disabled || svc.Target != config.PassthroughTarget {
			continue
		}
		for _, p := range svc.Ports {
			if p == port {
				return true
			}
		}
	}
	return false
}

// RealIPPortNote explains, in the generated file, why a particular port
// does or does not recover the client address.
//
// Written into the config rather than only into the panel because somebody
// reading this file in six months needs to know why two ports that look
// alike behave differently.
func RealIPPortNote(c *config.Config, port int) string {
	if !RealIPEnabled(c) {
		return ""
	}
	if portHasPassthrough(c, port) {
		return "# This port carries a passthrough, so no PROXY header is added:\n" +
			"# a remote service does not speak it and would see a corrupt TLS\n" +
			"# handshake. Traffic arriving here is seen by the http block as\n" +
			"# 127.0.0.1, so per-address rules cannot apply to it.\n"
	}
	return "# Announces the real client address to our own http block.\n"
}

// GenerateStreamForTest exposes the stream generator to an external
// harness. Test-only; the panel always goes through Generate().
func GenerateStreamForTest(c *config.Config, out string) error {
	return (&Generator{}).generateStreamTo(c, out)
}
