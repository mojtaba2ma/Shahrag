package config

// Advanced nginx tuning.
//
// Every value here is something an operator could set by hand in
// nginx.conf. The point of putting them in the panel is not to hide nginx —
// it is that almost all of them only have a correct answer once you know
// what the machine actually is, and nobody wants to work out
// `worker_rlimit_nofile` from first principles on a Tuesday.
//
// So each field has a matching recommendation in Recommend(), which reads
// the real CPU count, the real amount of RAM, the real file-descriptor
// limit, and measures the real link speed, then produces a number with a
// reason attached. The panel shows an "auto-fill" button next to the field;
// pressing it writes the recommendation in, and the operator can then
// override it. Nothing is applied without being saved.
//
// The zero value of every field means "leave nginx's own default alone".
// That is what keeps an upgrade byte-identical for anyone who never opens
// this page: an absent key generates nothing.

import (
	"fmt"
	"strings"
)

// Tuning is the advanced nginx configuration.
type Tuning struct {
	// Enabled gates the whole block. Off means nothing at all is emitted,
	// so an existing installation is unaffected until someone opts in.
	Enabled bool `json:"enabled,omitempty"`

	// Profile records which preset was last applied, purely so the UI can
	// show it. The individual fields are the truth; this is a label.
	Profile string `json:"profile,omitempty"`

	// ── Workers ──────────────────────────────────────────────
	// WorkerProcesses is "auto" (one per core) or a number. Auto is right
	// for essentially everyone; a fixed number matters when the box is
	// shared and nginx should not claim every core.
	WorkerProcesses string `json:"worker_processes,omitempty"`
	// WorkerRLimitNofile is the per-worker descriptor ceiling. Each
	// client connection costs one descriptor, and each proxied upstream
	// connection costs another — so this must be comfortably above twice
	// worker_connections or nginx starts refusing connections with
	// "too many open files" long before the connection limit is reached.
	WorkerRLimitNofile int `json:"worker_rlimit_nofile,omitempty"`
	// MultiAccept lets one worker take every pending connection in a
	// single pass instead of one per event-loop turn.
	MultiAccept bool `json:"multi_accept,omitempty"`

	// ── Timeouts ─────────────────────────────────────────────
	// KeepaliveTimeout is how long an idle client connection is held.
	// nginx's default is 75s, which on a small box means a few hundred
	// idle browsers can hold every worker slot.
	KeepaliveTimeout int `json:"keepalive_timeout,omitempty"`
	// KeepaliveRequests caps how many requests one connection may serve
	// before it is closed, which bounds per-connection memory growth.
	KeepaliveRequests int `json:"keepalive_requests,omitempty"`
	// ClientHeaderTimeout / ClientBodyTimeout bound a slow client. These
	// are the two that make a slowloris expensive rather than free.
	ClientHeaderTimeout int `json:"client_header_timeout,omitempty"`
	ClientBodyTimeout   int `json:"client_body_timeout,omitempty"`
	// SendTimeout bounds a client that stops reading.
	SendTimeout int `json:"send_timeout,omitempty"`

	// ── Proxy behaviour ──────────────────────────────────────
	// ProxyConnectTimeout is how long to wait for the backend to accept.
	// Short on purpose: a local backend either answers immediately or is
	// down.
	ProxyConnectTimeout int `json:"proxy_connect_timeout,omitempty"`
	// ProxyReadTimeout / ProxySendTimeout must be LONG when anything is
	// proxied over a WebSocket, which is the normal case here — an xray
	// or VPN connection is idle for minutes at a time and nginx's 60s
	// default kills it. This is the direct cause of the
	// "recv() failed (104) while proxying upgraded connection" line in
	// the error log.
	ProxyReadTimeout int `json:"proxy_read_timeout,omitempty"`
	ProxySendTimeout int `json:"proxy_send_timeout,omitempty"`
	// ProxySocketKeepalive turns on TCP keepalive towards the backend, so
	// a dead peer is noticed instead of holding a socket for hours.
	ProxySocketKeepalive bool `json:"proxy_socket_keepalive,omitempty"`
	// UpstreamKeepalive is the size of the idle connection pool kept to
	// each backend. Without it every single proxied request opens a new
	// TCP connection to the backend and closes it again.
	UpstreamKeepalive int `json:"upstream_keepalive,omitempty"`
	// ProxyBuffering off streams the response straight through. Required
	// for anything long-lived or streaming; wasteful for ordinary pages.
	ProxyBufferingOff bool `json:"proxy_buffering_off,omitempty"`

	// ── Sizes ────────────────────────────────────────────────
	// ClientMaxBodySize in megabytes. nginx defaults to 1 MB, which
	// rejects almost any file upload with a confusing 413.
	ClientMaxBodyMB int `json:"client_max_body_mb,omitempty"`
	// ClientBodyBufferKB is how much of a request body is held in memory
	// before it spills to a temp file.
	ClientBodyBufferKB int `json:"client_body_buffer_kb,omitempty"`
	// LargeClientHeaderKB covers big cookies and long URLs.
	LargeClientHeaderKB int `json:"large_client_header_kb,omitempty"`

	// ── Compression ──────────────────────────────────────────
	GzipEnabled bool `json:"gzip_enabled,omitempty"`
	// GzipCompLevel 1..9. Above about 6 the extra CPU buys almost nothing.
	GzipCompLevel int `json:"gzip_comp_level,omitempty"`
	// GzipMinLength: below roughly a packet's worth, compressing costs
	// more than it saves.
	GzipMinLength int `json:"gzip_min_length,omitempty"`

	// ── TLS ──────────────────────────────────────────────────
	// SSLSessionCacheMB sizes the shared TLS session cache. 1 MB holds
	// about 4,000 sessions, and a resumed handshake is far cheaper than a
	// full one.
	SSLSessionCacheMB int `json:"ssl_session_cache_mb,omitempty"`
	// SSLSessionTimeoutMin is how long a session may be resumed.
	SSLSessionTimeoutMin int `json:"ssl_session_timeout_min,omitempty"`
	// SSLECDHCurve is the curve preference list. Setting this explicitly
	// is what fixes "SSL routines::bad key share": the client offered a
	// group the server was not configured to accept.
	SSLECDHCurve string `json:"ssl_ecdh_curve,omitempty"`

	// ── Privacy and limits ───────────────────────────────────
	// ServerTokensOff hides the nginx version from responses and error
	// pages. Not security by itself, but there is no reason to publish
	// it.
	ServerTokensOff bool `json:"server_tokens_off,omitempty"`
	// LimitConnPerIP caps simultaneous connections from one address.
	// Deliberately generous by default: browsers open six or more, and a
	// shared NAT multiplies that by every user behind it.
	LimitConnPerIP int `json:"limit_conn_per_ip,omitempty"`

	// ── Static files ─────────────────────────────────────────
	// OpenFileCacheMax caches file metadata, which matters for the fake
	// site and any real site being served.
	OpenFileCacheMax int `json:"open_file_cache_max,omitempty"`
	// AccessLogOff turns the access log off entirely.
	//
	// A real option, and a dangerous one, so it is off by default and
	// carries a warning in the UI: the statistics page and the 404-flood
	// ban rule both read that log, and switching it off silently disables
	// both.
	AccessLogOff bool `json:"access_log_off,omitempty"`
}

