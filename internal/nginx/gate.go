package nginx

// Service gate — an optional challenge that runs BEFORE nginx proxies to a
// backend, so a scanner never receives the panel's HTML, script names or
// login form.
//
// Why this exists: services like an x-ui or sanaei panel are found by robots
// that crawl IP ranges and fingerprint the response body. Hiding the panel
// behind a long path helps, but the moment the path leaks the backend answers
// with something identifiable. With a gate the FIRST response is always a
// neutral page; the real service is only revealed to a client that proved it
// is a browser (mode "js") or that knows a word (mode "secret").
//
// Implementation notes
//
//   - Pure nginx: no Lua, no extra module, no helper daemon. The check is a
//     `map` on a cookie plus one `if` that internally rewrites to a private
//     location. `rewrite ... last` is one of the two directives that are
//     actually safe inside `if` in a location context.
//   - The challenge page is emitted with `return 200`, which means the HTML
//     must contain no `$` (nginx would read it as a variable) and no single
//     quote (it terminates the string). buildGatePage is written to that
//     constraint and gateSafeHTML enforces it.
//   - The cookie value IS the secret. For "secret" mode that is the word the
//     operator chose; for "js" mode it is a random token that only appears
//     inside the challenge page's script, so a client that does not execute
//     JavaScript can never learn it.
//   - The gate only guards HTTP services. An SNI rule is a raw TCP relay:
//     nginx never sees the bytes, so there is nothing to challenge.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"shahrag/internal/config"
)

// GateCookiePrefix is prepended to every gate cookie name. Keeping a common
// prefix makes the cookies easy to spot and to clear.
const GateCookiePrefix = "shg_"

// gateSlugRe matches everything that is NOT allowed in an nginx variable
// name. Service names are free-form, but `$cookie_<name>` only accepts
// letters, digits and underscores.
var gateSlugRe = regexp.MustCompile(`[^a-z0-9_]+`)

// GateSlug turns a service name into a token usable in an nginx variable and
// in a URI. Two different service names could in principle collapse onto the
// same slug ("my-panel" and "my_panel"); the caller disambiguates by
// appending an index, see gateSlugs.
func GateSlug(name string) string {
	s := gateSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "svc"
	}
	return s
}

