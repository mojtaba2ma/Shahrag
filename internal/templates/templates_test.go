package templates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── helpers ─────────────────────────────────────────────────────

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ── the built-ins ───────────────────────────────────────────────

// The built-ins are the reason the feature works with no network, so their
// existence and completeness is a behaviour worth locking, not a detail.
func TestBuiltinsAreComplete(t *testing.T) {
	metas := BuiltinMetas()
	if len(metas) < 5 {
		t.Fatalf("expected at least 5 built-in templates, got %d", len(metas))
	}
	sites := 0
	for _, m := range metas {
		if !m.Builtin {
			t.Errorf("%s: Builtin flag not set", m.ID)
		}
		if m.Name.Pick("en") == "" || m.Name.Pick("fa") == "" {
			t.Errorf("%s: missing en or fa name", m.ID)
		}
		tfs, ok := BuiltinFS(m.ID)
		if !ok {
			t.Fatalf("%s: no embedded filesystem", m.ID)
		}
		if m.Kind == KindSite {
			sites++
			for _, p := range m.Pages {
				if _, err := tfs.Open(p); err != nil {
					t.Errorf("%s: page %s missing: %v", m.ID, p, err)
				}
			}
		}
		// Every template must supply every advertised error page, or
		// the panel offers a status code it cannot actually serve.
		for _, code := range m.ErrorPages {
			if _, err := tfs.Open("errors/" + code + ".html"); err != nil {
				t.Errorf("%s: error page %s missing", m.ID, code)
			}
		}
	}
	if sites < 3 {
		t.Errorf("expected at least 3 site templates, got %d", sites)
	}
}

// A template that reaches out to a CDN is both a privacy leak and a blank
// page on a filtered network — the exact network this panel exists for.
func TestBuiltinsMakeNoExternalRequests(t *testing.T) {
	bad := []string{"http://", "https://", "//fonts.", "cdn.", "googleapis"}
	for _, m := range BuiltinMetas() {
		tfs, _ := BuiltinFS(m.ID)
		err := walkFiles(tfs, func(p string, b []byte) {
			if !strings.HasSuffix(p, ".html") && !strings.HasSuffix(p, ".css") {
				return
			}
			// The SVG XML namespace is a URI, not a URL: nothing
			// ever fetches it, and every inline <svg> carries it.
			s := strings.ReplaceAll(string(b), "http://www.w3.org/2000/svg", "")
			// The sitemap legitimately contains the site's own URL
			// placeholder, which is the operator's own address.
			s = strings.ReplaceAll(s, "{{SITE_URL}}", "")
			for _, needle := range bad {
				if idx := strings.Index(s, needle); idx >= 0 {
					end := idx + 60
					if end > len(s) {
						end = len(s)
					}
					t.Errorf("%s/%s contains %q — it would fail on a filtered network: %q",
						m.ID, p, needle, s[idx:end])
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The embedded size is paid for by every user on every upgrade, so it is
// locked as behaviour. The first cut inlined the CSS into all eleven error
// pages and came to 506 KB; sharing one stylesheet brought it to 168 KB.
func TestBuiltinFootprint(t *testing.T) {
	var total int64
	var files int
	for _, m := range BuiltinMetas() {
		total += m.Size
		files += m.Files
	}
	const budget = 220 << 10
	t.Logf("built-in templates: %d files, %d bytes (%.1f KB)", files, total, float64(total)/1024)
	if total > budget {
		t.Errorf("built-in templates are %d bytes, over the %d byte budget", total, budget)
	}
}

func walkFiles(f fs.FS, fn func(string, []byte)) error {
	return fs.WalkDir(f, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := fs.ReadFile(f, p)
		if rerr != nil {
			return rerr
		}
		fn(p, b)
		return nil
	})
}

// ── the index ───────────────────────────────────────────────────

// A malformed or hostile index entry must be dropped, not passed to the UI
// where it fails later with a confusing message — or worse, installed.
func TestIndexRejectsUnsafeEntries(t *testing.T) {
	good := sum([]byte("x"))
	raw := fmt.Sprintf(`{"schema":1,"templates":[
	 {"id":"ok","archive":"t/ok.tar.gz","sha256":%q},
	 {"id":"../escape","archive":"t/a.tar.gz","sha256":%q},
	 {"id":"no-sum","archive":"t/b.tar.gz"},
	 {"id":"abs","archive":"/etc/passwd","sha256":%q},
	 {"id":"url","archive":"https://evil/x.tar.gz","sha256":%q},
	 {"id":"traverse","archive":"../../x.tar.gz","sha256":%q},
	 {"id":"Bad-Case","archive":"t/c.tar.gz","sha256":%q},
	 {"id":"badkind","kind":"exe","archive":"t/d.tar.gz","sha256":%q},
	 {"id":"ok","archive":"t/dupe.tar.gz","sha256":%q}
	]}`, good, good, good, good, good, good, good, good)

	idx, err := parseIndex([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Templates) != 1 {
		var ids []string
		for _, m := range idx.Templates {
			ids = append(ids, m.ID)
		}
		t.Fatalf("expected only the safe entry to survive, got %v", ids)
	}
	if idx.Templates[0].ID != "ok" {
		t.Errorf("wrong survivor: %s", idx.Templates[0].ID)
	}
	if idx.Templates[0].Kind != KindSite {
		t.Errorf("kind should default to site, got %q", idx.Templates[0].Kind)
	}
}

// ── the mirror chain ────────────────────────────────────────────

// The whole point of the chain: the first mirror is blocked (the usual state
// of raw.githubusercontent.com in Iran) and the list still loads.
func TestMirrorChainFallsThrough(t *testing.T) {
	var hits []string
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "good"+r.URL.Path)
		w.Write([]byte(`{"schema":1,"templates":[]}`))
	}))
	defer good.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "dead")
		http.Error(w, "blocked", 403)
	}))
	defer dead.Close()

	c := NewClient(t.TempDir())
	src := Source{Mirror: dead.URL}
	// Replace the public mirrors with our test server by pointing the
	// repo at a path the test server serves; simplest is to override
	// mirrors through Mirror plus a second entry, so use a custom Source
	// via the unexported builder.
	got, _, err := c.getWith(context.Background(),
		[]string{dead.URL + "/{path}", good.URL + "/{path}"},
		"index.json", MaxIndexBytes, 5*time.Second)
	if err != nil {
		t.Fatalf("chain should have succeeded: %v", err)
	}
	if !strings.Contains(string(got), "schema") {
		t.Errorf("wrong body: %s", got)
	}
	if len(hits) != 2 || hits[0] != "dead" {
		t.Errorf("expected the dead mirror to be tried first, got %v", hits)
	}
	_ = src
}