// Profiles.
const (
	ProfileSmall    = "small"    // 1 GB VPS running several services
	ProfileBalanced = "balanced" // the sane middle
	ProfileBusy     = "busy"     // plenty of RAM, lots of traffic
	ProfileCustom   = "custom"
)

// SysInfo is what a recommendation is computed from. Filled by the caller
// (internal/health reads /proc; the installer reads it directly) so this
// package stays free of platform code and is trivially testable.
type SysInfo struct {
	Cores int
	// RAMBytes is total physical memory.
	RAMBytes int64
	// AvailableBytes is what could actually be allocated right now. Used
	// rather than total for anything that reserves memory, because on a
	// box already running xray and AdGuard the free half is the honest
	// budget.
	AvailableBytes int64
	// FileMax is the system-wide descriptor ceiling (/proc/sys/fs/file-max).
	FileMax int
	// NofileHard is the hard RLIMIT_NOFILE the service can raise itself to.
	NofileHard int
	// LinkMbits is the measured or configured uplink speed. Zero when it
	// could not be determined, and every recommendation that would use it
	// falls back to a conservative default rather than guessing.
	LinkMbits int
	// SwapBytes is present so a recommendation can be more conservative
	// on a box with no swap at all, where overcommitting means the OOM
	// killer rather than a slow patch.
	SwapBytes int64
}

