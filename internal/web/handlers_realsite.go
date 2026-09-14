package web

// The real-site and template-gallery API.
//
// Two things share this file because they are two halves of one feature: the
// gallery is where a template comes from, and the real site is what it is
// used for.
//
// The gallery endpoints never fail the page. A blocked repository returns 200
// with an empty list and an `error` string, because the alternative — a 502
// on a panel screen — reads as "the panel is broken" when the truth is "GitHub
// is unreachable from here", and the built-in templates are still perfectly
// usable while that is true.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/realsite"
	"shahrag/internal/templates"
)

// templatesClient returns the client, honouring the configured cache dir.
func (s *Server) templatesClient() *templates.Client {
	if s.tpl == nil {
		s.tpl = templates.NewClient(templateCacheDir())
	}
	return s.tpl
}

func (s *Server) templateSource(c *config.Config) templates.Source {
	ttl := time.Duration(c.RealSites.CacheHours) * time.Hour
	return templates.Source{
		Repo:   c.RealSites.Repo,
		Ref:    c.RealSites.Ref,
		Mirror: c.RealSites.Mirror,
		TTL:    ttl,
	}
}

// ── the gallery ─────────────────────────────────────────────────

// handleTemplateCatalogue lists what the repository offers, merged with what
// is already available locally so the UI can show one list with an
// "installed" flag rather than two lists the operator has to reconcile.
func (s *Server) handleTemplateCatalogue(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	cl := s.templatesClient()
	force := r.URL.Query().Get("refresh") == "1"

	// The page must render even when every mirror hangs, so the whole
	// walk is bounded well below any browser timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	res, err := cl.FetchIndex(ctx, s.templateSource(c), force)

	local := cl.List()
	installed := map[string]templates.Installed{}
	for _, l := range local {
		installed[l.ID] = l
	}

	type entry struct {
		templates.Meta
		Installed bool  `json:"installed"`
		Local     bool  `json:"local"`
		OnDisk    int64 `json:"on_disk,omitempty"`
	}
	out := []entry{}
	seen := map[string]bool{}
	// Local first: a built-in must appear even when the repository is
	// unreachable, which is the whole reason built-ins exist.
	for _, l := range local {
		out = append(out, entry{Meta: l.Meta, Installed: true, Local: true, OnDisk: l.Bytes})
		seen[l.ID] = true
	}
	var cats []templates.Category
	if res != nil && res.Index != nil {
		cats = res.Index.Categories
		for _, m := range res.Index.Templates {
			if seen[m.ID] {
				continue
			}
			out = append(out, entry{Meta: m})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Installed != out[j].Installed {
			return out[i].Installed
		}
		return out[i].ID < out[j].ID
	})

	body := map[string]interface{}{
		"templates":  out,
		"categories": mergeCategories(cats, out2ids(out)),
		"repo":       firstNonEmpty(strings.TrimSpace(c.RealSites.Repo), templates.DefaultRepo),
		"ref":        firstNonEmpty(strings.TrimSpace(c.RealSites.Ref), templates.DefaultRef),
	}
	if res != nil {
		body["stale"] = res.Stale
		body["cached"] = res.Cached
		body["mirror"] = res.Mirror
		if !res.FetchedAt.IsZero() {
			body["fetched_at"] = res.FetchedAt.UTC().Format(time.RFC3339)
		}
		if res.Err != "" {
			body["error"] = res.Err
		}
	}
	if err != nil && (res == nil || res.Err == "") {
		body["error"] = err.Error()
	}
	// Always 200: see the note at the top of the file.
	writeJSON(w, 200, body)
}

func out2ids(entries interface{}) []string { return nil } // placeholder, see mergeCategories

// mergeCategories guarantees the UI always has a category for every template,
// including the built-ins, whose categories the repository may not list.
func mergeCategories(repo []templates.Category, _ []string) []templates.Category {
	have := map[string]bool{}
	out := make([]templates.Category, 0, len(repo)+6)
	for _, c := range repo {
		if c.ID == "" || have[c.ID] {
			continue
		}
		if c.Kind == "" {
			c.Kind = templates.KindSite
		}
		have[c.ID] = true
		out = append(out, c)
	}
	for _, c := range builtinCategories {
		if have[c.ID] {
			continue
		}
		have[c.ID] = true
		out = append(out, c)
	}
	return out
}

