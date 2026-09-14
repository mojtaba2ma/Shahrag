// Package realsite turns a template plus the operator's text into a
// document root nginx can serve.
//
// Why a placeholder pass and not a template engine
// ────────────────────────────────────────────────
// The templates come from a repository on the internet. Handing them to
// html/template would let an archive execute template actions — ranges,
// function calls, access to whatever data the panel passes in. A plain
// `{{NAME}}` substitution over a byte slice cannot do any of that: the
// template chooses where text goes and nothing else. Every substituted value
// is HTML-escaped on the way in, so an operator who types a `<script>` into
// the tagline gets a tagline that reads `<script>`, not a script.
//
// Why the output is written to disk instead of served by the panel
// ────────────────────────────────────────────────────────────────
// nginx serving a static file from disk costs a few microseconds and no Go
// process at all. Proxying every page view of a decoy site through the panel
// would put the panel in the request path of anonymous internet traffic —
// more memory, more attack surface, and a panel restart would take the
// "website" down in a way a real website never goes down.
//
// Why rendering is idempotent and content-addressed
// ─────────────────────────────────────────────────
// The generator runs on every config change. Re-rendering five pages each
// time is cheap, but rewriting files nginx is serving is not free and churns
// the page cache. So a stamp file records the hash of (template, content,
// options); when it matches, rendering is skipped entirely.
package realsite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/templates"
)

// stampFile records what was rendered, so an unchanged site is not rewritten.
const stampFile = ".shahrag-render.json"

// maxRenderBytes caps one rendered file. A template that somehow expands
// without bound must not fill a 1 GB server's disk.
const maxRenderBytes = 4 << 20

// placeholder matches {{NAME}} with no spaces, which is all the templates use.
var placeholder = regexp.MustCompile(`\{\{([A-Z][A-Z0-9_]{0,47})\}\}`)

// textExt are the file types that get a substitution pass. Binary assets are
// copied byte for byte: running a regex over a PNG is wasted work and could
// corrupt it if a byte sequence happened to look like a placeholder.
var textExt = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true,
	".txt": true, ".xml": true, ".json": true, ".svg": true, ".webmanifest": true,
}

// Renderer writes rendered sites under Dir.
type Renderer struct {
	Dir    string
	Client *templates.Client
	// Now is overridable for tests.
	Now func() time.Time
}

func (r *Renderer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// DomainRoot is where a domain's rendered site lives.
func (r *Renderer) DomainRoot(domain string) string {
	return filepath.Join(r.Dir, safeDomainDir(domain))
}

// safeDomainDir turns a hostname into a directory name that cannot escape
// the sites directory. Domains are validated elsewhere, but this path ends
// up in an nginx `root` directive, so it is checked again here.
func safeDomainDir(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	var b strings.Builder
	for _, ch := range d {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		case ch == '.' || ch == '-' || ch == '_':
			b.WriteRune(ch)
		case ch == '*':
			b.WriteString("_wild_")
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	// Collapse any remaining ".." run. The slashes are already gone, so
	// the result is a single path segment and cannot traverse — but a
	// directory literally named "_.._etc" passes through a shell, a log
	// line and an nginx root, and the value only has to survive ONE
	// careless string concatenation somewhere later to become a
	// traversal. Removing it costs nothing.
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", ".")
	}
	out = strings.Trim(out, ".")
	if out == "" {
		return "_unnamed"
	}
	return out
}

type stamp struct {
	Hash       string    `json:"hash"`
	RenderedAt time.Time `json:"rendered_at"`
	Template   string    `json:"template"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
}

// Result reports what a render did.
type Result struct {
	Domain   string    `json:"domain"`
	Root     string    `json:"root"`
	Template string    `json:"template"`
	Skipped  bool      `json:"skipped"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	At       time.Time `json:"at"`
	// ErrorPages maps status code to the path of the file that serves it,
	// relative to Root. The nginx generator needs exactly this.
	ErrorPages map[string]string `json:"error_pages,omitempty"`
}

// Render materialises one domain's site. It is safe to call on every config
// change: an unchanged site costs one file read and one hash comparison.
func (r *Renderer) Render(domain string, site config.RealSite) (Result, error) {
	res := Result{Domain: domain, Template: site.Template, At: r.now()}

	// A site pointed at the operator's own directory renders nothing —
	// their files are theirs, and rewriting them would be both rude and
	// destructive. Only the error pages are generated, into a private
	// subdirectory of the sites dir so the operator's tree is untouched.
	if strings.TrimSpace(site.Root) != "" {
		res.Root = strings.TrimSpace(site.Root)
		ep, err := r.renderErrorsOnly(domain, site)
		if err != nil {
			return res, err
		}
		res.ErrorPages = ep
		return res, nil
	}

	if strings.TrimSpace(site.Template) == "" {
		return res, fmt.Errorf("no template chosen for %s", domain)
	}
	tfs, ok := r.Client.OpenTemplate(site.Template)
	if !ok {
		return res, fmt.Errorf("template %q is not installed", site.Template)
	}

	vals := Values(domain, site)
	h := hashInputs(site, vals, r.Client)
	root := r.DomainRoot(domain)
	res.Root = root

	if prev, err := readStamp(root); err == nil && prev.Hash == h {
		res.Skipped = true
		res.Files, res.Bytes = prev.Files, prev.Bytes
		res.ErrorPages = r.errorPageMap(root, site)
		return res, nil
	}

	tmp, err := os.MkdirTemp(filepath.Dir(root), ".render-")
	if err != nil {
		if mkErr := os.MkdirAll(filepath.Dir(root), 0o755); mkErr != nil {
			return res, mkErr
		}
		tmp, err = os.MkdirTemp(filepath.Dir(root), ".render-")
		if err != nil {
			return res, err
		}
	}
	defer os.RemoveAll(tmp)

	files, total, err := renderTree(tfs, tmp, vals)
	if err != nil {
		return res, err
	}

	// Error pages, after the template so an override wins.
	epFiles, epBytes, err := r.writeErrorPages(tmp, site, vals)
	if err != nil {
		return res, err
	}
	files += epFiles
	total += epBytes

	st := stamp{Hash: h, RenderedAt: r.now(), Template: site.Template, Files: files, Bytes: total}
	sb, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(tmp, stampFile), sb, 0o644); err != nil {
		return res, err
	}

	if err := swap(tmp, root); err != nil {
		return res, err
	}
	res.Files, res.Bytes = files, total
	res.ErrorPages = r.errorPageMap(root, site)
	return res, nil
}