// When every mirror is blocked the cached list must still render. This is the
// difference between "the gallery works offline" and "the gallery is a blank
// page with a spinner".
func TestStaleCacheServesWhenEverythingIsBlocked(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema":1,"templates":[{"id":"a","archive":"a.tar.gz","sha256":"` + sum([]byte("a")) + `"}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	c := &Client{Dir: dir}
	src := Source{Mirror: up.URL, TTL: time.Nanosecond}

	res, err := c.FetchIndex(context.Background(), src, true)
	if err != nil || res.Stale {
		t.Fatalf("first fetch should be live: %v %+v", err, res)
	}
	up.Close() // every mirror is now unreachable

	res2, err := c.FetchIndex(context.Background(), src, true)
	if err != nil {
		t.Fatalf("a blocked repository must not be an error when a cache exists: %v", err)
	}
	if !res2.Stale {
		t.Error("result should be marked stale so the UI can say so")
	}
	if len(res2.Index.Templates) != 1 {
		t.Errorf("cached list lost its entries: %+v", res2.Index)
	}
	if res2.Err == "" {
		t.Error("the reason should be reported alongside the stale list")
	}
}

// With no cache and no network the caller gets ErrUnavailable — a calm
// "repository unavailable", never a hang.
func TestUnavailableWithoutCache(t *testing.T) {
	c := &Client{Dir: t.TempDir()}
	_, err := c.FetchIndex(context.Background(),
		Source{Mirror: "http://127.0.0.1:1", Repo: "nope/nope", TTL: time.Hour}, true)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// jsDelivr must come before raw.githubusercontent.com: on the target network
// it is the one that usually resolves. Getting this backwards would make
// every gallery visit wait for a timeout first.
func TestMirrorOrder(t *testing.T) {
	m := Source{Repo: "o/r", Ref: "main"}.mirrors()
	if len(m) != 2 || !strings.Contains(m[0], "jsdelivr") {
		t.Fatalf("jsDelivr must be tried first: %v", m)
	}
	if !strings.Contains(m[1], "raw.githubusercontent.com") {
		t.Errorf("GitHub must be the fallback: %v", m)
	}
	withMirror := Source{Repo: "o/r", Mirror: "https://my.host/tpl"}.mirrors()
	if len(withMirror) != 3 || !strings.HasPrefix(withMirror[0], "https://my.host/tpl") {
		t.Errorf("an operator mirror must be tried first: %v", withMirror)
	}
}

// ── installing ──────────────────────────────────────────────────

func TestInstallVerifiesChecksum(t *testing.T) {
	blob := tgz(t, map[string]string{"index.html": "<h1>hi</h1>"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(blob)
	}))
	defer srv.Close()
	c := NewClient(t.TempDir())
	src := Source{Mirror: srv.URL}

	// Wrong checksum: must be refused and must leave nothing behind.
	_, err := c.Install(context.Background(), src, Meta{
		ID: "t1", Kind: KindSite, Archive: "t1.tar.gz", SHA256: sum([]byte("different")),
	})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("a tampered archive must be refused: %v", err)
	}
	if c.Has("t1") {
		t.Error("a refused template must not be installed")
	}

	// Correct checksum: installs.
	inst, err := c.Install(context.Background(), src, Meta{
		ID: "t1", Kind: KindSite, Archive: "t1.tar.gz", SHA256: sum(blob),
	})
	if err != nil {
		t.Fatalf("valid install failed: %v", err)
	}
	if !c.Has("t1") || inst.Files < 1 {
		t.Errorf("install did not land: %+v", inst)
	}
}