// builtinCategories cover the templates that ship in the binary, so the
// gallery has working filters with no network at all.
var builtinCategories = []templates.Category{
	{ID: "business", Kind: templates.KindSite, Name: templates.Localised{
		"en": "Corporate", "fa": "شرکتی", "ar": "شركات", "tr": "Kurumsal"}},
	{ID: "shop", Kind: templates.KindSite, Name: templates.Localised{
		"en": "Shop", "fa": "فروشگاهی", "ar": "متجر", "tr": "Mağaza"}},
	{ID: "medical", Kind: templates.KindSite, Name: templates.Localised{
		"en": "Medical", "fa": "پزشکی", "ar": "طبي", "tr": "Sağlık"}},
	{ID: "personal", Kind: templates.KindSite, Name: templates.Localised{
		"en": "Personal", "fa": "شخصی", "ar": "شخصي", "tr": "Kişisel"}},
	{ID: "technology", Kind: templates.KindSite, Name: templates.Localised{
		"en": "Technology", "fa": "تکنولوژی", "ar": "تقنية", "tr": "Teknoloji"}},
	{ID: "education", Kind: templates.KindSite, Name: templates.Localised{
		"en": "Education", "fa": "آموزشی", "ar": "تعليم", "tr": "Eğitim"}},
	{ID: "restaurant", Kind: templates.KindSite, Name: templates.Localised{
		"en": "Food", "fa": "رستوران", "ar": "مطاعم", "tr": "Yemek"}},
	{ID: "errors", Kind: templates.KindError, Name: templates.Localised{
		"en": "Error pages", "fa": "صفحات خطا", "ar": "صفحات الخطأ", "tr": "Hata sayfaları"}},
}

// handleTemplateDetail returns everything the details drawer shows.
func (s *Server) handleTemplateDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !templates.ValidID(id) {
		writeErr(w, 400, "Invalid template id")
		return
	}
	c, _ := s.cfg.Read()
	cl := s.templatesClient()

	body := map[string]interface{}{"id": id}
	if m, ok := findLocal(cl, id); ok {
		body["meta"] = m.Meta
		body["installed"] = true
		body["on_disk"] = m.Bytes
		body["files"] = listFiles(cl, id)
		writeJSON(w, 200, body)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	res, err := cl.FetchIndex(ctx, s.templateSource(c), false)
	if err != nil || res == nil || res.Index == nil {
		writeJSON(w, 200, map[string]interface{}{
			"id": id, "installed": false, "error": errString(err)})
		return
	}
	for _, m := range res.Index.Templates {
		if m.ID == id {
			body["meta"] = m
			body["installed"] = false
			writeJSON(w, 200, body)
			return
		}
	}
	writeErr(w, 404, "Template not found")
}

func findLocal(cl *templates.Client, id string) (templates.Installed, bool) {
	for _, l := range cl.List() {
		if l.ID == id {
			return l, true
		}
	}
	return templates.Installed{}, false
}