// swap replaces dst with src atomically enough that a visitor never sees a
// half-written site: the new tree is built elsewhere and moved in, and the
// old tree is only deleted afterwards.
func swap(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	old := dst + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	// The temp dir was created 0700 by MkdirTemp; nginx's worker runs as
	// www-data and must be able to traverse it.
	_ = os.Chmod(dst, 0o755)
	_ = os.RemoveAll(old)
	return nil
}

func readStamp(root string) (stamp, error) {
	var st stamp
	b, err := os.ReadFile(filepath.Join(root, stampFile))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// renderTree copies a template filesystem into dst, substituting placeholders
// in text files.
func renderTree(src fs.FS, dst string, vals map[string]string) (int, int64, error) {
	var files int
	var total int64
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		base := path.Base(p)
		// Never copy the template's own metadata into a public root.
		if base == ".shahrag-template.json" || base == stampFile {
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, rerr := fs.ReadFile(src, p)
		if rerr != nil {
			return rerr
		}
		if textExt[strings.ToLower(path.Ext(p))] {
			b = Substitute(b, vals)
		}
		if int64(len(b)) > maxRenderBytes {
			return fmt.Errorf("%s renders to more than %d bytes", p, maxRenderBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		files++
		total += int64(len(b))
		return nil
	})
	return files, total, err
}

// Substitute replaces every {{NAME}} for which vals has a value. A
// placeholder with no value is replaced with the empty string rather than
// left in place: a visitor seeing `{{HERO_TITLE}}` on the page is a far worse
// outcome than a blank heading, and it is the single most obvious tell that
// a site is generated.
func Substitute(in []byte, vals map[string]string) []byte {
	if !bytes.Contains(in, []byte("{{")) {
		return in
	}
	return placeholder.ReplaceAllFunc(in, func(m []byte) []byte {
		key := string(m[2 : len(m)-2])
		if v, ok := vals[key]; ok {
			return []byte(v)
		}
		return nil
	})
}

// Values builds the substitution table for one domain.
//
// Everything here is HTML-escaped. The operator's text lands inside HTML
// elements and attributes, and while the operator is trusted, the value can
// also come from a restored backup or a hand-edited config — and a template
// from a repository decides WHERE it lands, including inside an href.
func Values(domain string, site config.RealSite) map[string]string {
	c := site.Content
	esc := html.EscapeString

	name := strings.TrimSpace(c.SiteName)
	if name == "" {
		name = defaultSiteName(domain)
	}
	lang := strings.TrimSpace(c.Language)
	if lang == "" {
		lang = "en"
	}
	dir := "ltr"
	if isRTL(lang) {
		dir = "rtl"
	}
	url := strings.TrimSpace(c.SiteURL)
	if url == "" && domain != "" && !strings.Contains(domain, "*") {
		url = "https://" + domain
	}
	robots := "index,follow"
	robotsRule := "Allow: /"
	if c.NoIndex {
		robots = "noindex,nofollow"
		robotsRule = "Disallow: /"
	}

	v := map[string]string{
		"SITE_NAME":     esc(name),
		"SITE_INITIAL":  esc(initial(name)),
		"SITE_TAGLINE":  esc(orDefault(c.Tagline, defaultTagline(lang))),
		"SITE_URL":      esc(strings.TrimRight(url, "/")),
		"LANG":          esc(lang),
		"DIR":           dir,
		"YEAR":          strconv.Itoa(time.Now().Year()),
		"EMAIL":         esc(orDefault(c.Email, "info@"+plainHost(domain))),
		"PHONE":         esc(orDefault(c.Phone, "+00 000 0000")),
		"ADDRESS":       esc(orDefault(c.Address, defaultAddress(lang))),
		"ROBOTS":        robots,
		"ROBOTS_RULE":   robotsRule,
		"FORM_ACTION":   "#",
		"ASSET_BASE":    "/",
		"HERO_TITLE":    esc(orDefault(c.HeroTitle, defaultHero(lang, name))),
		"HERO_TEXT":     esc(orDefault(c.HeroText, defaultHeroText(lang))),
		"CTA_PRIMARY":   tr(lang, "cta_primary"),
		"CTA_SECONDARY": tr(lang, "cta_secondary"),
	}
	for _, k := range copyKeys {
		v[k] = tr(lang, strings.ToLower(k))
	}
	// Operator-supplied extras last, so they override anything above —
	// that is the point of an escape hatch.
	for k, val := range c.Extra {
		key := strings.ToUpper(strings.TrimSpace(k))
		if placeholder.MatchString("{{" + key + "}}") {
			v[key] = esc(val)
		}
	}
	return v
}

// ASSET_BASE for a page one directory down (errors/404.html) must climb out.
func valuesForDepth(vals map[string]string, depth int) map[string]string {
	if depth <= 0 {
		return vals
	}
	out := make(map[string]string, len(vals))
	for k, v := range vals {
		out[k] = v
	}
	out["ASSET_BASE"] = strings.Repeat("../", depth)
	return out
}

func plainHost(d string) string {
	d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "*.")
	if d == "" {
		return "example.com"
	}
	return d
}

func defaultSiteName(domain string) string {
	h := plainHost(domain)
	parts := strings.Split(h, ".")
	if len(parts) == 0 || parts[0] == "" {
		return "Website"
	}
	w := strings.ReplaceAll(parts[0], "-", " ")
	return strings.ToUpper(w[:1]) + w[1:]
}

func initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return strings.ToUpper(string(r))
	}
	return "W"
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func isRTL(lang string) bool {
	switch strings.ToLower(strings.SplitN(lang, "-", 2)[0]) {
	case "fa", "ar", "he", "ur", "ps", "ckb":
		return true
	}
	return false
}