// Only the template the operator picked may be downloaded. Fetching the whole
// repository would defeat the reason the gallery is out of the binary.
func TestInstallDownloadsOnlyTheChosenArchive(t *testing.T) {
	var asked []string
	a := tgz(t, map[string]string{"index.html": "a"})
	b := tgz(t, map[string]string{"index.html": "b"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/a.tar.gz":
			w.Write(a)
		case "/b.tar.gz":
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(t.TempDir())
	if _, err := c.Install(context.Background(), Source{Mirror: srv.URL},
		Meta{ID: "a", Kind: KindSite, Archive: "a.tar.gz", SHA256: sum(a)}); err != nil {
		t.Fatal(err)
	}
	for _, p := range asked {
		if p == "/b.tar.gz" {
			t.Errorf("downloaded a template nobody asked for: %v", asked)
		}
	}
	if len(asked) != 1 {
		t.Errorf("expected exactly one request, got %v", asked)
	}
}

// ── unpacking: the dangerous part ───────────────────────────────

func TestUnpackRefusesPathEscape(t *testing.T) {
	for _, name := range []string{"../evil.html", "/etc/passwd", "a/../../evil", "a/./../../x"} {
		blob := tgz(t, map[string]string{name: "pwned"})
		dir := t.TempDir()
		if err := Unpack(blob, dir); err == nil {
			t.Errorf("%q was accepted — it must not be", name)
		}
	}
}

func TestUnpackRefusesSymlinks(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.WriteHeader(&tar.Header{Name: "secrets", Linkname: "/etc/shadow", Typeflag: tar.TypeSymlink, Mode: 0o777})
	tw.Close()
	zw.Close()

	dir := t.TempDir()
	if err := Unpack(buf.Bytes(), dir); err == nil {
		t.Fatal("a symlink out of the template must be refused")
	}
	if _, err := os.Lstat(filepath.Join(dir, "secrets")); err == nil {
		t.Error("the symlink was created anyway")
	}
}

// A small archive that expands to gigabytes is how a 1 GB server's disk gets
// filled by a "website".
func TestUnpackRefusesBomb(t *testing.T) {
	big := strings.Repeat("A", MaxUnpackedBytes+1024)
	blob := tgz(t, map[string]string{"index.html": big})
	t.Logf("compressed %d bytes, expands to %d", len(blob), len(big))
	if len(blob) > 1<<20 {
		t.Fatalf("test fixture is not compressible enough: %d", len(blob))
	}
	if err := Unpack(blob, t.TempDir()); err == nil {
		t.Fatal("an oversized expansion must be refused")
	}
}

// `tar czf x.tar.gz mytheme` produces a wrapper directory; `tar czf x.tar.gz
// mytheme/*` does not. Both are what a template author will actually upload.
func TestUnpackStripsSingleWrapperDirectory(t *testing.T) {
	wrapped := tgz(t, map[string]string{
		"mytheme/index.html":      "<h1>hi</h1>",
		"mytheme/assets/site.css": "body{}",
	})
	dir := t.TempDir()
	if err := Unpack(wrapped, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Errorf("wrapper directory was not stripped: %v", err)
	}

	flat := tgz(t, map[string]string{"index.html": "x", "about.html": "y"})
	dir2 := t.TempDir()
	if err := Unpack(flat, dir2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "index.html")); err != nil {
		t.Errorf("flat archive broken: %v", err)
	}
}

// ── built-ins cannot be destroyed ───────────────────────────────

func TestBuiltinCannotBeRemoved(t *testing.T) {
	c := NewClient(t.TempDir())
	if err := c.Remove("corporate-slate"); err == nil {
		t.Fatal("a built-in must not be removable — it is the offline fallback")
	}
	if !c.Has("corporate-slate") {
		t.Error("built-in disappeared")
	}
}

func TestMaterialiseBuiltin(t *testing.T) {
	c := NewClient(t.TempDir())
	dst := filepath.Join(t.TempDir(), "site")
	if err := c.Materialise("tech-night", dst); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"index.html", "assets/site.css", "errors/404.html", "robots.txt"} {
		if _, err := os.Stat(filepath.Join(dst, want)); err != nil {
			t.Errorf("%s missing after materialise: %v", want, err)
		}
	}
}

