package nginx

// Emitting the advanced tuning directives.
//
// Split into three pieces because nginx accepts these in three different
// contexts, and putting one in the wrong place is a config nginx refuses to
// load at all:
//
//	TuningMainBlock   - main context (worker_processes, worker_rlimit_nofile)
//	TuningEventsBlock - inside events{} (multi_accept)
//	TuningHTTPBlock   - inside http{} (everything else)
//
// The main and events directives cannot live in the generated file that is
// included from inside http{}, so they go into a separate drop-in that the
// boot-guard machinery already knows how to install and validate. The http
// ones are written straight into the generated config.
//
// Every value is emitted ONLY when it is non-zero. That is what makes this
// safe to add to an existing installation: a Tuning with nothing set
// produces an empty string, and the generated file is byte-identical to
// what it was before this feature existed.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shahrag/internal/config"
)

// TuningMainBlock returns directives for nginx's main context.
//
// These must be at the top level of nginx.conf, outside http{} and events{}.
func TuningMainBlock(c *config.Config) string {
	t := c.NginxSettings.Tuning
	if !t.Enabled {
		return ""
	}
	// Built body-first and the header prepended only if there IS a body.
	// A length heuristic was tried and was wrong: the header contains
	// multi-byte box-drawing characters, so "is this longer than the
	// header?" compares bytes against a number that is not the header's
	// character count. Counting what was actually written is unambiguous.
	var body strings.Builder
	if wp := strings.TrimSpace(t.WorkerProcesses); wp != "" {
		fmt.Fprintf(&body, "worker_processes %s;\n", wp)
	}
	if t.WorkerRLimitNofile > 0 {
		// The ceiling on how many descriptors each worker may hold.
		// Below twice worker_connections nginx starts logging "too many
		// open files" while its own connection counter says it has room,
		// which is one of the least obvious failures it produces.
		fmt.Fprintf(&body, "worker_rlimit_nofile %d;\n", t.WorkerRLimitNofile)
	}
	if body.Len() == 0 {
		return ""
	}
	return "# ── Shahrag tuning (main context) ────────────────────────\n" + body.String()
}

// TuningEventsBlock returns the body of an events{} block.
func TuningEventsBlock(c *config.Config) string {
	t := c.NginxSettings.Tuning
	if !t.Enabled || !t.MultiAccept {
		return ""
	}
	return "    multi_accept on;\n"
}