// Recommendation is one suggested value plus the reasoning.
//
// The reasoning travels with the number on purpose. A panel that fills in
// 2048 and says nothing teaches the operator nothing and cannot be argued
// with; one that says "2 cores x 1024, doubled for proxied upstreams" can be
// checked, and overridden with confidence.
type Recommendation struct {
	Field string `json:"field"`
	// Value is the suggested value, as a string so one type covers
	// numbers, booleans and "auto".
	Value string `json:"value"`
	// Why is a short, translatable reason CODE, not prose. The UI looks
	// it up; the CLI prints the English fallback.
	Why string `json:"why"`
	// Detail carries the numbers the reason refers to, already formatted.
	Detail string `json:"detail,omitempty"`
}

// Recommend computes a suggested value for every tunable.
//
// Returned as a map keyed by field so the UI can offer a per-field
// auto-fill button as well as a fill-everything button, and so a future
// field cannot silently be forgotten: the completeness test iterates the
// struct and fails if a field has no recommendation.
func Recommend(si SysInfo, profile string) map[string]Recommendation {
	cores := si.Cores
	if cores < 1 {
		cores = 1
	}
	ramMB := int(si.RAMBytes / (1 << 20))
	if ramMB < 1 {
		ramMB = 1024 // an unknown machine is assumed small, never large
	}
	availMB := int(si.AvailableBytes / (1 << 20))
	if availMB < 1 {
		availMB = ramMB / 2
	}

	small := ramMB <= 1280
	busy := ramMB >= 6144
	switch profile {
	case ProfileSmall:
		small, busy = true, false
	case ProfileBusy:
		small, busy = false, true
	case ProfileBalanced:
		small, busy = false, false
	}

	out := map[string]Recommendation{}
	add := func(field, value, why, detail string) {
		out[field] = Recommendation{Field: field, Value: value, Why: why, Detail: detail}
	}

	// ── Workers ──────────────────────────────────────────────
	add("worker_processes", "auto", "workers_auto",
		fmt.Sprintf("%d cores detected", cores))

	// worker_connections: each connection needs memory for its buffers.
	// A rough, deliberately conservative figure is ~16 KB per active
	// proxied connection once request and response buffers are counted.
	// Budget a QUARTER of available memory for connection buffers — the
	// rest belongs to the backends this box is actually running.
	connBudget := (availMB / 4) * 1024 / 16
	perWorker := connBudget / cores
	switch {
	case perWorker < 512:
		perWorker = 512
	case perWorker > 16384:
		perWorker = 16384
	}
	// A machine we could not measure gets a deliberately small number.
	//
	// Without this the fallbacks compound into a large figure: an unknown
	// box assumes 1024 MB, halves it to 512 MB "available", and then
	// divides by ONE core — arriving at 8192 connections for a machine we
	// know nothing about. Guessing high is the failure that makes nginx
	// refuse to start, so an unmeasured machine is capped at a value that
	// is safe everywhere and can be raised by hand. Caught by
	// TestAnUnknownMachineGetsConservativeNumbers.
	if si.Cores == 0 || si.RAMBytes == 0 {
		if perWorker > 1024 {
			perWorker = 1024
		}
	}
	// Round down to a power-of-two-ish figure so the number looks
	// deliberate rather than computed to the byte.
	perWorker = roundDownNice(perWorker)
	add("worker_connections", fmt.Sprint(perWorker), "conns_from_ram",
		fmt.Sprintf("%d MB available / 4, ~16 KB per connection, / %d workers",
			availMB, cores))

	// worker_rlimit_nofile: two descriptors per connection (client and
	// upstream) plus headroom for logs, certificates and sockets. Capped
	// by what the system will actually allow, because asking for more
	// than the hard limit makes nginx fail to start.
	want := perWorker*2 + 1024
	if si.NofileHard > 0 && want > si.NofileHard {
		want = si.NofileHard
	}
	if si.FileMax > 0 && want > si.FileMax/2 {
		want = si.FileMax / 2
	}
	add("worker_rlimit_nofile", fmt.Sprint(want), "nofile_from_conns",
		fmt.Sprintf("%d connections x 2 (client + upstream) + 1024 headroom", perWorker))

	add("multi_accept", boolStr(busy), "multi_accept_busy",
		"accept every pending connection per event-loop pass")

	// ── Timeouts ─────────────────────────────────────────────
	// A small box wants idle connections gone quickly, because each one
	// holds a worker slot and buffers.
	ka := 30
	if small {
		ka = 20
	} else if busy {
		ka = 65
	}
	add("keepalive_timeout", fmt.Sprint(ka), "keepalive_small",
		fmt.Sprintf("%d MB RAM: idle connections should not hold worker slots", ramMB))
	add("keepalive_requests", "1000", "keepalive_requests",
		"bounds per-connection memory growth")

	add("client_header_timeout", "15", "slowloris", "a slow header is an attack, not a client")
	add("client_body_timeout", "20", "slowloris", "a slow body is an attack, not a client")
	add("send_timeout", "20", "slowloris", "a client that stops reading must not hold a worker")

	// ── Proxy ────────────────────────────────────────────────
	add("proxy_connect_timeout", "5", "proxy_connect",
		"a local backend answers immediately or is down")
	// The long ones. This is what keeps a WebSocket or a VPN tunnel alive.
	add("proxy_read_timeout", "3600", "proxy_long_read",
		"an upgraded connection is idle for minutes; 60s kills it")
	add("proxy_send_timeout", "3600", "proxy_long_read",
		"an upgraded connection is idle for minutes; 60s kills it")
	add("proxy_socket_keepalive", "true", "proxy_keepalive_socket",
		"notice a dead backend instead of holding the socket")

	// The upstream pool. Sized per worker, since each worker keeps its
	// own pool: too large and idle sockets pile up, too small and it
	// stops helping.
	pool := 16
	if busy {
		pool = 64
	} else if small {
		pool = 16
	} else {
		pool = 32
	}
	add("upstream_keepalive", fmt.Sprint(pool), "upstream_pool",
		fmt.Sprintf("%d idle connections kept per worker to each backend", pool))

	// Buffering OFF is right here: the services being proxied are mostly
	// tunnels and streams, where buffering adds latency and memory for no
	// benefit.
	add("proxy_buffering_off", "true", "buffering_tunnels",
		"tunnelled and streamed traffic must not be buffered")

	// ── Sizes ────────────────────────────────────────────────
	body := 32
	if small {
		body = 16
	} else if busy {
		body = 128
	}
	add("client_max_body_mb", fmt.Sprint(body), "body_size",
		fmt.Sprintf("nginx's own default is 1 MB, which rejects most uploads"))
	add("client_body_buffer_kb", "128", "body_buffer",
		"below this a body stays in memory instead of hitting a temp file")
	add("large_client_header_kb", "8", "header_buffer",
		"covers large cookies and long URLs")

	// ── Compression ──────────────────────────────────────────
	add("gzip_enabled", "true", "gzip_on",
		"less bandwidth means shorter connections and less memory held")
	lvl := 5
	if small {
		lvl = 4 // CPU is the scarcer resource on a small box
	} else if busy {
		lvl = 6
	}
	add("gzip_comp_level", fmt.Sprint(lvl), "gzip_level",
		"above 6 the extra CPU buys almost nothing")
	add("gzip_min_length", "1024", "gzip_min",
		"below about one packet, compressing costs more than it saves")

	// ── TLS ──────────────────────────────────────────────────
	// 1 MB holds roughly 4,000 sessions. Scale with memory but stay
	// modest: this is memory taken away from the backends.
	cache := 10
	if small {
		cache = 4
	} else if busy {
		cache = 32
	}
	add("ssl_session_cache_mb", fmt.Sprint(cache), "tls_cache",
		fmt.Sprintf("~%d thousand resumable sessions; a resumed handshake is far cheaper", cache*4))
	add("ssl_session_timeout_min", "60", "tls_timeout", "one hour of resumable sessions")
	add("ssl_ecdh_curve", "X25519:prime256v1:secp384r1", "ecdh_curve",
		"setting the list explicitly is what fixes 'bad key share'")

	// ── Privacy and limits ───────────────────────────────────
	add("server_tokens_off", "true", "tokens_off",
		"there is no reason to publish the nginx version")

	// limit_conn: generous, because a shared NAT puts many real users
	// behind one address and a browser alone opens six connections.
	lc := 64
	if small {
		lc = 48
	} else if busy {
		lc = 128
	}
	add("limit_conn_per_ip", fmt.Sprint(lc), "limit_conn",
		"a browser opens ~6; a shared NAT multiplies that by its users")

	// ── Static files ─────────────────────────────────────────
	ofc := 1000
	if small {
		ofc = 512
	} else if busy {
		ofc = 4000
	}
	add("open_file_cache_max", fmt.Sprint(ofc), "open_file_cache",
		"caches file metadata for the served site")

	// Never recommended. It exists because some operators genuinely want
	// it, but recommending it would silently break the statistics page
	// and the 404-flood ban rule, both of which read that log.
	add("access_log_off", "false", "access_log_needed",
		"the statistics page and the 404 ban rule both read this log")

	return out
}