// gateSlugs assigns a unique slug to every gated service. Sorted iteration
// keeps the generated file byte-stable across runs, which matters because the
// panel diffs and validates what it writes.
func gateSlugs(c *config.Config) map[string]string {
	names := make([]string, 0, len(c.Services))
	for n, svc := range c.Services {
		if svc.GateEnabled() {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	out := make(map[string]string, len(names))
	used := map[string]bool{}
	for _, n := range names {
		base := GateSlug(n)
		slug := base
		for i := 2; used[slug]; i++ {
			slug = fmt.Sprintf("%s_%d", base, i)
		}
		used[slug] = true
		out[n] = slug
	}
	return out
}

// GateCookieName is the cookie a passed challenge leaves behind.
func GateCookieName(slug string) string { return GateCookiePrefix + slug }

// GatePath is the private URI the unauthenticated request is rewritten to.
// It is marked `internal`, so it cannot be requested directly from outside.
func GatePath(slug string) string { return "/__shg_gate_" + slug }

// NewGateToken makes the random value used by the "js" mode. It never leaves
// the challenge page, so 128 bits is ample.
func NewGateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// gateSecretRe is deliberately strict: the value travels as a raw cookie, so
// allowing quotes, semicolons or spaces would break the cookie header or the
// generated nginx string.
var gateSecretRe = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)

// ValidGateSecret reports whether a secret can be used safely.
func ValidGateSecret(s string) bool { return gateSecretRe.MatchString(s) }

// gateSafeHTML is a guard rail, not decoration. A `$` or a `'` reaching
// `return 200 '...'` would either be expanded as an nginx variable or
// terminate the string early and produce a config that fails to load. If a
// future edit introduces one, the gate is dropped for that service rather
// than emitting a broken file.
func gateSafeHTML(s string) bool {
	return !strings.ContainsAny(s, "$'")
}

// buildGatePage renders the challenge body.
//
// Constraints, all enforced by gateSafeHTML: no `$`, no single quotes. The
// markup is intentionally bland — a scanner should learn nothing from it.
func buildGatePage(mode, cookie, token string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta name="robots" content="noindex,nofollow">`)
	b.WriteString(`<title>Loading</title><style>`)
	b.WriteString(`body{margin:0;height:100vh;display:flex;align-items:center;justify-content:center;`)
	b.WriteString(`background:#0f1115;color:#c9d1d9;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}`)
	b.WriteString(`.c{text-align:center;max-width:320px;padding:24px}`)
	b.WriteString(`.s{width:28px;height:28px;margin:0 auto 16px;border:3px solid #2a2f3a;`)
	b.WriteString(`border-top-color:#5b8def;border-radius:50%;animation:r .8s linear infinite}`)
	b.WriteString(`@keyframes r{to{transform:rotate(360deg)}}`)
	b.WriteString(`input{width:100%;box-sizing:border-box;padding:10px;margin:10px 0;border-radius:8px;`)
	b.WriteString(`border:1px solid #2a2f3a;background:#161a22;color:#c9d1d9;font-size:15px}`)
	b.WriteString(`button{width:100%;padding:10px;border:0;border-radius:8px;background:#5b8def;`)
	b.WriteString(`color:#fff;font-size:15px;cursor:pointer}`)
	b.WriteString(`p{font-size:13px;color:#8b949e;margin:8px 0 0}`)
	b.WriteString(`</style></head><body><div class="c">`)

	if mode == config.GateSecret {
		b.WriteString(`<div id="f"><input id="w" type="password" autocomplete="off" `)
		b.WriteString(`placeholder="Access key" autofocus><button id="g">Continue</button>`)
		b.WriteString(`<p id="m"></p></div>`)
	} else {
		b.WriteString(`<div class="s"></div><p>Checking your browser...</p>`)
		b.WriteString(`<noscript><p>JavaScript is required to continue.</p></noscript>`)
	}
	b.WriteString(`</div><script>`)

	// A shared helper. `document.cookie` is written without encoding, which
	// is why the accepted secret alphabet is restricted: the value must
	// survive the round trip byte-for-byte or the map will not match.
	b.WriteString(`function sc(v){document.cookie="` + cookie + `="+v+`)
	b.WriteString(`";path=/;max-age=86400;SameSite=Lax;Secure"}`)

	if mode == config.GateSecret {
		b.WriteString(`var g=document.getElementById("g"),w=document.getElementById("w");`)
		b.WriteString(`function go(){var v=w.value.trim();if(!v)return;sc(v);`)
		// A wrong word simply lands back here. Marking the attempt lets the
		// page tell "wrong key" apart from "first visit" without leaking
		// whether the key exists.
		b.WriteString(`try{sessionStorage.setItem("` + cookie + `_a","1")}catch(e){}`)
		b.WriteString(`location.replace(location.href)}`)
		b.WriteString(`g.onclick=go;w.onkeydown=function(e){if(e.key==="Enter")go()};`)
		b.WriteString(`try{if(sessionStorage.getItem("` + cookie + `_a")){`)
		b.WriteString(`document.getElementById("m").textContent="Incorrect key."}}catch(e){}`)
	} else {
		// Reload guard. Without it a browser that refuses cookies would
		// bounce between the gate and itself forever. The timestamp makes
		// the guard self-clearing, so a cookie that merely expired later
		// does not permanently show the error.
		b.WriteString(`(function(){var k="` + cookie + `_t",n=Date.now(),p=0;`)
		b.WriteString(`try{p=parseInt(sessionStorage.getItem(k)||"0",10)}catch(e){}`)
		b.WriteString(`if(p&&n-p<10000){document.querySelector(".c").innerHTML=`)
		b.WriteString(`"<p>Cookies must be enabled to continue.</p>";return}`)
		b.WriteString(`try{sessionStorage.setItem(k,String(n))}catch(e){}`)
		b.WriteString(`sc("` + token + `");location.replace(location.href)})();`)
	}
	b.WriteString(`</script></body></html>`)
	return b.String()
}

// gateMapBlock emits the http-level cookie test for one service.
//
// The map is the whole authorisation check: `1` means the request carries the
// right cookie and may reach the backend, anything else is challenged.
func gateMapBlock(slug, secret string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "map $cookie_%s $%s_ok {\n", GateCookieName(slug), GateCookieName(slug))
	b.WriteString("    default 0;\n")
	fmt.Fprintf(&b, "    %q 1;\n", secret)
	b.WriteString("}\n\n")
	return b.String()
}

// SearchEngineBots are the crawlers GateAllowBots lets through.
//
// Matching is on User-Agent, which the client chooses freely — anyone can
// send "Googlebot". So this is a CONVENIENCE for indexing, never a security
// boundary. The panel says so in the UI, and GateAllowPaths exists for the
// cases where it actually matters.
var SearchEngineBots = []string{
	"googlebot", "bingbot", "slurp", "duckduckbot", "baiduspider",
	"yandexbot", "applebot", "petalbot", "twitterbot", "facebookexternalhit",
	"linkedinbot", "telegrambot", "whatsapp", "discordbot", "ia_archiver",
}

// gateBotMapBlock emits the User-Agent test. Shared by every service that
// enables it, hence the single global variable name.
func gateBotMapBlock() string {
	var b strings.Builder
	b.WriteString("map $http_user_agent $shg_is_searchbot {\n")
	b.WriteString("    default 0;\n")
	for _, ua := range SearchEngineBots {
		// ~* is a case-insensitive regex match; the UA string appears
		// anywhere in the header.
		fmt.Fprintf(&b, "    ~*%s 1;\n", regexp.QuoteMeta(ua))
	}
	b.WriteString("}\n\n")
	return b.String()
}

// gateAllowPathMapBlock emits the per-service path exemption.
//
// The match is on $uri (the decoded, normalised path), NOT $request_uri, so
// "/sitemap.xml?x=1" and "/SiteMap.xml" cannot be used to slip past a rule,
// and neither can "%2Fsitemap.xml" style encoding tricks.
func gateAllowPathMapBlock(slug string, paths []string) string {
	clean := normaliseAllowPaths(paths)
	if len(clean) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "map $uri $%s_pathok {\n", GateCookieName(slug))
	b.WriteString("    default 0;\n")
	for _, p := range clean {
		if strings.HasSuffix(p, "/") {
			// A directory rule covers everything beneath it.
			fmt.Fprintf(&b, "    ~^%s 1;\n", regexp.QuoteMeta(p))
		} else {
			fmt.Fprintf(&b, "    %s 1;\n", p)
		}
	}
	b.WriteString("}\n\n")
	return b.String()
}

