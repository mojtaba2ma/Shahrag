package realsite

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/templates"
)

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	return &Renderer{
		Dir:    filepath.Join(t.TempDir(), "sites"),
		Client: templates.NewClient(t.TempDir()),
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// ── the switch ──────────────────────────────────────────────────

// Off by default, for every domain, on an untouched config. Turning a
// feature on by upgrading is the one thing a proxy panel must never do.
func TestOffByDefault(t *testing.T) {
	c := config.Default()
	c.Domains["a.example"] = config.Domain{Cert: "/c", Key: "/k"}
	if _, ok := c.EffectiveRealSite("a.example"); ok {
		t.Fatal("a fresh config must serve no real site")
	}
	if c.AnyRealSite() {
		t.Fatal("AnyRealSite must be false on a fresh config")
	}
}

// The core per-domain requirement: one domain on, the others untouched.
func TestPerDomainIndependence(t *testing.T) {
	c := config.Default()
	for _, d := range []string{"one.example", "two.example", "three.example"} {
		c.Domains[d] = config.Domain{Cert: "/c", Key: "/k"}
	}
	c.DomainSites = map[string]config.RealSite{
		"one.example": {Enabled: true, Template: "corporate-slate"},
	}
	got := c.RealSiteDomains()
	if len(got) != 1 || got[0] != "one.example" {
		t.Fatalf("only one.example should be active, got %v", got)
	}
}

// An operator who switched a domain OFF must keep it off when the
// panel-wide default is later switched on. Anything else silently exposes
// the domain that hosts the panel.
func TestExplicitOffBeatsTheDefault(t *testing.T) {
	c := config.Default()
	c.Domains["panel.example"] = config.Domain{Cert: "/c", Key: "/k"}
	c.Domains["front.example"] = config.Domain{Cert: "/c", Key: "/k"}
	c.RealSites.Defaults = config.RealSite{Enabled: true, Template: "tech-night"}
	c.DomainSites = map[string]config.RealSite{
		"panel.example": {Mode: config.RealSiteOff},
	}
	if _, ok := c.EffectiveRealSite("panel.example"); ok {
		t.Error("an explicit off must survive the panel-wide default being on")
	}
	if _, ok := c.EffectiveRealSite("front.example"); !ok {
		t.Error("a domain with no opinion should inherit the default")
	}
}

// A custom domain overrides the template but inherits the address and phone
// number it did not re-type.
func TestCustomInheritsUnsetFields(t *testing.T) {
	c := config.Default()
	c.Domains["a.example"] = config.Domain{Cert: "/c", Key: "/k"}
	c.RealSites.Defaults = config.RealSite{
		Enabled: true, Template: "corporate-slate",
		Content: config.RealSiteContent{Phone: "+1 555", Address: "HQ", SiteName: "Default Co"},
	}
	c.DomainSites = map[string]config.RealSite{
		"a.example": {Enabled: true, Mode: config.RealSiteCustom,
			Template: "clinic-calm",
			Content:  config.RealSiteContent{SiteName: "Clinic A"}},
	}
	eff, ok := c.EffectiveRealSite("a.example")
	if !ok {
		t.Fatal("should be active")
	}
	if eff.Template != "clinic-calm" {
		t.Errorf("template not overridden: %s", eff.Template)
	}
	if eff.Content.SiteName != "Clinic A" {
		t.Errorf("name not overridden: %s", eff.Content.SiteName)
	}
	if eff.Content.Phone != "+1 555" || eff.Content.Address != "HQ" {
		t.Errorf("unset fields should inherit: %+v", eff.Content)
	}
}

// One domain on while the panel-wide default is off — the usual first step.
func TestSingleDomainWithoutTouchingDefaults(t *testing.T) {
	c := config.Default()
	c.Domains["a.example"] = config.Domain{Cert: "/c", Key: "/k"}
	c.Domains["b.example"] = config.Domain{Cert: "/c", Key: "/k"}
	c.DomainSites = map[string]config.RealSite{
		"a.example": {Enabled: true, Template: "shop-bright"},
	}
	eff, ok := c.EffectiveRealSite("a.example")
	if !ok {
		t.Fatal("a.example should be on")
	}
	// The template the domain chose must SURVIVE. Checking only that the
	// domain is "active" is what let a bug through in which the resolved
	// site came back with an empty template, rendered nothing, and left
	// the domain quietly serving the old fake page.
	if eff.Template != "shop-bright" {
		t.Errorf("the domain's own template was discarded: %q", eff.Template)
	}
	if _, ok := c.EffectiveRealSite("b.example"); ok {
		t.Error("b.example must stay off")
	}
}

// The same for content: a domain that inherits but names itself must keep
// its name, and still inherit the address it did not type.
func TestInheritMergesRatherThanReplaces(t *testing.T) {
	c := config.Default()
	c.Domains["a.example"] = config.Domain{Cert: "/c", Key: "/k"}
	c.RealSites.Defaults = config.RealSite{
		Enabled: true, Template: "corporate-slate",
		Content: config.RealSiteContent{Address: "HQ", Phone: "+1 555"},
	}
	c.DomainSites = map[string]config.RealSite{
		"a.example": {Enabled: true, Template: "tech-night",
			Content: config.RealSiteContent{SiteName: "Only Mine"}},
	}
	eff, ok := c.EffectiveRealSite("a.example")
	if !ok {
		t.Fatal("should be active")
	}
	if eff.Template != "tech-night" {
		t.Errorf("the domain's template was lost: %q", eff.Template)
	}
	if eff.Content.SiteName != "Only Mine" {
		t.Errorf("the domain's name was lost: %q", eff.Content.SiteName)
	}
	if eff.Content.Address != "HQ" || eff.Content.Phone != "+1 555" {
		t.Errorf("fields it did not set should still inherit: %+v", eff.Content)
	}
}

// ── rendering ───────────────────────────────────────────────────

func TestRenderProducesACompleteSite(t *testing.T) {
	r := newRenderer(t)
	site := config.RealSite{Enabled: true, Template: "corporate-slate",
		Content: config.RealSiteContent{SiteName: "Acme Ltd", Language: "en"}}
	res, err := r.Render("acme.example", site)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"index.html", "about.html", "services.html",
		"contact.html", "assets/site.css", "robots.txt", "sitemap.xml",
		"errors/404.html", "errors/502.html"} {
		if _, err := os.Stat(filepath.Join(res.Root, want)); err != nil {
			t.Errorf("%s missing from the rendered site", want)
		}
	}
	idx := read(t, filepath.Join(res.Root, "index.html"))
	if !strings.Contains(idx, "Acme Ltd") {
		t.Error("the site name was not substituted")
	}
	t.Logf("rendered %d files, %d bytes", res.Files, res.Bytes)
}