// TuningHTTPBlock returns directives for the http context.
//
// This is written into the generated config, which is included from inside
// http{}, so everything here must be legal at http level.
func TuningHTTPBlock(c *config.Config) string {
	t := c.NginxSettings.Tuning
	if !t.Enabled {
		return ""
	}
	var b strings.Builder
	b.WriteString("# ── Shahrag tuning ───────────────────────────────────────\n")
	b.WriteString("#    Every directive below is one an operator could set by\n")
	b.WriteString("#    hand; the panel computes sensible values from what this\n")
	b.WriteString("#    machine actually is. Anything left unset is absent here\n")
	b.WriteString("#    and keeps nginx's own default.\n")

	wrote := false
	w := func(format string, args ...interface{}) {
		fmt.Fprintf(&b, format, args...)
		wrote = true
	}

	// server_tokens is emitted only when nginx.conf does not already set
	// it.
	//
	// Debian and Ubuntu ship an nginx.conf with `server_tokens off;`
	// inside http{}, and our generated file is INCLUDED from inside
	// http{} — so emitting it again is "directive is duplicate" and nginx
	// refuses to load the entire configuration. Found by the r47 browser
	// test, which saved the tuning form against a real Debian nginx and
	// got the config rolled back.
	//
	// Checking the file rather than assuming either way: some
	// distributions do not set it, and on those the operator would
	// otherwise silently not get the setting they asked for.
	if t.ServerTokensOff && !mainConfSets("server_tokens") {
		w("server_tokens off;\n")
	}

	// ── Client timeouts ──────────────────────────────────────
	// These are what make a slowloris expensive for the attacker rather
	// than free: a connection that dribbles one byte a second holds a
	// worker slot for as long as these allow.
	if t.KeepaliveTimeout > 0 {
		w("keepalive_timeout %ds;\n", t.KeepaliveTimeout)
	}
	if t.KeepaliveRequests > 0 {
		w("keepalive_requests %d;\n", t.KeepaliveRequests)
	}
	if t.ClientHeaderTimeout > 0 {
		w("client_header_timeout %ds;\n", t.ClientHeaderTimeout)
	}
	if t.ClientBodyTimeout > 0 {
		w("client_body_timeout %ds;\n", t.ClientBodyTimeout)
	}
	if t.SendTimeout > 0 {
		w("send_timeout %ds;\n", t.SendTimeout)
	}

	// ── Sizes ────────────────────────────────────────────────
	if t.ClientMaxBodyMB > 0 {
		w("client_max_body_size %dm;\n", t.ClientMaxBodyMB)
	}
	if t.ClientBodyBufferKB > 0 {
		w("client_body_buffer_size %dk;\n", t.ClientBodyBufferKB)
	}
	if t.LargeClientHeaderKB > 0 {
		// Four buffers is nginx's own count; only the size is tuned.
		w("large_client_header_buffers 4 %dk;\n", t.LargeClientHeaderKB)
	}

	// ── Proxy defaults ───────────────────────────────────────
	// Set at http level so every generated location inherits them without
	// each one having to repeat the block.
	if t.ProxyConnectTimeout > 0 {
		w("proxy_connect_timeout %ds;\n", t.ProxyConnectTimeout)
	}
	if t.ProxyReadTimeout > 0 {
		// The one that keeps a tunnel alive. nginx's 60s default is what
		// produces "recv() failed (104) while proxying upgraded
		// connection" on an idle WebSocket.
		w("proxy_read_timeout %ds;\n", t.ProxyReadTimeout)
	}
	if t.ProxySendTimeout > 0 {
		w("proxy_send_timeout %ds;\n", t.ProxySendTimeout)
	}
	if t.ProxySocketKeepalive {
		w("proxy_socket_keepalive on;\n")
	}
	if t.ProxyBufferingOff {
		w("proxy_buffering off;\n")
		// request_buffering too: buffering an upload before forwarding it
		// is the same mistake in the other direction, and breaks
		// streaming uploads entirely.
		w("proxy_request_buffering off;\n")
	}

	// ── Compression ──────────────────────────────────────────
	if t.GzipEnabled {
		// Same duplicate hazard as server_tokens: Debian's nginx.conf
		// has `gzip on;` in http{}. Repeating a simple on/off flag is
		// harmless to BEHAVIOUR but fatal to nginx, which rejects the
		// config outright.
		if !mainConfSets("gzip") {
			w("gzip on;\n")
		}
		w("gzip_vary on;\n")
		// Compressing a proxied response is the case that matters here:
		// almost everything this server returns comes from a backend.
		w("gzip_proxied any;\n")
		if t.GzipCompLevel > 0 {
			w("gzip_comp_level %d;\n", t.GzipCompLevel)
		}
		if t.GzipMinLength > 0 {
			w("gzip_min_length %d;\n", t.GzipMinLength)
		}
		w("gzip_types text/plain text/css text/xml application/json " +
			"application/javascript application/xml+rss text/javascript " +
			"image/svg+xml application/wasm;\n")
	}

	// ── TLS ──────────────────────────────────────────────────
	if t.SSLSessionCacheMB > 0 {
		// shared, not builtin: builtin is per-worker and therefore
		// useless the moment there is more than one worker.
		w("ssl_session_cache shared:SHG_SSL:%dm;\n", t.SSLSessionCacheMB)
	}
	if t.SSLSessionTimeoutMin > 0 {
		w("ssl_session_timeout %dm;\n", t.SSLSessionTimeoutMin)
	}
	if cv := strings.TrimSpace(t.SSLECDHCurve); cv != "" {
		// Validated in config.ValidateTuning, which refuses anything
		// that could terminate the directive.
		w("ssl_ecdh_curve %s;\n", cv)
	}

	// ── Connection limit ─────────────────────────────────────
	if t.LimitConnPerIP > 0 {
		// $binary_remote_addr, not $remote_addr: the binary form is 4
		// bytes instead of up to 15, which is a quarter of the zone
		// memory for the same information.
		//
		// The zone name is distinct from the honeypot's and the ban
		// engine's so the three cannot collide.
		w("limit_conn_zone $binary_remote_addr zone=shg_perip:8m;\n")
		w("limit_conn shg_perip %d;\n", t.LimitConnPerIP)
		// 429 rather than nginx's default 503: it says "you specifically
		// are doing too much" rather than "this server is broken", and
		// unlike a refusal it is an ordinary answer that reveals nothing
		// about filtering.
		w("limit_conn_status 429;\n")
	}

	// ── Static file cache ────────────────────────────────────
	if t.OpenFileCacheMax > 0 {
		w("open_file_cache max=%d inactive=60s;\n", t.OpenFileCacheMax)
		w("open_file_cache_valid 30s;\n")
		w("open_file_cache_min_uses 2;\n")
		// Caching the ERRORS too is what makes this help a server being
		// scanned: without it every probe for a nonexistent file is a
		// fresh stat() syscall.
		w("open_file_cache_errors on;\n")
	}

	// ── The access log ───────────────────────────────────────
	if t.AccessLogOff {
		b.WriteString("# WARNING: the access log is OFF. The statistics page and the\n")
		b.WriteString("#          404-flood ban rule both read it, and both are now\n")
		b.WriteString("#          blind. This was an explicit choice in the panel.\n")
		w("access_log off;\n")
	}

	if !wrote {
		return ""
	}
	b.WriteString("\n")
	return b.String()
}

