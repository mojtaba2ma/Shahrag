package web

// Serving assets pre-compressed.
//
// The panel ships 578 KB of JavaScript, CSS and translations, which gzip to
// 183 KB — a 68% saving. On the connections this panel is actually used
// over that is the difference between a page that appears and a page that
// is still loading, and it costs the server nothing at request time because
// every asset is compressed ONCE, lazily, and the result cached.
//
// Compressing per request would be the wrong trade entirely: it would spend
// CPU on every page load to save bandwidth that a browser cache already
// eliminates. Compressing once and holding 183 KB of memory is the right
// one — the assets never change while the process runs, since they are
// embedded in the binary.

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gzipCache holds the compressed form of each asset.
//
// Populated on first request rather than at startup: a panel that is never
// opened should not pay to compress anything, and the first visitor pays
// only a few milliseconds once.
var (
	gzipMu    sync.RWMutex
	gzipCache = map[string][]byte{}
)

// minGzipSize is the smallest asset worth compressing.
//
// Below roughly a kilobyte the gzip header and trailer cancel out the
// saving, and a very small file can even grow. There is also no point
// spending a round of CPU to save a hundred bytes.
const minGzipSize = 1024

// gzipContentTypes are the types that compress well. Images and fonts are
// already compressed; running them through gzip wastes CPU on both ends and
// typically makes them very slightly larger.
func gzipWorthIt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".css", ".html", ".json", ".svg", ".txt", ".map":
		return true
	}
	return false
}

// clientAcceptsGzip reports whether the browser said it can decode gzip.
//
// Checked properly rather than with a substring match: "gzip;q=0" means the
// client explicitly does NOT want it, and a naive strings.Contains would
// send compressed bytes to a client that just told us not to.
func clientAcceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		part = strings.TrimSpace(part)
		name, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		// An explicit q=0 is a refusal.
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			if v, ok := strings.CutPrefix(p, "q="); ok {
				if q, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && q == 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

// gzipAsset returns the compressed bytes for an asset, or nil when
// compression is not worth it or failed.
func gzipAsset(name string, raw []byte) []byte {
	if len(raw) < minGzipSize || !gzipWorthIt(name) {
		return nil
	}
	gzipMu.RLock()
	if b, ok := gzipCache[name]; ok {
		gzipMu.RUnlock()
		return b
	}
	gzipMu.RUnlock()

	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := zw.Write(raw); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	out := buf.Bytes()
	// If compression did not actually help, remember that too — as a nil
	// entry — so the work is not repeated on every request.
	if len(out) >= len(raw) {
		out = nil
	}

	gzipMu.Lock()
	gzipCache[name] = out
	gzipMu.Unlock()
	return out
}

// serveAsset writes an embedded asset, compressed when the client allows.
//
// Returns false when the asset does not exist, so the caller can fall
// through to its own handling.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string) bool {
	raw, err := fs.ReadFile(StaticFS(), name)
	if err != nil {
		return false
	}
	setAssetCache(w, name)

	if ct := assetContentType(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	if gz := gzipAsset(name, raw); gz != nil && clientAcceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		// Vary is mandatory here: without it a shared cache could hand
		// compressed bytes to a client that cannot decode them.
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
		// ServeContent would re-negotiate and re-set Content-Type from the
		// compressed bytes, so the response is written directly. The ETag
		// and cache headers are already set above.
		if r.Method == http.MethodHead {
			return true
		}
		_, _ = w.Write(gz)
		return true
	}

	w.Header().Add("Vary", "Accept-Encoding")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(raw))
	return true
}

// assetContentType maps an extension to a type.
//
// Go's mime package consults /etc/mime.types, which differs between
// distributions and has been known to lack an entry for .js on a minimal
// container — which makes the browser refuse to execute the file. Stating
// the handful the panel actually ships removes that dependency.
func assetContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	}
	return ""
}