// roundDownNice rounds to a readable step so a computed number does not
// look like a rounding accident.
func roundDownNice(n int) int {
	steps := []int{512, 768, 1024, 1536, 2048, 3072, 4096, 6144, 8192, 12288, 16384}
	best := steps[0]
	for _, s := range steps {
		if s <= n {
			best = s
		}
	}
	return best
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ApplyRecommendations fills a Tuning from a recommendation set.
//
// Used by the "fill everything" button and by the installer, so a fresh
// install starts with values that suit the machine rather than with nginx's
// defaults, which suit nothing in particular.
func ApplyRecommendations(t Tuning, recs map[string]Recommendation) Tuning {
	geti := func(k string) int {
		var n int
		if r, ok := recs[k]; ok {
			_, _ = fmt.Sscanf(r.Value, "%d", &n)
		}
		return n
	}
	getb := func(k string) bool { return recs[k].Value == "true" }
	gets := func(k string) string { return recs[k].Value }

	t.WorkerProcesses = gets("worker_processes")
	t.WorkerRLimitNofile = geti("worker_rlimit_nofile")
	t.MultiAccept = getb("multi_accept")

	t.KeepaliveTimeout = geti("keepalive_timeout")
	t.KeepaliveRequests = geti("keepalive_requests")
	t.ClientHeaderTimeout = geti("client_header_timeout")
	t.ClientBodyTimeout = geti("client_body_timeout")
	t.SendTimeout = geti("send_timeout")

	t.ProxyConnectTimeout = geti("proxy_connect_timeout")
	t.ProxyReadTimeout = geti("proxy_read_timeout")
	t.ProxySendTimeout = geti("proxy_send_timeout")
	t.ProxySocketKeepalive = getb("proxy_socket_keepalive")
	t.UpstreamKeepalive = geti("upstream_keepalive")
	t.ProxyBufferingOff = getb("proxy_buffering_off")

	t.ClientMaxBodyMB = geti("client_max_body_mb")
	t.ClientBodyBufferKB = geti("client_body_buffer_kb")
	t.LargeClientHeaderKB = geti("large_client_header_kb")

	t.GzipEnabled = getb("gzip_enabled")
	t.GzipCompLevel = geti("gzip_comp_level")
	t.GzipMinLength = geti("gzip_min_length")

	t.SSLSessionCacheMB = geti("ssl_session_cache_mb")
	t.SSLSessionTimeoutMin = geti("ssl_session_timeout_min")
	t.SSLECDHCurve = gets("ssl_ecdh_curve")

	t.ServerTokensOff = getb("server_tokens_off")
	t.LimitConnPerIP = geti("limit_conn_per_ip")
	t.OpenFileCacheMax = geti("open_file_cache_max")
	// access_log_off is deliberately NOT applied from a recommendation:
	// the recommendation is always "false", and an operator who turned it
	// on knew what they were doing.
	return t
}

// ValidateTuning rejects values nginx would refuse, with a readable reason.
//
// Refusing at save time rather than at reload time matters: a reload that
// fails is rolled back, so the operator sees "nginx rejected the config"
// with no idea which of thirty fields did it.
func ValidateTuning(t Tuning) error {
	if !t.Enabled {
		return nil
	}
	if wp := strings.TrimSpace(t.WorkerProcesses); wp != "" && wp != "auto" {
		var n int
		if _, err := fmt.Sscanf(wp, "%d", &n); err != nil || n < 1 || n > 128 {
			return fmt.Errorf("worker_processes must be \"auto\" or a number from 1 to 128")
		}
	}
	type rng struct {
		name     string
		v        int
		min, max int
	}
	for _, c := range []rng{
		{"worker_rlimit_nofile", t.WorkerRLimitNofile, 1024, 1048576},
		{"keepalive_timeout", t.KeepaliveTimeout, 1, 3600},
		{"keepalive_requests", t.KeepaliveRequests, 1, 1000000},
		{"client_header_timeout", t.ClientHeaderTimeout, 1, 3600},
		{"client_body_timeout", t.ClientBodyTimeout, 1, 3600},
		{"send_timeout", t.SendTimeout, 1, 3600},
		{"proxy_connect_timeout", t.ProxyConnectTimeout, 1, 600},
		{"proxy_read_timeout", t.ProxyReadTimeout, 1, 86400},
		{"proxy_send_timeout", t.ProxySendTimeout, 1, 86400},
		{"upstream_keepalive", t.UpstreamKeepalive, 1, 4096},
		{"client_max_body_mb", t.ClientMaxBodyMB, 1, 10240},
		{"client_body_buffer_kb", t.ClientBodyBufferKB, 1, 65536},
		{"large_client_header_kb", t.LargeClientHeaderKB, 1, 1024},
		{"gzip_comp_level", t.GzipCompLevel, 1, 9},
		{"gzip_min_length", t.GzipMinLength, 1, 1048576},
		{"ssl_session_cache_mb", t.SSLSessionCacheMB, 1, 1024},
		{"ssl_session_timeout_min", t.SSLSessionTimeoutMin, 1, 1440},
		{"limit_conn_per_ip", t.LimitConnPerIP, 1, 100000},
		{"open_file_cache_max", t.OpenFileCacheMax, 1, 1000000},
	} {
		// Zero means "leave nginx's default alone" and is always valid.
		if c.v == 0 {
			continue
		}
		if c.v < c.min || c.v > c.max {
			return fmt.Errorf("%s must be between %d and %d (got %d)",
				c.name, c.min, c.max, c.v)
		}
	}
	// The curve list is written straight into the config, so anything that
	// could terminate a directive is refused rather than escaped.
	if c := strings.TrimSpace(t.SSLECDHCurve); c != "" {
		if strings.ContainsAny(c, "${}';\"\\ \t\r\n") {
			return fmt.Errorf("ssl_ecdh_curve may only contain curve names separated by colons")
		}
	}
	// A descriptor ceiling below twice the connection limit is the
	// classic misconfiguration: nginx accepts connections until it runs
	// out of file descriptors and then logs "too many open files" while
	// the connection counter says it has plenty of room.
	if t.WorkerRLimitNofile > 0 {
		// WorkerConnections lives in NginxSettings, so the caller checks
		// that pairing; here we can only bound the obvious.
		if t.WorkerRLimitNofile < 1024 {
			return fmt.Errorf("worker_rlimit_nofile below 1024 will make nginx refuse connections")
		}
	}
	return nil
}

// TuningWarnings returns non-fatal advice about the CURRENT combination.
//
// Separate from validation because these are choices an operator may
// legitimately make, and refusing them would be presumptuous. Saying
// "you have switched off the log that the statistics page reads" is not.
func TuningWarnings(t Tuning, workerConnections int) []string {
	var out []string
	if !t.Enabled {
		return nil
	}
	if t.AccessLogOff {
		out = append(out, "access_log_off_breaks_stats")
	}
	if t.WorkerRLimitNofile > 0 && workerConnections > 0 &&
		t.WorkerRLimitNofile < workerConnections*2 {
		out = append(out, "nofile_below_connections")
	}
	if t.ProxyReadTimeout > 0 && t.ProxyReadTimeout < 300 {
		out = append(out, "proxy_read_too_short_for_tunnels")
	}
	if t.GzipEnabled && t.GzipCompLevel >= 8 {
		out = append(out, "gzip_level_wasteful")
	}
	if t.KeepaliveTimeout > 0 && t.KeepaliveTimeout > 120 {
		out = append(out, "keepalive_long_on_small_box")
	}
	return out
}
