package nginx

// Generating the honeypot.
//
// Pure nginx, no Lua and no third-party module, for the same reason the bot
// shield is: the panel has to work on a stock distribution nginx that the
// operator did not compile.
//
// The shape is:
//
//	limit_req_zone $shg_hp_key zone=shg_hp:1m rate=2r/m;   (throttle mode)
//	geo $shg_hp_exempt { ... }                             (allow-list)
//	map "$shg_hp_exempt" $shg_hp_key { ... }               (exempt = no key)
//	location = /wp-login.php { ... }                       (one per bait path)
//
// The rate limiter is keyed on a variable that is EMPTY for exempt clients.
// nginx skips limit_req entirely when its key evaluates to empty, which is
// the documented way to carve an exception out of a zone — cleaner than a
// second zone and impossible to get subtly wrong.

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"shahrag/internal/config"
)

// Names used in the generated file. Prefixed like everything else the panel
// writes, so a human reading nginx.conf can tell where a directive came
// from and so nothing collides with an operator's own configuration.
var HoneypotLogPath = envOr("SHAHRAG_HONEYPOT_LOG", "/var/log/nginx/shahrag-honeypot.log")

const (
	honeypotZone   = "shg_hp"
	honeypotKeyVar = "$shg_hp_key"
	honeypotExempt = "$shg_hp_exempt"
)

// HoneypotEnabled reports whether anything should be emitted at all.
func HoneypotEnabled(c *config.Config) bool {
	return c != nil && c.Honeypot.Enabled && len(c.Honeypot.EffectivePaths()) > 0
}

// honeypotAllowBlock emits the geo block marking clients that never trip
// the trap.
//
// geo tests the real TCP peer address ($remote_addr), which a remote client
// cannot forge — unlike an X-Forwarded-For header, which anyone can send.
// That distinction is the difference between an exception and a bypass.
func honeypotAllowBlock(ips []string) string {
	clean := normaliseAllowIPs(ips)
	var b strings.Builder
	b.WriteString("# Clients that never trip the trap. Matched on the real TCP\n")
	b.WriteString("# peer address, which cannot be forged from outside.\n")
	fmt.Fprintf(&b, "geo %s {\n", honeypotExempt)
	b.WriteString("    default 0;\n")
	// Loopback is always exempt: the panel's own selftest and any local
	// probe must never be able to lock the operator out of their server.
	b.WriteString("    127.0.0.1/32 1;\n")
	b.WriteString("    ::1/128 1;\n")
	for _, ip := range clean {
		fmt.Fprintf(&b, "    %s 1;\n", ip)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// honeypotKeyBlock maps the exemption verdict onto the rate-limiter key.
//
// An exempt client gets an EMPTY key, and nginx does not apply limit_req to
// a request whose key is empty. Everyone else is keyed by address.
func honeypotKeyBlock() string {
	var b strings.Builder
	b.WriteString("# An empty key disables the limiter for that request, which is\n")
	b.WriteString("# how nginx itself documents carving out an exception.\n")
	fmt.Fprintf(&b, "map %s %s {\n", honeypotExempt, honeypotKeyVar)
	b.WriteString("    1 \"\";\n")
	b.WriteString("    default $binary_remote_addr;\n")
	b.WriteString("}\n\n")
	return b.String()
}

// honeypotZoneBlock declares the shared-memory zone.
//
// 1m holds roughly 16,000 addresses, which is far more than a single
// scanner will ever occupy and costs a megabyte on a 1 GB VPS.
func honeypotZoneBlock(ratePerMinute int) string {
	if ratePerMinute < 1 {
		ratePerMinute = 1
	}
	return fmt.Sprintf(
		"limit_req_zone %s zone=%s:1m rate=%dr/m;\n\n",
		honeypotKeyVar, honeypotZone, ratePerMinute)
}

// honeypotLogFormat records who tripped the trap and what they asked for.
func honeypotLogFormat() string {
	return "log_format shg_honeypot '$time_iso8601 $remote_addr \"$request\" " +
		"ua=\"$http_user_agent\" ref=\"$http_referer\"';\n\n"
}

// HoneypotPrelude returns everything that belongs at the top level of the
// http block, above the server blocks.
func HoneypotPrelude(c *config.Config) string {
	if !HoneypotEnabled(c) {
		return ""
	}
	h := c.Honeypot
	var b strings.Builder
	b.WriteString("# ── Honeypot ──────────────────────────────────────────────\n")
	b.WriteString("#    Paths no real visitor ever requests. A client that asks for\n")
	b.WriteString("#    one has identified itself as a scanner.\n")
	switch h.EffectiveMode() {
	case config.HoneypotBlock:
		b.WriteString("#    Mode: block.\n")
	case config.HoneypotDecoy:
		b.WriteString("#    Mode: decoy — a plausible empty page, plus throttling.\n")
	default:
		b.WriteString("#    Mode: throttle — the offender is slowed, not refused, so\n")
		b.WriteString("#    the server does not advertise that it filters anything.\n")
	}
	b.WriteString(honeypotAllowBlock(h.AllowIPs))
	b.WriteString(honeypotKeyBlock())
	if h.EffectiveMode() != config.HoneypotBlock {
		b.WriteString(honeypotZoneBlock(h.EffectiveRate()))
	}
	if h.LogHits {
		b.WriteString(honeypotLogFormat())
	}
	return b.String()
}

// honeypotLocationBody is the shared inside of every trap location.
func honeypotLocationBody(h config.Honeypot, indent string) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(indent + s + "\n") }

	if h.LogHits {
		fmt.Fprintf(&b, "%saccess_log %s shg_honeypot;\n", indent, HoneypotLogPath)
	}

	switch h.EffectiveMode() {
	case config.HoneypotBlock:
		// An explicit refusal for the offender, an ordinary "not found" for
		// an exempt client. Without the split the two branches were
		// identical and the exemption did nothing at all.
		//
		// This refuses the REQUEST, not the client: nginx has no memory
		// between requests, so a lasting per-address ban needs state the
		// panel keeps. That is the auto-ban step, and it reads this log.
		fmt.Fprintf(&b, "%sif (%s = 0) { return 403; }\n", indent, honeypotExempt)
		w("return 404;")

	case config.HoneypotDecoy:
		// burst=1 nodelay lets the first request through immediately and
		// then applies the rate; a scanner sees a real answer and records
		// a boring result rather than escalating.
		fmt.Fprintf(&b, "%slimit_req zone=%s burst=1 nodelay;\n", indent, honeypotZone)
		w("limit_req_status 429;")
		w("default_type text/html;")
		w("add_header Cache-Control \"no-store\" always;")
		fmt.Fprintf(&b, "%serror_page 418 = @%s_decoy;\n", indent, honeypotZone)
		w("return 418;")

	default: // throttle
		fmt.Fprintf(&b, "%slimit_req zone=%s burst=1 nodelay;\n", indent, honeypotZone)
		// 404 is what an ordinary server says about a path it does not
		// have, so the trap is indistinguishable from a plain miss.
		w("limit_req_status 404;")
		// NOT `return 404;`. `return` is handled in nginx's REWRITE phase,
		// which runs BEFORE the preaccess phase where limit_req lives — so
		// a location whose body is a bare `return` never reaches the
		// limiter at all and the throttle silently does nothing. Verified
		// against a real nginx: with `return` the fifth rapid probe was
		// still answered normally; with try_files the limiter fired and
		// logged "limiting requests".
		//
		// try_files runs in the precontent phase, after limit_req, so the
		// request is rate-limited first and only then answered. The named
		// file cannot exist, so the =404 fallback always applies.
		w("try_files /__shg_hp_nonexistent__ =404;")
	}
	return b.String()
}