// ── cost ────────────────────────────────────────────────────────

// Listing the local library happens on every gallery visit, so its cost is
// locked as behaviour rather than left to drift.
func TestListCost(t *testing.T) {
	c := NewClient(t.TempDir())
	// Warm the embedded FS walk.
	c.List()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	const n = 200
	for i := 0; i < n; i++ {
		c.List()
	}
	per := time.Since(start) / n
	runtime.ReadMemStats(&after)
	t.Logf("Client.List(): %v per call, %d allocs per call",
		per, (after.Mallocs-before.Mallocs)/n)
	if per > 5*time.Millisecond {
		t.Errorf("List() costs %v per call — the gallery calls it on every visit", per)
	}
}

func TestValidID(t *testing.T) {
	for _, ok := range []string{"a", "corporate-slate", "errors_dark", "t1"} {
		if !ValidID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "-x", "A", "a/b", "a b", "a;b", strings.Repeat("a", 65)} {
		if ValidID(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

var _ = json.Marshal

// A blocked network must be discovered ONCE, not on every page view.
//
// Measured before this existed, against a mirror that hangs rather than
// refuses — which is how a blocked host behaves on the network this panel
// is written for: every gallery visit walked the chain to three timeouts
// and cost 12-13 seconds, forever. The page still rendered from the
// built-ins at the end of it, but thirteen seconds of blank screen reads as
// "the panel is broken".
//
// This is a cost locked as behaviour, not a benchmark: it asserts the number
// of network attempts, which is the thing that must not regress.
func TestABlockedRepositoryIsOnlyDiscoveredOnce(t *testing.T) {
	var attempts int32
	hang := make(chan struct{})
	defer close(hang)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		select {
		case <-hang:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.Close()

	now := time.Now()
	c := &Client{Dir: t.TempDir(), Now: func() time.Time { return now }}
	// A short per-attempt timeout so the test is quick; the behaviour
	// under test is how MANY attempts happen, not how long one takes.
	c.HTTP = &http.Client{Timeout: 300 * time.Millisecond}
	src := Source{Mirror: srv.URL, Repo: "nope/nope"}

	start := time.Now()
	if _, err := c.FetchIndex(context.Background(), src, false); err == nil {
		t.Fatal("a hanging mirror should have failed")
	}
	first := time.Since(start)
	n1 := atomic.LoadInt32(&attempts)
	if n1 == 0 {
		t.Fatal("the first visit made no request at all")
	}

	// Five more visits inside the failure window.
	start = time.Now()
	for i := 0; i < 5; i++ {
		c.FetchIndex(context.Background(), src, false)
	}
	rest := time.Since(start)
	n2 := atomic.LoadInt32(&attempts)

	t.Logf("first visit: %v with %d network attempts", first.Round(time.Millisecond), n1)
	t.Logf("next five:   %v with %d further attempts", rest.Round(time.Millisecond), n2-n1)

	if n2 != n1 {
		t.Errorf("the blocked repository was retried %d more times; "+
			"every gallery visit would pay the full timeout again", n2-n1)
	}
	if rest > first {
		t.Errorf("five cached failures took %v, longer than the one real attempt (%v)", rest, first)
	}

	// Past the window it must try again, or the gallery would never
	// recover when the network does.
	now = now.Add(FailTTL + time.Second)
	c.FetchIndex(context.Background(), src, false)
	if atomic.LoadInt32(&attempts) == n2 {
		t.Error("the repository was never retried after the failure window expired")
	}
}

// A success must clear the remembered failure immediately, so the gallery
// comes back the moment the network does rather than after another window.
func TestRecoveryClearsTheRememberedFailure(t *testing.T) {
	var up atomic.Bool
	body := `{"schema":1,"templates":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "blocked", 403)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	now := time.Now()
	c := &Client{Dir: t.TempDir(), Now: func() time.Time { return now }}
	src := Source{Mirror: srv.URL, Repo: "nope/nope"}

	if _, err := c.FetchIndex(context.Background(), src, false); err == nil {
		t.Fatal("should have failed while down")
	}
	up.Store(true)
	// The operator presses "check the repository", which forces a retry.
	res, err := c.FetchIndex(context.Background(), src, true)
	if err != nil || res.Stale {
		t.Fatalf("a forced refresh should have succeeded: %v %+v", err, res)
	}
	// And the next ordinary visit must be a clean cache hit, not a stale one.
	res2, err := c.FetchIndex(context.Background(), src, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Stale {
		t.Error("the failure was still remembered after a successful fetch")
	}
}

// An archive produced by the REAL tar command, the way the documented
// packing script does it.
//
// Every other unpack test here builds its fixture with Go's archive/tar,
// which writes exactly the entries it is told to and no trailing slashes.
// Real tar does not: `tar -czf x.tar.gz -C dir .` emits "./", "./assets/"
// and "./errors/" as directory entries WITH a trailing slash, which made
// the last path segment empty and got the whole archive rejected as a
// traversal attempt. The bug was invisible to every synthetic fixture and
// appeared the first time a template was installed from a real repository.
func TestUnpackRealTarArchive(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar is not available")
	}
	src := t.TempDir()
	for _, f := range []string{"index.html", "about.html",
		"assets/site.css", "errors/404.html", "errors/50x.html"} {
		full := filepath.Join(src, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("<h1>"+f+"</h1>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Both forms a template author will actually use.
	for _, args := range [][]string{
		// The documented one: contents at the archive root, with "./".
		{"--sort=name", "--mtime=UTC 2020-01-01", "--owner=0", "--group=0",
			"--numeric-owner", "-czf", "", "-C", src, "."},
		// The other habit: a single wrapper directory.
		{"--sort=name", "--mtime=UTC 2020-01-01", "--owner=0", "--group=0",
			"--numeric-owner", "-czf", "", "-C", filepath.Dir(src), filepath.Base(src)},
	} {
		out := filepath.Join(t.TempDir(), "t.tar.gz")
		a := append([]string{}, args...)
		for i := range a {
			if a[i] == "" {
				a[i] = out
			}
		}
		if o, err := exec.Command("tar", a...).CombinedOutput(); err != nil {
			t.Fatalf("tar: %v\n%s", err, o)
		}
		blob, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		if err := Unpack(blob, dst); err != nil {
			t.Fatalf("a real tar archive was refused: %v", err)
		}
		for _, want := range []string{"index.html", "assets/site.css", "errors/404.html"} {
			if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(want))); err != nil {
				t.Errorf("%s missing after unpacking a real archive: %v", want, err)
			}
		}
	}
}

// The trailing slash must not become a way IN either: "../" is still an
// escape once the slash is gone.
func TestTrailingSlashDoesNotLaunderAnEscape(t *testing.T) {
	for _, name := range []string{"../evil/", "/etc/", "a/../../x/"} {
		blob := tgz(t, map[string]string{name + "f.html": "x"})
		if err := Unpack(blob, t.TempDir()); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
}
