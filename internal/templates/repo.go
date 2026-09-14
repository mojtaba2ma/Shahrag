// Package templates talks to the out-of-panel template repository.
//
// Why the templates do not live in the binary
// ───────────────────────────────────────────
// A gallery of real websites is megabytes of HTML, CSS, fonts and images.
// Embedding all of it would multiply a 10 MB binary several times over and
// make every upgrade a full re-download, on servers that are frequently on
// slow or filtered links. So the panel ships a handful of built-in templates
// (enough that the feature works with no network at all) and fetches the rest
// from a plain GitHub repository, one template at a time, only when the
// operator actually picks one.
//
// Why there is a mirror chain
// ───────────────────────────
// The target audience is on Iranian networks where raw.githubusercontent.com
// is routinely unreachable while jsDelivr's CDN still resolves. Trying only
// one host would make the gallery permanently empty for most users, so every
// fetch walks a list of mirrors in order and the first that answers wins.
// jsDelivr is tried FIRST, not GitHub, because it is the one that usually
// works there.
//
// Why nothing here ever blocks the page
// ─────────────────────────────────────
// When every mirror is blocked the panel must still render the gallery
// instantly from an on-disk cache and say so, rather than hanging for three
// timeouts. The index is therefore always read from cache first, and the
// network is only consulted when the cache is stale — and a network failure
// downgrades to "stale cache" instead of an error.
package templates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits. Every one of these exists because the archive comes from the
// network: a repository that is compromised, mirrored badly, or simply wrong
// must not be able to fill the disk of a 1 GB server.
const (
	// MaxIndexBytes caps index.json. A few hundred templates fit in far
	// less; anything larger is a mistake or an attack.
	MaxIndexBytes = 2 << 20 // 2 MiB

	// MaxArchiveBytes caps one downloaded template archive.
	MaxArchiveBytes = 8 << 20 // 8 MiB

	// MaxUnpackedBytes caps the same archive after decompression. A
	// "zip bomb" compresses gigabytes into kilobytes, so the compressed
	// limit above is not sufficient on its own.
	MaxUnpackedBytes = 32 << 20 // 32 MiB

	// MaxFiles caps how many entries one archive may contain.
	MaxFiles = 2000

	// MaxAssetBytes caps a thumbnail or screenshot.
	MaxAssetBytes = 1 << 20 // 1 MiB
)

// DefaultRepo is the repository the panel looks in when the operator has not
// chosen another one. It does not need to exist: a missing repository shows
// as "repository unavailable", never as a broken page.
const DefaultRepo = "mojtaba2ma/Shahrag-Templates"

// DefaultRef is the branch fetched from the repository.
const DefaultRef = "main"

// DefaultTTL is how long a cached index is served without asking the network.
const DefaultTTL = 6 * time.Hour

// fetchTimeout bounds a single mirror attempt. Deliberately short: the point
// of the chain is to move on quickly, and a blocked host in Iran usually
// manifests as a hang rather than a refusal.
const fetchTimeout = 12 * time.Second

// archiveTimeout bounds a template download, which is much bigger than an
// index and may legitimately be slow.
const archiveTimeout = 90 * time.Second

// Errors callers distinguish.
var (
	// ErrUnavailable means no mirror answered and no cache exists. The UI
	// turns this into a calm "repository unavailable" panel.
	ErrUnavailable = errors.New("template repository unavailable")

	// ErrNotFound means the repository answered but has no such template.
	ErrNotFound = errors.New("template not found")

	// ErrChecksum means a download arrived intact but is not the file the
	// index describes. Never install it.
	ErrChecksum = errors.New("template checksum mismatch")
)

// Localised is a short string with per-language variants. "en" is the
// fallback; a template that offers only one language still works everywhere.
type Localised map[string]string

// Pick returns the best available string for lang.
func (l Localised) Pick(lang string) string {
	if l == nil {
		return ""
	}
	if v, ok := l[lang]; ok && strings.TrimSpace(v) != "" {
		return v
	}
	if v, ok := l["en"]; ok && strings.TrimSpace(v) != "" {
		return v
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.TrimSpace(l[k]) != "" {
			return l[k]
		}
	}
	return ""
}