// decoyPage is the body served in decoy mode.
//
// Constraints are the same as the gate's challenge page and enforced the
// same way: no `$` (nginx would expand it) and no single quote (it would
// terminate the string). Bland on purpose — it should look like a default
// installation page and teach a scanner nothing.
func decoyPage() string {
	return "<!DOCTYPE html><html><head><meta charset=\"utf-8\">" +
		"<title>Not Found</title></head><body>" +
		"<h1>404 Not Found</h1><hr><p>nginx</p></body></html>"
}

// HoneypotLocations returns the trap locations for one server block.
//
// `=` gives an exact match, which nginx resolves before any prefix or regex
// location — so a trap can never be shadowed by a service, and equally can
// never accidentally swallow a longer path beneath it. Directory-style bait
// (/wp-admin) additionally gets a prefix location with ^~ so everything
// under it is caught too.
func HoneypotLocations(c *config.Config) string {
	if !HoneypotEnabled(c) {
		return ""
	}
	h := c.Honeypot
	paths := h.EffectivePaths()
	body := honeypotLocationBody(h, "        ")

	// Refuse to emit a page nginx cannot parse, exactly as the gate does.
	if h.EffectiveMode() == config.HoneypotDecoy && !gateSafeHTML(decoyPage()) {
		return ""
	}

	var b strings.Builder
	b.WriteString("    # ── Honeypot: paths no legitimate visitor requests ──\n")
	b.WriteString("    #    Matched case-insensitively: nginx location matching is\n")
	b.WriteString("    #    case-SENSITIVE, so an exact-match trap for /wp-login.php\n")
	b.WriteString("    #    lets /WP-LOGIN.PHP straight through — and varying case is\n")
	b.WriteString("    #    one of the oldest tricks a scanner has.\n")
	if h.EffectiveMode() == config.HoneypotDecoy {
		// The decoy body is served from a named location reached through
		// error_page. A bare `return 200 "..."` would run in the rewrite
		// phase and skip the rate limiter entirely — the same trap the
		// throttle mode fell into.
		fmt.Fprintf(&b, "    location @%s_decoy {\n", honeypotZone)
		b.WriteString("        internal;\n")
		b.WriteString("        default_type text/html;\n")
		fmt.Fprintf(&b, "        return 200 '%s';\n", decoyPage())
		b.WriteString("    }\n")
	}
	// ONE alternation per group instead of one location per bait path.
	//
	// This is a measured change, not a tidy-up. nginx tests regex
	// locations sequentially, so N bait paths cost N regex evaluations on
	// EVERY request — including the overwhelming majority that are
	// ordinary visitors who will never match any of them. Benchmarked on
	// this hardware with a plain `ab` run against the ordinary path:
	//
	//	  47 separate regexes   24,854 rps
	//	   2 alternations       29,555 rps   (+19%)
	//	 100 separate regexes   22,811 rps
	//	 400 separate regexes   18,587 rps
	//	 800 separate regexes   14,286 rps
	//	2000 separate regexes    4,398 rps   (-82%)
	//
	// The cost is linear in the number of traps, which is exactly the
	// wrong shape: adding bait would make the whole site slower. Folded
	// into an alternation, PCRE matches the group in one pass and the cost
	// stops growing with the list.
	//
	// Chunked because nginx rejects a single directive parameter longer
	// than about 4 KB ("too long parameter"), which a large bait list
	// reaches. Each chunk is one location, so a realistic list is one or
	// two regexes and even an extreme one is a handful.
	exact := make([]string, 0, len(paths))
	dirs := make([]string, 0, len(paths))
	for _, p := range paths {
		// The path is regex-quoted, so the dot in /.env matches a literal
		// dot and nothing else.
		q := regexp.QuoteMeta(p)
		if honeypotIsDirLike(p) {
			// A directory bait also catches everything beneath it: a
			// scanner asking for /wp-admin/setup-config.php is the same
			// scanner.
			dirs = append(dirs, q)
		} else {
			exact = append(exact, q)
		}
	}
	// ~* is case-insensitive. Exact-match locations are compared byte for
	// byte, so a trap for /wp-login.php would not catch /WP-LOGIN.PHP —
	// verified against a real nginx, where the uppercase probe got a 200.
	for _, group := range honeypotChunks(exact) {
		fmt.Fprintf(&b, "    location ~* ^(%s)$ {\n", group)
		b.WriteString(body)
		b.WriteString("    }\n")
	}
	for _, group := range honeypotChunks(dirs) {
		fmt.Fprintf(&b, "    location ~* ^(%s)(/|$) {\n", group)
		b.WriteString(body)
		b.WriteString("    }\n")
	}
	b.WriteString("\n")
	return b.String()
}

