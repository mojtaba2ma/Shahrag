package realsite

// Error pages.
//
// Why the panel generates them at all
// ───────────────────────────────────
// nginx's own 404 is a grey page that says "nginx" and the version. On a
// server pretending to be an ordinary website that is the loudest tell there
// is: a real company site has a branded 404, and nobody's real site says
// "nginx/1.18.0" at the bottom. Worse, the same page appears for 502 when a
// backend is down, which tells a probe exactly when the interesting service
// stopped answering.
//
// Precedence, from strongest to weakest, and why each level exists
//  1. per-status custom HTML — the operator pasted a page for exactly this
//     status and means it;
//  2. per-status template override — a different design for this one status;
//  3. the set's error template (ErrorTemplate) — "all my error pages come
//     from this pack";
//  4. the site template's own errors/ directory — the pack that came with
//     the design, which is what most people should use;
//  5. nothing, i.e. nginx's default, when the status is switched off or the
//     whole error mode is off.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"shahrag/internal/config"
	"shahrag/internal/templates"
)

// errorDir is where generated error pages are written inside a site root.
// Under the root, not beside it, so one `root` directive serves both and
// nginx needs no second location.
const errorDir = "errors"

// writeErrorPages renders the error pages into an already-created tree.
// Returns the file count and byte total it added.
func (r *Renderer) writeErrorPages(dst string, site config.RealSite, vals map[string]string) (int, int64, error) {
	mode := config.NormalizeErrorMode(site.ErrorMode)
	if mode == config.ErrorPagesOff {
		// Remove whatever a previous render left, so switching the
		// feature off actually stops serving the pages instead of
		// leaving orphan files that a stray request could still find.
		_ = os.RemoveAll(filepath.Join(dst, errorDir))
		return 0, 0, nil
	}

	out := filepath.Join(dst, errorDir)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return 0, 0, err
	}
	// Pages inside errors/ are one directory deep.
	sub := valuesForDepth(vals, 1)

	var files int
	var total int64
	for _, code := range r.codesFor(site) {
		ep := site.ErrorPages[code]
		if ep.Disabled {
			_ = os.Remove(filepath.Join(out, code+".html"))
			continue
		}
		body, err := r.errorBody(site, code, ep, sub)
		if err != nil {
			return files, total, err
		}
		if body == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(out, code+".html"), body, 0o644); err != nil {
			return files, total, err
		}
		files++
		total += int64(len(body))
	}
	return files, total, nil
}

// codesFor is every status the site could serve a page for: the built-in list
// plus anything the operator added by hand.
func (r *Renderer) codesFor(site config.RealSite) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range templates.ErrorCodes() {
		seen[c] = true
		out = append(out, c)
	}
	for c := range site.ErrorPages {
		if !seen[c] && config.ValidErrorCode(c) {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// errorBody resolves one status through the precedence above. A nil body with
// a nil error means "no page for this status", which is not a failure.
func (r *Renderer) errorBody(site config.RealSite, code string, ep config.ErrorPage, vals map[string]string) ([]byte, error) {
	if strings.TrimSpace(ep.HTML) != "" {
		return Substitute([]byte(ep.HTML), r.errorValues(vals, code)), nil
	}
	var sources []string
	if t := strings.TrimSpace(ep.Template); t != "" {
		sources = append(sources, t)
	}
	if config.NormalizeErrorMode(site.ErrorMode) == config.ErrorPagesCustom {
		if t := strings.TrimSpace(site.ErrorTemplate); t != "" {
			sources = append(sources, t)
		}
	}
	if t := strings.TrimSpace(site.Template); t != "" {
		sources = append(sources, t)
	}
	// A last resort so a template with no errors/ directory still gets
	// branded pages instead of nginx's.
	sources = append(sources, "errors-plain")

	for _, id := range sources {
		tfs, ok := r.Client.OpenTemplate(id)
		if !ok {
			continue
		}
		for _, name := range candidateNames(code) {
			b, err := fs.ReadFile(tfs, errorDir+"/"+name)
			if err != nil {
				continue
			}
			return Substitute(b, r.errorValues(vals, code)), nil
		}
	}
	return nil, nil
}

// candidateNames lets a template supply one page for a family, the way
// nginx's own packaging does with 50x.html.
func candidateNames(code string) []string {
	names := []string{code + ".html"}
	if strings.HasPrefix(code, "5") {
		names = append(names, "50x.html")
	}
	if strings.HasPrefix(code, "4") {
		names = append(names, "40x.html")
	}
	return names
}

// errorValues adds the per-status strings a page needs.
func (r *Renderer) errorValues(vals map[string]string, code string) map[string]string {
	out := make(map[string]string, len(vals)+8)
	for k, v := range vals {
		out[k] = v
	}
	lang := vals["LANG"]
	out["CODE"] = code
	out["E_HOME"] = tr(lang, "e_home")
	out["E_BACK"] = tr(lang, "e_back")
	// Both the generic and the code-specific keys, because a downloaded
	// template may use either.
	out["ERROR_TITLE"] = errTitle(lang, code)
	out["ERROR_TEXT"] = errText(lang, code)
	out["E"+code+"_TITLE"] = out["ERROR_TITLE"]
	out["E"+code+"_TEXT"] = out["ERROR_TEXT"]
	return out
}

// renderErrorsOnly handles a site pointed at the operator's own directory:
// their files are never touched, but they still get branded error pages,
// written into a private directory the generator points nginx at.
func (r *Renderer) renderErrorsOnly(domain string, site config.RealSite) (map[string]string, error) {
	if config.NormalizeErrorMode(site.ErrorMode) == config.ErrorPagesOff {
		return nil, nil
	}
	dir := filepath.Join(r.Dir, "_errors", safeDomainDir(domain))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	vals := Values(domain, site)
	// Depth 0: these pages are served from their own root, so assets sit
	// beside them. There are none, since a standalone error page inlines
	// what it needs — but a downloaded pack may ship a stylesheet, so
	// copy the pack's assets directory when it has one.
	if err := r.copyErrorAssets(site, dir); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, code := range r.codesFor(site) {
		ep := site.ErrorPages[code]
		if ep.Disabled {
			_ = os.Remove(filepath.Join(dir, code+".html"))
			continue
		}
		body, err := r.errorBody(site, code, ep, vals)
		if err != nil {
			return nil, err
		}
		if body == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, code+".html"), body, 0o644); err != nil {
			return nil, err
		}
		out[code] = code + ".html"
	}
	_ = os.Chmod(dir, 0o755)
	return out, nil
}