// Category groups templates in the gallery.
type Category struct {
	ID   string    `json:"id"`
	Name Localised `json:"name"`
	// Kind separates the site gallery from the error-page gallery so a
	// 404 design never shows up as a candidate home page.
	Kind string `json:"kind,omitempty"` // "site" (default) or "error"
}

// Meta describes one template in index.json.
type Meta struct {
	ID       string    `json:"id"`
	Name     Localised `json:"name"`
	Desc     Localised `json:"description,omitempty"`
	Category string    `json:"category,omitempty"`
	// Kind is "site" for a whole website and "error" for a set of error
	// pages. Defaults to "site" when absent.
	Kind    string `json:"kind,omitempty"`
	Version string `json:"version,omitempty"`
	Author  string `json:"author,omitempty"`
	License string `json:"license,omitempty"`

	// Archive is the repository-relative path of the .tar.gz.
	Archive string `json:"archive"`
	// Size is the archive size in bytes, shown before downloading so the
	// operator on a metered link can decide.
	Size int64 `json:"size,omitempty"`
	// Files is how many files it contains. Informational.
	Files int `json:"files,omitempty"`
	// SHA256 is verified after download. A template without one is
	// rejected: an unverified archive from a mirror is untrusted code
	// served to the operator's visitors.
	SHA256 string `json:"sha256"`

	Thumb       string   `json:"thumb,omitempty"`
	Screenshots []string `json:"screenshots,omitempty"`

	// Pages lists the HTML files the template provides, for the details
	// view. Informational only.
	Pages []string `json:"pages,omitempty"`
	// ErrorPages lists the status codes this template supplies a page
	// for, e.g. ["404","403","50x"].
	ErrorPages []string `json:"error_pages,omitempty"`

	// RTL marks a template designed right-to-left.
	RTL bool `json:"rtl,omitempty"`

	// Builtin is set by the panel, never by the repository: it marks a
	// template compiled into the binary, which is always installed and
	// can never be removed.
	Builtin bool `json:"builtin,omitempty"`
}

// Index is the whole repository catalogue.
type Index struct {
	Schema     int        `json:"schema"`
	Updated    string     `json:"updated,omitempty"`
	Categories []Category `json:"categories,omitempty"`
	Templates  []Meta     `json:"templates"`
}

// Source describes where the panel looks for templates.
type Source struct {
	// Repo is "owner/name" on GitHub.
	Repo string
	// Ref is the branch or tag.
	Ref string
	// Mirror is an optional operator-supplied base URL tried FIRST, for
	// someone who hosts their own copy inside the country.
	Mirror string
	// TTL overrides DefaultTTL.
	TTL time.Duration
}

func (s Source) repo() string {
	if r := strings.Trim(strings.TrimSpace(s.Repo), "/"); r != "" {
		return r
	}
	return DefaultRepo
}

func (s Source) ref() string {
	if r := strings.TrimSpace(s.Ref); r != "" {
		return r
	}
	return DefaultRef
}

func (s Source) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultTTL
}

// mirrors returns the URL builders to try, in order.
//
// Order is deliberate and measured against the target network:
//  1. an operator mirror, because someone who set one knows their link;
//  2. jsDelivr, which is the CDN that still resolves on most Iranian ISPs;
//  3. raw.githubusercontent.com, the authoritative source, usually blocked.
func (s Source) mirrors() []string {
	repo, ref := s.repo(), s.ref()
	var out []string
	if m := strings.TrimRight(strings.TrimSpace(s.Mirror), "/"); m != "" {
		out = append(out, m+"/{path}")
	}
	out = append(out,
		"https://cdn.jsdelivr.net/gh/"+repo+"@"+ref+"/{path}",
		"https://raw.githubusercontent.com/"+repo+"/"+ref+"/{path}",
	)
	return out
}

// Client fetches from the repository and caches on disk.
type Client struct {
	// Dir is the cache root, normally /var/lib/shahrag/templates.
	Dir string
	// HTTP is the transport. Nil means a sensible default.
	HTTP *http.Client
	// Now is overridable for tests.
	Now func() time.Time

	mu sync.Mutex
}