// maxRegexChunk bounds one generated alternation.
//
// nginx refuses a directive parameter longer than roughly 4 KB with
// "too long parameter" — reproduced with a 19 KB alternation, which it
// rejected outright. 3 KB leaves comfortable room for the surrounding
// syntax while still fitting a realistic bait list in a single regex.
const maxRegexChunk = 3000

// honeypotChunks splits quoted paths into alternation groups that stay
// under the parameter limit. Order is preserved, so the generated file is
// stable across runs.
func honeypotChunks(quoted []string) []string {
	if len(quoted) == 0 {
		return nil
	}
	var out []string
	var cur strings.Builder
	for _, q := range quoted {
		// +1 for the separating pipe.
		if cur.Len() > 0 && cur.Len()+len(q)+1 > maxRegexChunk {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte('|')
		}
		cur.WriteString(q)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// honeypotFileRe matches a bait path whose last segment looks like a file.
var honeypotFileRe = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)

// honeypotIsDirLike reports whether a bait path should also trap everything
// beneath it. A path ending in an extension is a file and gets only the
// exact match; anything else is treated as a directory.
func honeypotIsDirLike(p string) bool {
	last := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		last = p[i+1:]
	}
	if last == "" {
		return false
	}
	// A dotfile is a file, not a directory: /.env has no extension but is
	// certainly not a folder to recurse into.
	if strings.HasPrefix(last, ".") && !strings.Contains(last[1:], ".") {
		return false
	}
	// Anything living UNDER a dot-directory is a specific file being probed
	// (/.git/config, /.ssh/id_rsa). Trapping the whole tree beneath it adds
	// nothing, since the exact paths are what scanners ask for.
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return !honeypotFileRe.MatchString(last)
}

// envOr lets the test suite redirect the honeypot log; production always
// uses the default, since nothing sets the variable.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