// listFiles is the file tree shown in the details drawer, capped so a
// pathological template cannot produce a megabyte of JSON.
func listFiles(cl *templates.Client, id string) []string {
	tfs, ok := cl.OpenTemplate(id)
	if !ok {
		return nil
	}
	var out []string
	_ = fs.WalkDir(tfs, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || len(out) >= 200 {
			return nil
		}
		if strings.HasPrefix(path.Base(p), ".") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}

// handleTemplateAsset proxies a thumbnail. Proxied rather than linked
// directly because the panel page must not make the operator's BROWSER talk
// to jsDelivr: that leaks the fact that a Shahrag panel is open to whoever
// watches the browser's traffic, and on a filtered network the images would
// simply fail to load while the server can still reach them.
func (s *Server) handleTemplateAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !templates.ValidID(id) {
		writeErr(w, 400, "Invalid template id")
		return
	}
	which := r.URL.Query().Get("f")
	c, _ := s.cfg.Read()
	cl := s.templatesClient()

	// A local template serves its own thumbnail off disk, no network.
	if tfs, ok := cl.OpenTemplate(id); ok {
		name := firstNonEmpty(which, "thumb.png")
		if !safeAssetName(name) {
			writeErr(w, 400, "Invalid asset")
			return
		}
		for _, cand := range []string{name, "assets/" + name, "thumb.svg", "thumb.png"} {
			if b, err := fs.ReadFile(tfs, cand); err == nil {
				w.Header().Set("Content-Type", assetType(cand))
				w.Header().Set("Cache-Control", "private, max-age=300")
				w.Write(b)
				return
			}
		}
		// No thumbnail shipped: draw one. A gallery of grey boxes is
		// worse than a generated card, and this costs nothing.
		writePlaceholderThumb(w, id)
		return
	}

	if which == "" || !safeAssetName(which) {
		writeErr(w, 400, "Invalid asset")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, ct, err := cl.FetchAsset(ctx, s.templateSource(c), which)
	if err != nil {
		writePlaceholderThumb(w, id)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(b)
}

func safeAssetName(p string) bool {
	if p == "" || len(p) > 256 || strings.Contains(p, "..") ||
		strings.HasPrefix(p, "/") || strings.Contains(p, "://") {
		return false
	}
	return true
}

func assetType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

// writePlaceholderThumb draws a deterministic card from the id, so the same
// template always looks the same and the gallery never has holes in it.
func writePlaceholderThumb(w http.ResponseWriter, id string) {
	var h uint32 = 2166136261
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= 16777619
	}
	hue := int(h % 360)
	initial := strings.ToUpper(id[:1])
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 180">`+
		`<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">`+
		`<stop offset="0" stop-color="hsl(%d,62%%,52%%)"/>`+
		`<stop offset="1" stop-color="hsl(%d,62%%,38%%)"/></linearGradient></defs>`+
		`<rect width="320" height="180" fill="url(#g)"/>`+
		`<rect x="18" y="18" width="284" height="26" rx="6" fill="#fff" opacity=".28"/>`+
		`<rect x="18" y="58" width="150" height="12" rx="4" fill="#fff" opacity=".45"/>`+
		`<rect x="18" y="78" width="110" height="12" rx="4" fill="#fff" opacity=".3"/>`+
		`<rect x="18" y="112" width="84" height="50" rx="8" fill="#fff" opacity=".22"/>`+
		`<rect x="118" y="112" width="84" height="50" rx="8" fill="#fff" opacity=".22"/>`+
		`<rect x="218" y="112" width="84" height="50" rx="8" fill="#fff" opacity=".22"/>`+
		`<text x="300" y="46" text-anchor="end" font-family="sans-serif" font-size="26" `+
		`font-weight="700" fill="#fff" opacity=".9">%s</text></svg>`,
		hue, (hue+40)%360, initial)
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write([]byte(svg))
}