// A visitor seeing {{HERO_TITLE}} is the single most obvious tell that a
// site is machine-generated, and it is what an unsubstituted placeholder
// produces.
func TestNoPlaceholderSurvivesRendering(t *testing.T) {
	r := newRenderer(t)
	for _, id := range []string{"corporate-slate", "shop-bright", "clinic-calm",
		"personal-ink", "tech-night"} {
		res, err := r.Render("x-"+id+".example", config.RealSite{
			Enabled: true, Template: id})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		filepath.WalkDir(res.Root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !textExt[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			b := read(t, p)
			if m := placeholder.FindString(b); m != "" {
				rel, _ := filepath.Rel(res.Root, p)
				t.Errorf("%s/%s still contains %s", id, rel, m)
			}
			return nil
		})
	}
}

// Operator text lands inside HTML. The operator is trusted, but the value
// can also come from a restored backup or a hand-edited config, and the
// TEMPLATE decides where it goes — including inside an attribute.
func TestContentIsEscaped(t *testing.T) {
	r := newRenderer(t)
	res, err := r.Render("evil.example", config.RealSite{
		Enabled: true, Template: "corporate-slate",
		Content: config.RealSiteContent{
			SiteName:  `<script>alert(1)</script>`,
			HeroTitle: `" onload="alert(2)`,
		}})
	if err != nil {
		t.Fatal(err)
	}
	idx := read(t, filepath.Join(res.Root, "index.html"))
	if strings.Contains(idx, "<script>alert(1)</script>") {
		t.Error("a script tag in the site name reached the page unescaped")
	}
	if strings.Contains(idx, `onload="alert(2)`) {
		t.Error("an attribute break reached the page unescaped")
	}
	if !strings.Contains(idx, "&lt;script&gt;") {
		t.Error("the text should still be visible, escaped")
	}
}

