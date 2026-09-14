package realsite

// The bridge between the renderer and the nginx generator.
//
// The generator deliberately does no file I/O of its own for this feature —
// it takes a plan and writes text. This function is the only place that knows
// both sides, which keeps the generator's tests free of disks and template
// stores and makes the failure modes easy to reason about: rendering happens
// first, completely, and only what succeeded is described to nginx.

import (
	"path/filepath"
	"strings"

	"shahrag/internal/config"
	"shahrag/internal/nginx"
)

// PlanHook returns a function suitable for Generator.RealSites.
func PlanHook(r *Renderer) func(*config.Config) (map[string]*nginx.RealSitePlan, error) {
	return func(c *config.Config) (map[string]*nginx.RealSitePlan, error) {
		return Plan(r, c)
	}
}

// Plan renders every enabled domain and describes the result to nginx.
//
// A domain that fails to render is simply absent from the map, so the
// generator falls back to the previous fake page for it. That is the
// conservative choice: pointing nginx at a root that was never written turns
// every request for that domain into a 403, which is both a broken site and
// exactly the "this server refuses things" signature to avoid.
func Plan(r *Renderer, c *config.Config) (map[string]*nginx.RealSitePlan, error) {
	if r == nil || !c.AnyRealSite() {
		// Still prune, so switching the last domain off reclaims the
		// disk instead of leaving a live website behind.
		if r != nil {
			_, _ = r.Prune(map[string]bool{})
		}
		return nil, nil
	}
	results, err := r.RenderAll(c)
	out := make(map[string]*nginx.RealSitePlan, len(results))
	for _, res := range results {
		site, ok := c.EffectiveRealSite(res.Domain)
		if !ok {
			continue
		}
		plan := &nginx.RealSitePlan{
			Root:        res.Root,
			Index:       site.Index,
			CacheAssets: site.CacheAssets,
			ExtraConfig: site.ExtraConfig,
			ErrorPages:  map[string]string{},
		}
		for code, rel := range res.ErrorPages {
			plan.ErrorPages[code] = rel
		}
		// A site serving the operator's own directory keeps its error
		// pages in a directory of ours, so nginx needs a second root
		// for them.
		if strings.TrimSpace(site.Root) != "" {
			dir := filepath.Join(r.Dir, "_errors", safeDomainDir(res.Domain))
			plan.ErrorRoot = dir
			for code, rel := range res.ErrorPages {
				plan.ErrorPages[code] = rel
			}
		}
		if len(plan.ErrorPages) == 0 {
			plan.ErrorPages = nil
		}
		out[strings.ToLower(res.Domain)] = plan
	}
	if len(out) == 0 {
		return nil, err
	}
	return out, err
}