// mainConfSets reports whether nginx.conf already contains a directive at
// the top level of its http block.
//
// Only a textual check, and deliberately so: parsing nginx's configuration
// language properly to answer "is this directive already set?" would be a
// large amount of code to avoid one duplicate, and a false NEGATIVE here is
// harmless (we emit it, nginx rejects it, the change is rolled back and
// reported) while a false positive just means the operator keeps the
// distribution's value. Comments are stripped first so a commented-out
// example does not count.
func mainConfSets(directive string) bool {
	txt, err := readConf()
	if err != nil {
		return false
	}
	if mainConfHas(txt, directive) {
		return true
	}
	// nginx.conf pulls in the distribution's own snippets, and Debian
	// puts several http-level defaults in them. Following one level of
	// include is enough for every layout seen in practice and avoids
	// implementing nginx's configuration parser.
	//
	// Our OWN generated file is skipped: it is the thing being generated,
	// so finding the directive there would mean "I already emitted this",
	// which is not the question being asked.
	for _, inc := range mainConfIncludes(txt) {
		if strings.Contains(inc, "conf.d/*") {
			// Reading the glob would include our own output.
			continue
		}
		if b, err := os.ReadFile(inc); err == nil && mainConfHas(string(b), directive) {
			return true
		}
	}
	return false
}

// mainConfIncludes lists the literal (non-glob) files nginx.conf includes.
func mainConfIncludes(txt string) []string {
	var out []string
	for _, line := range strings.Split(txt, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
		if !strings.HasPrefix(line, "include ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "include"))
		if path == "" || strings.ContainsAny(path, "*?[") {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = filepath.Join(filepath.Dir(confPath), path)
		}
		out = append(out, path)
	}
	return out
}

// mainConfHas reports whether a body contains the directive at the start of
// a line, ignoring comments.
func mainConfHas(txt, directive string) bool {
	for _, line := range strings.Split(txt, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "server_tokens off;" — the directive name, then whitespace.
		if strings.HasPrefix(line, directive) &&
			len(line) > len(directive) &&
			(line[len(directive)] == ' ' || line[len(directive)] == '\t') {
			return true
		}
	}
	return false
}

// TuningUpstreamKeepalive returns the keepalive line for an upstream block,
// or "" when pooling is off.
//
// Kept separate because it belongs inside upstream{}, and because turning it
// on requires two more things in the location — HTTP/1.1 and an empty
// Connection header — which the generator adds alongside it. Emitting the
// pool without those is the classic mistake: nginx opens pooled connections
// and then closes each one anyway, so it looks configured and does nothing.
func TuningUpstreamKeepalive(c *config.Config) string {
	t := c.NginxSettings.Tuning
	if !t.Enabled || t.UpstreamKeepalive <= 0 {
		return ""
	}
	return fmt.Sprintf("    keepalive %d;\n", t.UpstreamKeepalive)
}

// UpstreamPoolEnabled reports whether the generator should emit upstream
// blocks with a connection pool.
func UpstreamPoolEnabled(c *config.Config) bool {
	return c != nil && c.NginxSettings.Tuning.Enabled &&
		c.NginxSettings.Tuning.UpstreamKeepalive > 0
}