func (r *Renderer) copyErrorAssets(site config.RealSite, dir string) error {
	id := strings.TrimSpace(site.ErrorTemplate)
	if id == "" {
		id = strings.TrimSpace(site.Template)
	}
	if id == "" {
		id = "errors-plain"
	}
	tfs, ok := r.Client.OpenTemplate(id)
	if !ok {
		return nil
	}
	entries, err := fs.ReadDir(tfs, "assets")
	if err != nil {
		return nil // no assets directory; nothing to do
	}
	target := filepath.Join(dir, "assets")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	vals := Values("", site)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, rerr := fs.ReadFile(tfs, "assets/"+e.Name())
		if rerr != nil {
			continue
		}
		if textExt[strings.ToLower(filepath.Ext(e.Name()))] {
			b = Substitute(b, vals)
		}
		if err := os.WriteFile(filepath.Join(target, e.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// errorPageMap reports which status codes actually have a file on disk, so
// the nginx generator only emits an error_page directive for pages that
// exist. Pointing error_page at a missing file turns a 404 into a 500, which
// is the single worst outcome this feature could produce.
func (r *Renderer) errorPageMap(root string, site config.RealSite) map[string]string {
	if config.NormalizeErrorMode(site.ErrorMode) == config.ErrorPagesOff {
		return nil
	}
	dir := filepath.Join(root, errorDir)
	if strings.TrimSpace(site.Root) != "" {
		return nil // filled by renderErrorsOnly
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		code := strings.TrimSuffix(e.Name(), ".html")
		if !config.ValidErrorCode(code) {
			continue
		}
		out[code] = errorDir + "/" + e.Name()
	}
	return out
}

// Prune deletes rendered sites for domains that no longer have one, so
// turning the feature off for a domain actually reclaims the disk instead of
// leaving a website nobody knows is still there.
func (r *Renderer) Prune(keep map[string]bool) (int, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Name() == "_errors" {
			continue
		}
		if keep[e.Name()] {
			continue
		}
		// Only remove a directory this package created — identified by
		// its stamp file. Never delete something an operator put here.
		if _, err := os.Stat(filepath.Join(r.Dir, e.Name(), stampFile)); err != nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(r.Dir, e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// RenderAll renders every domain that has a real site and prunes the rest.
// Returns one Result per domain, sorted by domain.
func (r *Renderer) RenderAll(c *config.Config) ([]Result, error) {
	var out []Result
	keep := map[string]bool{}
	var firstErr error
	for _, d := range c.RealSiteDomains() {
		site, ok := c.EffectiveRealSite(d)
		if !ok {
			continue
		}
		res, err := r.Render(d, site)
		if err != nil {
			// One broken domain must not stop the others: the
			// generator is about to write a config for all of them
			// and a missing template is a per-domain problem.
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", d, err)
			}
			continue
		}
		keep[safeDomainDir(d)] = true
		out = append(out, res)
	}
	if _, err := r.Prune(keep); err != nil && firstErr == nil {
		firstErr = err
	}
	return out, firstErr
}