// NewClient builds a client rooted at dir.
func NewClient(dir string) *Client {
	return &Client{Dir: dir}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// One shared transport with a small pool: the panel makes a handful
	// of requests an hour, and an idle connection to a blocked host is
	// pure waste on a 1 GB box.
	return defaultHTTP
}

var defaultHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	},
}

// cacheDir returns the cache root, defaulting when unset.
func (c *Client) cacheDir() string {
	if strings.TrimSpace(c.Dir) != "" {
		return c.Dir
	}
	return "/var/lib/shahrag/templates"
}

// IndexResult carries the catalogue plus how fresh it is, because the UI
// must be able to say "showing a cached list from two hours ago; the
// repository is not reachable right now".
type IndexResult struct {
	Index *Index `json:"index"`
	// Stale is true when the network failed and this came from cache.
	Stale bool `json:"stale"`
	// Cached is true when no network request was made at all.
	Cached bool `json:"cached"`
	// FetchedAt is when the cached copy was written.
	FetchedAt time.Time `json:"fetched_at"`
	// Mirror is the URL that answered, empty when served from cache.
	Mirror string `json:"mirror,omitempty"`
	// Err is a human-readable reason the network failed, if it did.
	Err string `json:"error,omitempty"`
}

type cacheEnvelope struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Mirror    string          `json:"mirror"`
	Raw       json.RawMessage `json:"raw"`
}

func (c *Client) indexCachePath() string {
	return filepath.Join(c.cacheDir(), "index-cache.json")
}

// FetchIndex returns the catalogue, preferring a fresh cache, then the
// network, then a stale cache. It never returns both a nil index and a nil
// error, and it never blocks longer than the mirror chain allows.
func (c *Client) FetchIndex(ctx context.Context, src Source, force bool) (*IndexResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	env, cacheErr := c.readIndexCache()
	if cacheErr == nil && !force {
		if c.now().Sub(env.FetchedAt) < src.ttl() {
			idx, err := parseIndex(env.Raw)
			if err == nil {
				return &IndexResult{Index: idx, Cached: true,
					FetchedAt: env.FetchedAt, Mirror: env.Mirror}, nil
			}
			// A corrupt cache is not fatal; fall through to network.
		}
	}

	raw, mirror, err := c.get(ctx, src, "index.json", MaxIndexBytes, fetchTimeout)
	if err == nil {
		if idx, perr := parseIndex(raw); perr == nil {
			now := c.now()
			c.writeIndexCache(cacheEnvelope{FetchedAt: now, Mirror: mirror, Raw: raw})
			return &IndexResult{Index: idx, FetchedAt: now, Mirror: mirror}, nil
		} else {
			err = perr
		}
	}

	// Network failed. A stale cache is far more useful than an error.
	if cacheErr == nil {
		if idx, perr := parseIndex(env.Raw); perr == nil {
			return &IndexResult{Index: idx, Stale: true, Cached: true,
				FetchedAt: env.FetchedAt, Mirror: env.Mirror,
				Err: err.Error()}, nil
		}
	}
	return &IndexResult{Index: &Index{}, Err: err.Error()},
		fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func parseIndex(raw []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("index.json: %w", err)
	}
	// Normalise and drop entries that cannot be installed safely, rather
	// than letting a half-described template reach the UI and fail at
	// download time with a confusing message.
	clean := idx.Templates[:0]
	seen := map[string]bool{}
	for _, t := range idx.Templates {
		t.ID = strings.TrimSpace(t.ID)
		if !ValidID(t.ID) || seen[t.ID] {
			continue
		}
		t.Archive = strings.TrimSpace(t.Archive)
		t.SHA256 = strings.ToLower(strings.TrimSpace(t.SHA256))
		if t.Archive == "" || !safeRepoPath(t.Archive) {
			continue
		}
		if len(t.SHA256) != 64 {
			continue
		}
		if t.Kind == "" {
			t.Kind = KindSite
		}
		if t.Kind != KindSite && t.Kind != KindError {
			continue
		}
		if !safeRepoPath(t.Thumb) {
			t.Thumb = ""
		}
		shots := t.Screenshots[:0]
		for _, s := range t.Screenshots {
			if safeRepoPath(s) && s != "" {
				shots = append(shots, s)
			}
		}
		t.Screenshots = shots
		seen[t.ID] = true
		clean = append(clean, t)
	}
	idx.Templates = clean
	return &idx, nil
}