// Persian content must produce a right-to-left page, or it is not a
// Persian site.
func TestRTLFromLanguage(t *testing.T) {
	r := newRenderer(t)
	res, err := r.Render("fa.example", config.RealSite{
		Enabled: true, Template: "corporate-slate",
		Content: config.RealSiteContent{Language: "fa", SiteName: "نمونه"}})
	if err != nil {
		t.Fatal(err)
	}
	idx := read(t, filepath.Join(res.Root, "index.html"))
	if !strings.Contains(idx, `dir="rtl"`) || !strings.Contains(idx, `lang="fa"`) {
		t.Error("a Persian site must be lang=fa dir=rtl")
	}
	if !strings.Contains(idx, "خانه") {
		t.Error("the navigation should be in Persian")
	}
	e404 := read(t, filepath.Join(res.Root, "errors", "404.html"))
	if !strings.Contains(e404, "این صفحه پیدا نشد") {
		t.Error("the error page should be in Persian too")
	}
}

// The generator runs on EVERY config change. Re-rendering an unchanged site
// churns files nginx is serving, so it must be skipped.
func TestRenderIsIdempotent(t *testing.T) {
	r := newRenderer(t)
	site := config.RealSite{Enabled: true, Template: "tech-night",
		Content: config.RealSiteContent{SiteName: "Same"}}
	first, err := r.Render("same.example", site)
	if err != nil {
		t.Fatal(err)
	}
	if first.Skipped {
		t.Fatal("the first render must not be skipped")
	}
	idxPath := filepath.Join(first.Root, "index.html")
	st1, _ := os.Stat(idxPath)

	second, err := r.Render("same.example", site)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Skipped {
		t.Error("an unchanged site must be skipped, not rewritten")
	}
	st2, _ := os.Stat(idxPath)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("the file was rewritten despite nothing changing")
	}

	// A content change must re-render.
	site.Content.SiteName = "Different"
	third, err := r.Render("same.example", site)
	if err != nil {
		t.Fatal(err)
	}
	if third.Skipped {
		t.Error("a content change must trigger a re-render")
	}
	if !strings.Contains(read(t, idxPath), "Different") {
		t.Error("the new content did not land")
	}
}

// ── error pages ─────────────────────────────────────────────────

// Precedence is the whole design of the error-page editor.
func TestErrorPagePrecedence(t *testing.T) {
	r := newRenderer(t)
	site := config.RealSite{
		Enabled: true, Template: "corporate-slate",
		ErrorMode:     config.ErrorPagesCustom,
		ErrorTemplate: "errors-dark",
		ErrorPages: map[string]config.ErrorPage{
			"404": {HTML: "<html><body>MY OWN 404 for {{SITE_NAME}}</body></html>"},
			"403": {Template: "errors-plain"},
			"429": {Disabled: true},
		},
		Content: config.RealSiteContent{SiteName: "Prec"},
	}
	res, err := r.Render("prec.example", site)
	if err != nil {
		t.Fatal(err)
	}
	e404 := read(t, filepath.Join(res.Root, "errors", "404.html"))
	if !strings.Contains(e404, "MY OWN 404 for Prec") {
		t.Errorf("custom HTML should win and be substituted: %q", e404)
	}
	if _, err := os.Stat(filepath.Join(res.Root, "errors", "429.html")); err == nil {
		t.Error("a disabled status must have no page at all")
	}
	if _, ok := res.ErrorPages["429"]; ok {
		t.Error("a disabled status must not be reported to nginx")
	}
	if _, ok := res.ErrorPages["502"]; !ok {
		t.Error("502 should come from the error template")
	}
}