// normaliseAllowPaths cleans operator input: every entry gets a leading
// slash, blanks are dropped, and duplicates are removed. Sorted output keeps
// the generated file stable.
func normaliseAllowPaths(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		// A path carrying a quote or a $ would break the generated literal.
		if strings.ContainsAny(p, "$'\" \t\n{};") {
			continue
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// gateAllowIPBlock emits the geo block that marks exempt client addresses.
//
// `geo` reads $remote_addr, the peer of the actual TCP connection, so unlike
// a header this cannot be spoofed by a remote client. That is what makes it
// the right mechanism for a private VLAN or a fixed office address.
func gateAllowIPBlock(slug string, ips []string) string {
	clean := normaliseAllowIPs(ips)
	if len(clean) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "geo $%s_ipok {\n", GateCookieName(slug))
	b.WriteString("    default 0;\n")
	for _, ip := range clean {
		fmt.Fprintf(&b, "    %s 1;\n", ip)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// gateIPRe accepts an IPv4/IPv6 literal or CIDR. Anything else is dropped
// rather than written into the config, where it would fail `nginx -t`.
var gateIPRe = regexp.MustCompile(`^[0-9a-fA-F:.]+(/[0-9]{1,3})?$`)

func normaliseAllowIPs(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || !gateIPRe.MatchString(s) {
			continue
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// gateExemptMapBlock folds every exemption into ONE variable.
//
// nginx cannot do boolean OR in an `if`, and stacking several `if`s in a
// location is exactly the "if is evil" footgun. So the combination happens in
// a map keyed on the concatenated flags: any '1' anywhere means exempt.
func gateExemptMapBlock(slug string, hasPaths, hasIPs, allowBots bool) string {
	if !hasPaths && !hasIPs && !allowBots {
		return ""
	}
	cookie := GateCookieName(slug)

	parts := []string{}
	if hasPaths {
		parts = append(parts, "$"+cookie+"_pathok")
	}
	if hasIPs {
		parts = append(parts, "$"+cookie+"_ipok")
	}
	if allowBots {
		parts = append(parts, "$shg_is_searchbot")
	}

	var b strings.Builder
	// The concatenation is the map key: with three sources it is a string
	// like "010". Matching ~1 asks "is any source set?".
	fmt.Fprintf(&b, "map \"%s\" $%s_exempt {\n", strings.Join(parts, ""), cookie)
	b.WriteString("    default 0;\n")
	b.WriteString("    ~1 1;\n")
	b.WriteString("}\n\n")
	return b.String()
}

// gatePassMapBlock is the final verdict: pass if the cookie is right OR any
// exemption applies. Again a map, for the same reason as above.
func gatePassMapBlock(slug string, hasExempt bool) string {
	cookie := GateCookieName(slug)
	if !hasExempt {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "map \"$%s_ok$%s_exempt\" $%s_pass {\n", cookie, cookie, cookie)
	b.WriteString("    default 0;\n")
	b.WriteString("    ~1 1;\n")
	b.WriteString("}\n\n")
	return b.String()
}

// gateGuard is the single line placed at the top of a guarded location.
//
// It tests one variable: either the plain cookie check, or the combined
// verdict when the service has exemptions.
func gateGuard(slug string, hasExempt bool) string {
	v := GateCookieName(slug) + "_ok"
	if hasExempt {
		v = GateCookieName(slug) + "_pass"
	}
	return fmt.Sprintf("if ($%s = 0) { rewrite ^ %s last; }", v, GatePath(slug))
}

// GateExemptions reports which exemption kinds a service uses.
func GateExemptions(svc config.Service) (paths, ips, bots bool) {
	return len(normaliseAllowPaths(svc.GateAllowPaths)) > 0,
		len(normaliseAllowIPs(svc.GateAllowIPs)) > 0,
		svc.GateAllowBots
}

// gateHasExempt is true when any exemption is configured.
func gateHasExempt(svc config.Service) bool {
	p, i, b := GateExemptions(svc)
	return p || i || b
}

// AnyGateAllowsBots reports whether at least one service enables the
// search-engine exemption, so the shared UA map is emitted only when used.
func AnyGateAllowsBots(c *config.Config) bool {
	for _, svc := range c.Services {
		if svc.GateEnabled() && svc.GateAllowBots {
			return true
		}
	}
	return false
}

// gateLocation emits the private location that serves the challenge.
func gateLocation(slug, mode, secret, token string) string {
	page := buildGatePage(mode, GateCookieName(slug), token)
	if !gateSafeHTML(page) {
		// Refuse to write a file nginx cannot parse. The service still works,
		// it just is not gated, which is the safe direction to fail in.
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "    # gate for %s\n", slug)
	fmt.Fprintf(&b, "    location = %s {\n", GatePath(slug))
	b.WriteString("        internal;\n")
	b.WriteString("        default_type text/html;\n")
	b.WriteString("        add_header Cache-Control \"no-store\" always;\n")
	// 200, not 403: a scanner learns nothing from a page that looks like an
	// ordinary loading screen.
	fmt.Fprintf(&b, "        return 200 '%s';\n", page)
	b.WriteString("    }\n")
	return b.String()
}

// GatedServices returns the slug for each service that has a gate, or an
// empty map when none do. Callers use it to decide whether to emit anything
// at all, so a config without gates produces byte-identical output to before
// this feature existed.
func GatedServices(c *config.Config) map[string]string { return gateSlugs(c) }

// gateSecretFor returns the value the cookie must carry, and the token to put
// in the page. For "secret" mode they differ: the page must NOT contain the
// word, or the whole point is lost.
func gateSecretFor(svc config.Service) (secret, token string) {
	secret = strings.TrimSpace(svc.GateSecret)
	if config.NormalizeGate(svc.Gate) == config.GateSecret {
		return secret, ""
	}
	return secret, secret
}
