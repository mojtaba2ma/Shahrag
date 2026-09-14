package nginx

import (
	"strings"
	"testing"

	"shahrag/internal/config"
)

// The feature must be completely inert until someone turns it on: the same
// bytes a version without it would have written.
func TestNoPlanMeansNoChange(t *testing.T) {
	if got := RealSiteLocations(nil); got != "" {
		t.Errorf("a nil plan must produce nothing, got %q", got)
	}
	if got := RealSiteLocations(&RealSitePlan{}); got != "" {
		t.Errorf("an empty plan must produce nothing, got %q", got)
	}
}

// error_page pointing at a file that does not exist turns a 404 into a 500.
func TestNoErrorPagesMeansNoErrorPageDirective(t *testing.T) {
	out := RealSiteLocations(&RealSitePlan{Root: "/var/www/x"})
	if strings.Contains(out, "error_page") {
		t.Errorf("no pages were reported, so no directive may be emitted:\n%s", out)
	}
	if !strings.Contains(out, "root /var/www/x;") {
		t.Errorf("the root is missing:\n%s", out)
	}
}

// Eleven error_page lines where three would do is eleven lines nginx parses
// on every reload. Codes sharing a file are grouped.
func TestErrorPagesAreGrouped(t *testing.T) {
	out := RealSiteLocations(&RealSitePlan{
		Root: "/var/www/x",
		ErrorPages: map[string]string{
			"500": "errors/50x.html",
			"502": "errors/50x.html",
			"503": "errors/50x.html",
			"504": "errors/50x.html",
			"404": "errors/404.html",
		},
	})
	lines := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "error_page") {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("expected 2 grouped error_page lines, got %d:\n%s", lines, out)
	}
	if !strings.Contains(out, "error_page 500 502 503 504 /__shg_err/errors/50x.html;") {
		t.Errorf("the 5xx family was not grouped:\n%s", out)
	}
}

// Without `internal`, anyone can request /__shg_err/502.html directly and get
// a 200 — untidy, and a fingerprint of exactly this panel.
func TestErrorLocationIsInternal(t *testing.T) {
	out := RealSiteLocations(&RealSitePlan{
		Root:       "/var/www/x",
		ErrorPages: map[string]string{"404": "errors/404.html"},
	})
	i := strings.Index(out, "location ^~ /__shg_err/")
	if i < 0 {
		t.Fatalf("no error location:\n%s", out)
	}
	tail := out[i:]
	if len(tail) > 120 {
		tail = tail[:120]
	}
	if !strings.Contains(tail, "internal;") {
		t.Errorf("the error location must be internal:\n%s", tail)
	}
}

// try_files must NOT fall back to index.html: a single-page fallback answers
// 200 for every path a scanner invents, which both looks machine-generated
// and hides real 404s from the statistics page.
func TestTryFilesDoesNotSwallowEverything(t *testing.T) {
	out := RealSiteLocations(&RealSitePlan{Root: "/var/www/x"})
	if strings.Contains(out, "try_files $uri $uri/ /index.html") {
		t.Errorf("the real site must not answer 200 for every invented path:\n%s", out)
	}
	if !strings.Contains(out, "try_files $uri $uri/ =404;") {
		t.Errorf("expected a =404 fallback:\n%s", out)
	}
}

// The generator must not do file I/O: with no hook it produces exactly what
// it always produced.
func TestGeneratorWithoutHookIsUnchanged(t *testing.T) {
	c := config.Default()
	c.Domains["a.example"] = config.Domain{Cert: "/tmp/c.pem", Key: "/tmp/k.pem"}
	c.Services["web"] = config.Service{
		LocalPort: 3000, ListenPort: 443, Path: "/app",
		Bindings: []config.Binding{{Domain: "a.example", Subdomain: "www"}},
	}
	g := &Generator{}
	plans, err := g.realSitePlans(c)
	if err != nil || plans != nil {
		t.Errorf("a nil hook must be inert: %v %v", plans, err)
	}
}

// An operator's directives are re-indented and blank lines dropped, so a
// pasted block does not wreck the generated file's readability.
func TestExtraConfigIsIndented(t *testing.T) {
	out := RealSiteLocations(&RealSitePlan{
		Root:        "/var/www/x",
		ExtraConfig: "add_header X-A 1;\n\n   add_header X-B 2;\n",
	})
	if !strings.Contains(out, "        add_header X-A 1;\n        add_header X-B 2;\n") {
		t.Errorf("directives were not normalised:\n%s", out)
	}
}
