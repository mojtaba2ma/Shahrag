package nginx

// The nginx side of the real site.
//
// Where the real site sits in a server block, and why
// ───────────────────────────────────────────────────
// It replaces the `location /` that previously served the fake page, and
// ONLY that. Every proxied service keeps its own location, and nginx's
// longest-prefix rule means a service on /api is still matched before the
// site's `/`. A service that claims `/` itself still wins, because the
// generator already skips the fallback location in that case — the real site
// must never take a path away from a working service, and the panel warns
// about that combination instead of silently changing it.
//
// Why error_page is emitted only for pages that exist
// ───────────────────────────────────────────────────
// `error_page 404 /errors/404.html;` pointing at a missing file does not
// produce a 404 — it produces a 500, because nginx cannot serve the error
// handler. So the renderer reports which files it actually wrote and only
// those get a directive. This is also why the error locations are `internal`:
// without it, anyone can request /errors/502.html directly and get a 200,
// which is both untidy and a fingerprint.
//
// Why the site's own root is a separate location and not `root` on the server
// ───────────────────────────────────────────────────────────────────────────
// A `root` at server level would apply to every location that does not set
// its own, including ones added later, and a future service with try_files
// would suddenly start serving website files. Keeping it inside the location
// bounds the blast radius to exactly the path it is meant to serve.

import (
	"fmt"
	"sort"
	"strings"

	"shahrag/internal/config"
)

// RealSitePlan is what the generator needs to know about one domain, filled
// in by the caller after rendering: the generator must not do file I/O.
type RealSitePlan struct {
	Root string
	// ErrorPages maps status code to a path relative to Root.
	ErrorPages map[string]string
	// ErrorRoot is a separate root for error pages, used when the site
	// serves the operator's own directory and the panel must not write
	// into it.
	ErrorRoot string
	Index     string
	// CacheAssets adds an expires header for static files.
	CacheAssets bool
	// ExtraConfig is verbatim operator directives.
	ExtraConfig string
}

// RealSiteLocations renders the location blocks for one domain. Returns ""
// when there is nothing to emit, so a config with the feature off is byte
// identical to one from a version that never had it.
func RealSiteLocations(plan *RealSitePlan) string {
	if plan == nil || strings.TrimSpace(plan.Root) == "" {
		return ""
	}
	idx := strings.TrimSpace(plan.Index)
	if idx == "" {
		idx = "index.html"
	}
	var b strings.Builder
	b.WriteString("    # Real site\n")
	b.WriteString("    location / {\n")
	fmt.Fprintf(&b, "        root %s;\n", plan.Root)
	fmt.Fprintf(&b, "        index %s;\n", idx)
	// try_files ends in =404 rather than /index.html: a single-page
	// fallback would answer 200 for every path a scanner invents, which
	// makes the site look machine-generated and hides real 404s from the
	// statistics page.
	fmt.Fprintf(&b, "        try_files $uri $uri/ =404;\n")
	if plan.CacheAssets {
		b.WriteString("    }\n\n")
		b.WriteString("    # Static assets of the real site\n")
		b.WriteString("    location ~* ^/(assets|img|images|fonts|static)/ {\n")
		fmt.Fprintf(&b, "        root %s;\n", plan.Root)
		b.WriteString("        expires 7d;\n")
		b.WriteString("        add_header Cache-Control \"public, max-age=604800\";\n")
		b.WriteString("        access_log off;\n")
		b.WriteString("        try_files $uri =404;\n")
	}
	if extra := indentBlock(plan.ExtraConfig, "        "); extra != "" {
		b.WriteString(extra)
	}
	b.WriteString("    }\n\n")

	b.WriteString(realSiteErrorBlock(plan))
	return b.String()
}

// realSiteErrorBlock emits the error_page directives and the internal
// locations that serve them.
func realSiteErrorBlock(plan *RealSitePlan) string {
	if len(plan.ErrorPages) == 0 {
		return ""
	}
	root := plan.Root
	if r := strings.TrimSpace(plan.ErrorRoot); r != "" {
		root = r
	}
	codes := make([]string, 0, len(plan.ErrorPages))
	for c := range plan.ErrorPages {
		if config.ValidErrorCode(c) {
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	if len(codes) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("    # Error pages of the real site\n")
	// Group the codes that share a file into one directive: eleven
	// separate error_page lines where three would do is eleven lines
	// nginx parses on every reload and eleven lines an operator reads.
	byFile := map[string][]string{}
	for _, c := range codes {
		f := plan.ErrorPages[c]
		byFile[f] = append(byFile[f], c)
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		list := byFile[f]
		sort.Strings(list)
		fmt.Fprintf(&b, "    error_page %s /__shg_err/%s;\n", strings.Join(list, " "), f)
	}
	b.WriteString("\n")
	// One internal location serves them all. `internal` means nginx
	// refuses a direct request for it with a 404 — the pages are reachable
	// only as the result of an error, which is what a real site looks
	// like.
	b.WriteString("    location ^~ /__shg_err/ {\n")
	b.WriteString("        internal;\n")
	fmt.Fprintf(&b, "        alias %s/;\n", strings.TrimRight(root, "/"))
	b.WriteString("    }\n\n")
	return b.String()
}

// indentBlock re-indents an operator's directives and drops blank lines, so
// the generated file stays readable no matter how the text was pasted.
func indentBlock(s, indent string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		b.WriteString(indent)
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}