// hashInputs produces the stamp hash. It covers the template id, the resolved
// values, the error-page settings, and the template's own content hash — the
// last one so re-installing an updated template actually re-renders.
func hashInputs(site config.RealSite, vals map[string]string, cl *templates.Client) string {
	h := sha256.New()
	fmt.Fprintf(h, "v2\ntemplate=%s\nerrmode=%s\nerrtpl=%s\nindex=%s\n",
		site.Template, config.NormalizeErrorMode(site.ErrorMode), site.ErrorTemplate, site.Index)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, vals[k])
	}
	codes := make([]string, 0, len(site.ErrorPages))
	for c := range site.ErrorPages {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		ep := site.ErrorPages[c]
		fmt.Fprintf(h, "err %s disabled=%v tpl=%s html=%x\n",
			c, ep.Disabled, ep.Template, sha256.Sum256([]byte(ep.HTML)))
	}
	fmt.Fprintf(h, "tplhash=%s\n", templateHash(cl, site.Template))
	if site.ErrorTemplate != "" && site.ErrorTemplate != site.Template {
		fmt.Fprintf(h, "errtplhash=%s\n", templateHash(cl, site.ErrorTemplate))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// templateHash hashes a template's file names and sizes — cheap, and enough
// to notice a reinstall. Hashing every byte of every file on every config
// change would cost far more than it buys.
func templateHash(cl *templates.Client, id string) string {
	if id == "" {
		return ""
	}
	tfs, ok := cl.OpenTemplate(id)
	if !ok {
		return "missing"
	}
	h := sha256.New()
	_ = fs.WalkDir(tfs, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		fmt.Fprintf(h, "%s:%d\n", p, info.Size())
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// RootFor is the document root of a domain's rendered site. Exported so the
// API can report it without constructing a Renderer.
func RootFor(dir, domain string) string {
	return filepath.Join(dir, safeDomainDir(domain))
}

// ListRoot returns the files of a rendered site, capped at max entries, with
// the total on disk. Used by the panel to show that a site really exists.
func ListRoot(root string, max int) ([]map[string]interface{}, int64) {
	out := []map[string]interface{}{}
	var total int64
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += info.Size()
		if len(out) >= max || strings.HasPrefix(filepath.Base(p), ".") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, map[string]interface{}{
			"path": filepath.ToSlash(rel), "bytes": info.Size(),
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i]["path"].(string) < out[j]["path"].(string)
	})
	return out, total
}