// error_page pointing at a file that does not exist turns a 404 into a 500 —
// the worst possible outcome of this feature.
func TestErrorPagesReportedOnlyWhenTheFileExists(t *testing.T) {
	r := newRenderer(t)
	res, err := r.Render("exists.example", config.RealSite{
		Enabled: true, Template: "corporate-slate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ErrorPages) == 0 {
		t.Fatal("no error pages were reported")
	}
	for code, rel := range res.ErrorPages {
		if _, err := os.Stat(filepath.Join(res.Root, rel)); err != nil {
			t.Errorf("reported %s -> %s but the file is not there", code, rel)
		}
	}
}

// Switching error pages off must remove the files, not orphan them.
func TestErrorPagesOffRemovesTheFiles(t *testing.T) {
	r := newRenderer(t)
	site := config.RealSite{Enabled: true, Template: "corporate-slate"}
	res, _ := r.Render("off.example", site)
	if _, err := os.Stat(filepath.Join(res.Root, "errors", "404.html")); err != nil {
		t.Fatal("setup: no 404 page")
	}
	site.ErrorMode = config.ErrorPagesOff
	res2, err := r.Render("off.example", site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res2.Root, "errors")); err == nil {
		t.Error("the errors directory should be gone")
	}
	if len(res2.ErrorPages) != 0 {
		t.Error("nothing should be reported to nginx")
	}
}