// handleTemplateInstall downloads exactly one template.
func (s *Server) handleTemplateInstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !templates.ValidID(id) {
		writeErr(w, 400, "Invalid template id")
		return
	}
	c, _ := s.cfg.Read()
	cl := s.templatesClient()
	if templates.IsBuiltin(id) {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "builtin": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := cl.FetchIndex(ctx, s.templateSource(c), false)
	if err != nil || res == nil || res.Index == nil {
		writeErr(w, 503, "The template repository is not reachable right now")
		return
	}
	var meta *templates.Meta
	for i := range res.Index.Templates {
		if res.Index.Templates[i].ID == id {
			meta = &res.Index.Templates[i]
			break
		}
	}
	if meta == nil {
		writeErr(w, 404, "Template not found")
		return
	}
	inst, err := cl.Install(ctx, s.templateSource(c), *meta)
	if err != nil {
		switch {
		case errors.Is(err, templates.ErrChecksum):
			// Worth its own message: this is the one failure that
			// means "do not trust this file", not "try again".
			writeErr(w, 502, "The download did not match its checksum and was discarded")
		case errors.Is(err, templates.ErrUnavailable):
			writeErr(w, 503, "The template repository is not reachable right now")
		default:
			writeErr(w, 500, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "id": inst.ID, "bytes": inst.Bytes, "files": inst.Files})
}

// handleTemplateRemove deletes a downloaded template.
func (s *Server) handleTemplateRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cl := s.templatesClient()
	// Refuse while a domain is using it: removing it would leave that
	// domain's next regeneration with no template, which silently drops
	// it back to the fake page.
	c, _ := s.cfg.Read()
	var users []string
	for _, d := range c.RealSiteDomains() {
		site, _ := c.EffectiveRealSite(d)
		if site.Template == id || site.ErrorTemplate == id {
			users = append(users, d)
		}
	}
	if len(users) > 0 {
		sort.Strings(users)
		writeErr(w, 409, "In use by: "+strings.Join(users, ", "))
		return
	}
	if err := cl.Remove(id); err != nil {
		if errors.Is(err, templates.ErrNotFound) {
			writeErr(w, 404, "Template not found")
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handleTemplatePreview serves a rendered page of a template so the operator
// can look at it before committing. Rendered with the DOMAIN'S OWN content
// when a domain is named, so the preview shows what will actually be served,
// not a generic demo.
func (s *Server) handleTemplatePreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !templates.ValidID(id) {
		writeErr(w, 400, "Invalid template id")
		return
	}
	cl := s.templatesClient()
	tfs, ok := cl.OpenTemplate(id)
	if !ok {
		writeErr(w, 404, "Template is not installed")
		return
	}
	page := r.URL.Query().Get("p")
	if page == "" {
		page = "index.html"
	}
	if !safeAssetName(page) {
		writeErr(w, 400, "Invalid page")
		return
	}
	b, err := fs.ReadFile(tfs, page)
	if err != nil {
		writeErr(w, 404, "Page not found")
		return
	}

	c, _ := s.cfg.Read()
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	site := c.RealSites.Defaults
	if domain != "" {
		if eff, ok := c.EffectiveRealSite(domain); ok {
			site = eff
		} else if ds, ok := c.DomainSites[domain]; ok {
			site = ds
		}
	}
	site.Template = id
	vals := realsite.Values(domain, site)
	// The preview is served from the panel, so relative asset links must
	// point back at this endpoint.
	vals["ASSET_BASE"] = "./"
	depth := strings.Count(page, "/")
	for i := 0; i < depth; i++ {
		vals["ASSET_BASE"] = "../" + vals["ASSET_BASE"]
	}
	out := realsite.Substitute(b, vals)

	w.Header().Set("Content-Type", assetOrHTML(page))
	// The preview is arbitrary HTML from a repository. It is displayed in
	// a sandboxed iframe, and CSP is the belt to that braces: no scripts,
	// no network, nothing that could reach the panel's own origin.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline' 'self'; img-src data: 'self'; font-src data:; form-action 'none'; frame-ancestors 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(out)
}

func assetOrHTML(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	return assetType(p)
}

// ── real-site settings ──────────────────────────────────────────

type realSiteSettingsReq struct {
	Repo       string           `json:"repo"`
	Ref        string           `json:"ref"`
	Mirror     string           `json:"mirror"`
	CacheHours int              `json:"cache_hours"`
	SitesDir   string           `json:"sites_dir"`
	Defaults   *config.RealSite `json:"defaults"`
}

// handleGetRealSites returns the panel-wide block plus a row per domain, so
// the page needs one request.
func (s *Server) handleGetRealSites(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	type row struct {
		Domain      string          `json:"domain"`
		Site        config.RealSite `json:"site"`
		Active      bool            `json:"active"`
		Template    string          `json:"effective_template,omitempty"`
		Root        string          `json:"root,omitempty"`
		HasCert     bool            `json:"has_cert"`
		RootTakenBy string          `json:"root_taken_by,omitempty"`
	}
	names := make([]string, 0, len(c.Domains))
	for n := range c.Domains {
		names = append(names, strings.ToLower(n))
	}
	sort.Strings(names)
	uniq := names[:0]
	var prev string
	for _, n := range names {
		if n != prev {
			uniq = append(uniq, n)
			prev = n
		}
	}

	dir := c.RealSites.SitesDirOrDefault()
	rows := make([]row, 0, len(uniq))
	for _, n := range uniq {
		rr := row{Domain: n, Site: c.DomainSites[n]}
		d := c.Domains[n]
		rr.HasCert = strings.TrimSpace(d.Cert) != "" && strings.TrimSpace(d.Key) != ""
		if eff, ok := c.EffectiveRealSite(n); ok {
			rr.Active = true
			rr.Template = eff.Template
			if strings.TrimSpace(eff.Root) != "" {
				rr.Root = eff.Root
			} else {
				rr.Root = realsite.RootFor(dir, n)
			}
		}
		// The one collision that silently changes what the operator
		// gets, so it is reported as data rather than a warning string.
		for name, svc := range c.Services {
			if svc.IsEnabled() && svc.Path == "/" {
				for _, b := range svc.Bindings {
					if strings.EqualFold(b.Domain, n) {
						rr.RootTakenBy = name
					}
				}
			}
		}
		rows = append(rows, rr)
	}

	writeJSON(w, 200, map[string]interface{}{
		"settings":     c.RealSites,
		"sites_dir":    dir,
		"domains":      rows,
		"codes":        templates.ErrorCodes(),
		"warnings":     config.RealSiteWarnings(c),
		"default_repo": templates.DefaultRepo,
	})
}

func (s *Server) handleSetRealSites(w http.ResponseWriter, r *http.Request) {
	var body realSiteSettingsReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if body.Defaults != nil {
		if err := config.ValidateRealSite(*body.Defaults); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if d := strings.TrimSpace(body.SitesDir); d != "" {
		if !strings.HasPrefix(d, "/") || strings.Contains(d, "..") {
			writeErr(w, 400, "The sites directory must be an absolute path without ..")
			return
		}
	}
	if repo := strings.TrimSpace(body.Repo); repo != "" && !validRepo(repo) {
		writeErr(w, 400, "The repository must look like owner/name")
		return
	}
	if m := strings.TrimSpace(body.Mirror); m != "" &&
		!strings.HasPrefix(m, "https://") && !strings.HasPrefix(m, "http://") {
		writeErr(w, 400, "The mirror must be a full http(s) URL")
		return
	}
	if body.CacheHours < 0 || body.CacheHours > 24*30 {
		writeErr(w, 400, "The cache lifetime must be between 0 and 720 hours")
		return
	}

	_, err := s.cfg.Mutate(func(c *config.Config) error {
		c.RealSites.Repo = strings.TrimSpace(body.Repo)
		c.RealSites.Ref = strings.TrimSpace(body.Ref)
		c.RealSites.Mirror = strings.TrimRight(strings.TrimSpace(body.Mirror), "/")
		c.RealSites.CacheHours = body.CacheHours
		c.RealSites.SitesDir = strings.TrimSpace(body.SitesDir)
		if body.Defaults != nil {
			d := *body.Defaults
			d.Mode = "" // the defaults block has no mode of its own
			d.Configured = true
			c.RealSites.Defaults = d
		}
		c.RealSites.Configured = true
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The cache directory follows the sites directory only in tests; the
	// client is rebuilt so a changed repo takes effect immediately.
	s.tpl = nil
	writeJSON(w, 200, applied(s.autoApply()))
}

func validRepo(r string) bool {
	parts := strings.Split(strings.Trim(r, "/"), "/")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 100 || strings.Contains(p, "..") {
			return false
		}
		for _, ch := range p {
			switch {
			case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z',
				ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.':
			default:
				return false
			}
		}
	}
	return true
}

// handleGetDomainSite returns one domain's block, resolved and raw, so the
// editor can show both "what you set" and "what that means".
func (s *Server) handleGetDomainSite(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	c, _ := s.cfg.Read()
	if _, ok := c.Domains[name]; !ok {
		writeErr(w, 404, "Domain not found")
		return
	}
	eff, active := c.EffectiveRealSite(name)
	writeJSON(w, 200, map[string]interface{}{
		"domain":    name,
		"site":      c.DomainSites[name],
		"effective": eff,
		"active":    active,
		"root":      realsite.RootFor(c.RealSites.SitesDirOrDefault(), name),
		"codes":     templates.ErrorCodes(),
		"defaults":  c.RealSites.Defaults,
	})
}

func (s *Server) handleSetDomainSite(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	var body config.RealSite
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	if err := config.ValidateRealSite(body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cl := s.templatesClient()
	// A template that is not installed would render nothing and drop the
	// domain silently back to the fake page, so it is refused here where
	// the operator can see why.
	for _, id := range []string{body.Template, body.ErrorTemplate} {
		if id = strings.TrimSpace(id); id != "" && !cl.Has(id) {
			writeErr(w, 400, "Template \""+id+"\" is not installed yet")
			return
		}
	}
	for code, ep := range body.ErrorPages {
		if id := strings.TrimSpace(ep.Template); id != "" && !cl.Has(id) {
			writeErr(w, 400, "Template \""+id+"\" for "+code+" is not installed yet")
			return
		}
	}

	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if _, ok := c.Domains[name]; !ok {
			return fmt.Errorf("domain not found")
		}
		if c.DomainSites == nil {
			c.DomainSites = map[string]config.RealSite{}
		}
		body.Mode = config.NormalizeRealSiteMode(body.Mode)
		body.ErrorMode = config.NormalizeErrorMode(body.ErrorMode)
		body.Configured = true
		c.DomainSites[name] = body
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "domain not found") {
			writeErr(w, 404, "Domain not found")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	out := applied(s.autoApply())
	c, _ := s.cfg.Read()
	out["warnings"] = config.RealSiteWarnings(c)
	writeJSON(w, 200, out)
}

// handleToggleDomainSite is the one-click switch on the list, which is what
// the operator uses ninety per cent of the time.
func (s *Server) handleToggleDomainSite(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	var enabled bool
	_, err := s.cfg.Mutate(func(c *config.Config) error {
		if _, ok := c.Domains[name]; !ok {
			return fmt.Errorf("domain not found")
		}
		if c.DomainSites == nil {
			c.DomainSites = map[string]config.RealSite{}
		}
		site := c.DomainSites[name]
		_, active := c.EffectiveRealSite(name)
		if active {
			// Switching off an inheriting domain must SET "off",
			// not clear Enabled: clearing it would let the
			// panel-wide default switch it straight back on, which
			// is the opposite of what the operator just asked for.
			site.Mode = config.RealSiteOff
			site.Enabled = false
		} else {
			if config.NormalizeRealSiteMode(site.Mode) == config.RealSiteOff {
				site.Mode = config.RealSiteInherit
			}
			site.Enabled = true
			if strings.TrimSpace(site.Template) == "" &&
				strings.TrimSpace(c.RealSites.Defaults.Template) == "" &&
				strings.TrimSpace(site.Root) == "" {
				// Never enable a site with nothing to serve.
				site.Template = "corporate-slate"
			}
		}
		site.Configured = true
		c.DomainSites[name] = site
		enabled = site.Enabled && config.NormalizeRealSiteMode(site.Mode) != config.RealSiteOff
		return nil
	})
	if err != nil {
		writeErr(w, 404, "Domain not found")
		return
	}
	out := applied(s.autoApply())
	out["enabled"] = enabled
	writeJSON(w, 200, out)
}

// handleRealSiteFiles lists what was actually written for a domain, because
// "it says it is on but is it really serving anything" is the first question
// anyone asks.
func (s *Server) handleRealSiteFiles(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	c, _ := s.cfg.Read()
	root := realsite.RootFor(c.RealSites.SitesDirOrDefault(), name)
	if eff, ok := c.EffectiveRealSite(name); ok && strings.TrimSpace(eff.Root) != "" {
		root = eff.Root
	}
	files, total := realsite.ListRoot(root, 400)
	writeJSON(w, 200, map[string]interface{}{
		"root": root, "files": files, "bytes": total})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// templateCacheDir is where downloaded templates and the catalogue cache
// live. Under /var/lib like the other state, not /etc: it is regenerable
// data, and a backup of /etc should not carry megabytes of websites.
func templateCacheDir() string {
	// Overridable so tests never touch the real /var/lib.
	if v := strings.TrimSpace(os.Getenv("SHAHRAG_TEMPLATE_DIR")); v != "" {
		return v
	}
	return "/var/lib/shahrag/templates"
}

// parseCode is used by the error-page editor.
func parseCode(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 400 || n > 599 {
		return 0, false
	}
	return n, true
}

var _ = json.Marshal
var _ = parseCode