// Template kinds.
const (
	KindSite  = "site"
	KindError = "error"
)

func (c *Client) readIndexCache() (cacheEnvelope, error) {
	var env cacheEnvelope
	b, err := os.ReadFile(c.indexCachePath())
	if err != nil {
		return env, err
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return env, err
	}
	if len(env.Raw) == 0 {
		return env, errors.New("empty cache")
	}
	return env, nil
}

func (c *Client) writeIndexCache(env cacheEnvelope) {
	if err := os.MkdirAll(c.cacheDir(), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	tmp := c.indexCachePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.indexCachePath())
}

// get walks the mirror chain and returns the first successful body.
func (c *Client) get(ctx context.Context, src Source, p string, max int64, timeout time.Duration) ([]byte, string, error) {
	return c.getWith(ctx, src.mirrors(), p, max, timeout)
}

// getWith is get against an explicit mirror list, so a test can drive the
// chain without pretending to be jsDelivr.
func (c *Client) getWith(ctx context.Context, mirrors []string, p string, max int64, timeout time.Duration) ([]byte, string, error) {
	var last error
	for _, tmpl := range mirrors {
		url := strings.Replace(tmpl, "{path}", p, 1)
		body, err := c.fetchOne(ctx, url, max, timeout)
		if err == nil {
			return body, url, nil
		}
		last = err
		// A definitive 404 from one mirror does not prove the file is
		// absent — jsDelivr 404s while a tag propagates — so keep going.
		if ctx.Err() != nil {
			break
		}
	}
	if last == nil {
		last = errors.New("no mirrors configured")
	}
	return nil, "", last
}

func (c *Client) fetchOne(ctx context.Context, url string, max int64, timeout time.Duration) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Shahrag-Panel")
	req.Header.Set("Accept", "*/*")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hostOf(url), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: HTTP %d", hostOf(url), resp.StatusCode)
	}
	// LimitReader with max+1 so an oversized body is detected rather than
	// silently truncated into a corrupt archive.
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hostOf(url), err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: response larger than %d bytes", hostOf(url), max)
	}
	return b, nil
}

func hostOf(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i > 0 {
		return s[:i]
	}
	return s
}

// FetchAsset downloads a thumbnail or screenshot and returns the bytes plus a
// content type. Assets are cached on disk under assets/ by their repository
// path so the gallery does not re-fetch on every visit.
func (c *Client) FetchAsset(ctx context.Context, src Source, p string) ([]byte, string, error) {
	if !safeRepoPath(p) || p == "" {
		return nil, "", ErrNotFound
	}
	cached := filepath.Join(c.cacheDir(), "assets", hashPath(p)+filepath.Ext(p))
	if b, err := os.ReadFile(cached); err == nil && len(b) > 0 {
		return b, contentType(p), nil
	}
	b, _, err := c.get(ctx, src, p, MaxAssetBytes, fetchTimeout)
	if err != nil {
		return nil, "", err
	}
	if mkErr := os.MkdirAll(filepath.Dir(cached), 0o755); mkErr == nil {
		tmp := cached + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, cached)
		}
	}
	return b, contentType(p), nil
}

func hashPath(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:12])
}

func contentType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

// ValidID reports whether an id is safe to use as a directory name. Anything
// that could escape the templates directory, or confuse a shell or an nginx
// path, is rejected — ids come from a JSON file on the internet.
func ValidID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	if id == "." || id == ".." || strings.HasPrefix(id, "-") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// safeRepoPath rejects a repository path that tries to leave the repository,
// which would turn a fetch into a request for an arbitrary URL.
func safeRepoPath(p string) bool {
	if p == "" {
		return true // optional fields
	}
	if strings.Contains(p, "://") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return false
	}
	if strings.Contains(p, "\\") || strings.ContainsAny(p, "?#") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return len(p) <= 256
}