// An operator's own directory must never be written into.
func TestOwnRootIsNeverTouched(t *testing.T) {
	own := t.TempDir()
	os.WriteFile(filepath.Join(own, "index.html"), []byte("MY SITE"), 0o644)
	r := newRenderer(t)
	res, err := r.Render("own.example", config.RealSite{
		Enabled: true, Root: own, Template: "corporate-slate"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Root != own {
		t.Errorf("root should be the operator's: %s", res.Root)
	}
	if read(t, filepath.Join(own, "index.html")) != "MY SITE" {
		t.Fatal("the operator's own file was overwritten")
	}
	entries, _ := os.ReadDir(own)
	if len(entries) != 1 {
		t.Errorf("nothing should have been added to the operator's directory: %d entries", len(entries))
	}
	if len(res.ErrorPages) == 0 {
		t.Error("error pages should still be generated, elsewhere")
	}
}

// ── pruning ─────────────────────────────────────────────────────

// Turning the last domain off must reclaim the disk, not leave a live
// website nobody knows about.
func TestPruneRemovesDisabledSitesButNotStrangers(t *testing.T) {
	r := newRenderer(t)
	if _, err := r.Render("gone.example", config.RealSite{
		Enabled: true, Template: "corporate-slate"}); err != nil {
		t.Fatal(err)
	}
	// Something the operator put here by hand: must survive.
	stranger := filepath.Join(r.Dir, "handmade")
	os.MkdirAll(stranger, 0o755)
	os.WriteFile(filepath.Join(stranger, "index.html"), []byte("mine"), 0o644)

	n, err := r.Prune(map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 removal, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "gone.example")); err == nil {
		t.Error("the disabled site is still on disk")
	}
	if _, err := os.Stat(filepath.Join(stranger, "index.html")); err != nil {
		t.Error("a directory this package did not create was deleted")
	}
}

// A domain name goes straight into an nginx root directive.
func TestDomainDirIsSafe(t *testing.T) {
	cases := map[string]string{
		"a.example":      "a.example",
		"A.EXAMPLE":      "a.example",
		"../../etc":      "_._etc",
		"*.wild.example": "_wild_.wild.example",
		"a b;{}":         "a_b___",
		"":               "_unnamed",
	}
	for in, want := range cases {
		if got := safeDomainDir(in); got != want {
			t.Errorf("safeDomainDir(%q) = %q, want %q", in, got, want)
		}
		got := safeDomainDir(in)
		if strings.Contains(got, "..") || strings.ContainsAny(got, "/\\;{} ") {
			t.Errorf("safeDomainDir(%q) = %q is not safe in a config file", in, got)
		}
	}
}

// ── cost ────────────────────────────────────────────────────────

// The generator calls this on every config change, so the skip path is the
// one that matters. A full render is allowed to be slow; a no-op is not.
func TestRenderCost(t *testing.T) {
	r := newRenderer(t)
	site := config.RealSite{Enabled: true, Template: "corporate-slate",
		Content: config.RealSiteContent{SiteName: "Cost"}}

	start := time.Now()
	first, err := r.Render("cost.example", site)
	if err != nil {
		t.Fatal(err)
	}
	full := time.Since(start)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start = time.Now()
	const n = 100
	for i := 0; i < n; i++ {
		if res, err := r.Render("cost.example", site); err != nil || !res.Skipped {
			t.Fatalf("iteration %d: skipped=%v err=%v", i, res.Skipped, err)
		}
	}
	per := time.Since(start) / n
	runtime.ReadMemStats(&after)

	t.Logf("full render: %v for %d files / %d bytes", full, first.Files, first.Bytes)
	t.Logf("unchanged render: %v per call, %d allocs per call",
		per, (after.Mallocs-before.Mallocs)/n)

	// Measured at ~110 µs; the budget is generous enough not to be flaky
	// on a loaded shared runner but tight enough to catch a regression
	// that starts re-reading every template file.
	if per > 3*time.Millisecond {
		t.Errorf("an unchanged site costs %v to re-check — the generator does this on every save", per)
	}
	if full > 500*time.Millisecond {
		t.Errorf("a full render takes %v", full)
	}
}

// ── validation ──────────────────────────────────────────────────

func TestValidationRejectsDangerousRoots(t *testing.T) {
	for _, bad := range []string{"/", "/etc", "/etc/", "relative/path", "/var/www/../../etc", "/root"} {
		if err := config.ValidateRealSite(config.RealSite{Root: bad}); err == nil {
			t.Errorf("root %q should be refused", bad)
		}
	}
	if err := config.ValidateRealSite(config.RealSite{Root: "/var/www/mysite"}); err != nil {
		t.Errorf("a normal root was refused: %v", err)
	}
}

// Extra nginx config must not be able to open a new server block or include
// an arbitrary file.
func TestValidationRejectsEscapingDirectives(t *testing.T) {
	for _, bad := range []string{
		"server { listen 81; }",
		"include /etc/passwd;",
		"load_module foo.so;",
		"  user root;",
	} {
		if err := config.ValidateRealSite(config.RealSite{ExtraConfig: bad}); err == nil {
			t.Errorf("extra config %q should be refused", bad)
		}
	}
	ok := "add_header X-Frame-Options SAMEORIGIN;\n# a comment\nexpires 1h;"
	if err := config.ValidateRealSite(config.RealSite{ExtraConfig: ok}); err != nil {
		t.Errorf("ordinary directives were refused: %v", err)
	}
}

func TestValidErrorCode(t *testing.T) {
	for _, ok := range []string{"400", "404", "500", "599"} {
		if !config.ValidErrorCode(ok) {
			t.Errorf("%s should be valid", ok)
		}
	}
	for _, bad := range []string{"", "200", "301", "99", "1000", "4o4", "404 "} {
		if config.ValidErrorCode(strings.TrimSpace(bad)) && bad != "404 " {
			t.Errorf("%q should be refused", bad)
		}
	}
}
