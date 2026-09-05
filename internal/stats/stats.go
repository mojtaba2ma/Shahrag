// Package stats collects and aggregates request/connection metrics
// using in-memory ring buffers. No external dependencies.
package stats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MinuteBucket holds one minute of aggregated request data.
type MinuteBucket struct {
	TS        int64 `json:"ts"`
	Total     int64 `json:"total"`
	Success   int64 `json:"success"`
	Redirect  int64 `json:"redirect"`
	ClientErr int64 `json:"client_error"`
	ServerErr int64 `json:"server_error"`
	Bytes     int64 `json:"bytes"`
	UniqIPs   map[string]bool
}

type ConnectionSnapshot struct {
	TS      int64 `json:"ts"`
	Active  int   `json:"active"`
	Reading int   `json:"reading"`
	Writing int   `json:"writing"`
	Waiting int   `json:"waiting"`
}

// ProtoSnap is one sample of TCP/UDP connection counts.
type ProtoSnap struct {
	TS  int64 `json:"ts"`
	TCP int   `json:"tcp"`
	UDP int   `json:"udp"`
}

// ResourceSnap is one sample of server resource usage (percentages).
type ResourceSnap struct {
	TS   int64   `json:"ts"`
	CPU  float64 `json:"cpu"`
	RAM  float64 `json:"ram"`
	Disk float64 `json:"disk"`
	Swap float64 `json:"swap"`

	// Extremes, populated once a sample has been rolled up into a coarser
	// tier. A 30-second spike to 100% CPU disappears from an hourly mean,
	// and that spike is usually why someone opened the page. Omitted while
	// the sample is still raw, so live charts are unchanged.
	CPUMax float64 `json:"cpu_max,omitempty"`
	CPUMin float64 `json:"cpu_min,omitempty"`
	RAMMax float64 `json:"ram_max,omitempty"`
	RAMMin float64 `json:"ram_min,omitempty"`
}

type cpuCounters struct{ total, idle float64 }

type TopEntry struct {
	Key   string `json:"ip"`
	Count int64  `json:"cnt"`
	Bytes int64  `json:"bytes"`
}

type Collector struct {
	mu        sync.RWMutex
	buckets   []*MinuteBucket
	conns     []ConnectionSnapshot
	protos    []ProtoSnap
	resources []ResourceSnap
	cpuPrev   cpuCounters
	topIPs    map[string]*ipAgg
	topPaths  map[string]*pathAgg
	maxAge    time.Duration

	// Access-log follow state. The log is read incrementally, like
	// `tail -f`: only the bytes appended since the previous pass are
	// parsed. Re-reading the whole file every 30 seconds counted every
	// historical request again on every tick.
	logPos   int64  // byte offset already consumed
	logInode uint64 // inode of the file that offset belongs to
}

type ipAgg struct {
	count int64
	bytes int64
}
type pathAgg struct {
	count int64
	bytes int64
}

func NewCollector() *Collector {
	c := &Collector{
		topIPs:   make(map[string]*ipAgg),
		topPaths: make(map[string]*pathAgg),
		maxAge:   MaxRetention(),
	}
	// History survives restarts. A damaged file is reported and ignored:
	// losing statistics is an inconvenience, refusing to start is an outage.
	if err := c.Load(); err != nil {
		log.Printf("[shahrag] stats: %v", err)
	}
	go c.loop()
	return c
}

// Summary returns high-level stats for the last hour and 24h.
type Summary struct {
	TotalRequests int64 `json:"total_requests"`
	LastHour      struct {
		Requests     int64   `json:"requests"`
		Bytes        int64   `json:"bytes"`
		UniqueIPs    int     `json:"unique_ips"`
		Errors       int64   `json:"errors"`
		ErrorRatePct float64 `json:"error_rate_pct"`
	} `json:"last_hour"`
	Last24h struct {
		Requests  int64 `json:"requests"`
		Bytes     int64 `json:"bytes"`
		UniqueIPs int   `json:"unique_ips"`
	} `json:"last_24h"`
	Connections ConnectionSnapshot `json:"connections"`
}

func (c *Collector) Summary() Summary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	hourAgo := now.Add(-time.Hour).Unix()
	dayAgo := now.Add(-24 * time.Hour).Unix()

	var sum Summary
	for _, b := range c.buckets {
		sum.TotalRequests += b.Total
		if b.TS >= hourAgo {
			sum.LastHour.Requests += b.Total
			sum.LastHour.Bytes += b.Bytes
			sum.LastHour.Errors += b.ClientErr + b.ServerErr
		}
		if b.TS >= dayAgo {
			sum.Last24h.Requests += b.Total
			sum.Last24h.Bytes += b.Bytes
		}
	}
	if sum.LastHour.Requests > 0 {
		sum.LastHour.ErrorRatePct = float64(sum.LastHour.Errors) / float64(sum.LastHour.Requests) * 100
	}
	// Unique IPs over last hour
	ips := map[string]bool{}
	for _, b := range c.buckets {
		if b.TS >= hourAgo {
			for ip := range b.UniqIPs {
				ips[ip] = true
			}
		}
	}
	sum.LastHour.UniqueIPs = len(ips)
	ips24 := map[string]bool{}
	for _, b := range c.buckets {
		if b.TS >= dayAgo {
			for ip := range b.UniqIPs {
				ips24[ip] = true
			}
		}
	}
	sum.Last24h.UniqueIPs = len(ips24)
	if len(c.conns) > 0 {
		sum.Connections = c.conns[len(c.conns)-1]
	}
	return sum
}

// RequestsTimeseries returns per-bucket request counts.
func (c *Collector) RequestsTimeseries(minutes, bucketSec int) []map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	kept := []*MinuteBucket{}
	for _, b := range c.buckets {
		if b == nil || b.TS < since {
			continue
		}
		kept = append(kept, b)
	}
	// Requests are COUNTERS, so thinning them by picking every Nth bucket
	// would silently throw traffic away: a year would appear to have a
	// twelfth of the requests it really had. Group and SUM instead, which
	// is what the tiered roll-up already does on the storage side.
	out := []map[string]interface{}{}
	for _, g := range groupRuns(len(kept), maxChartPoints) {
		agg := map[string]interface{}{}
		var total, success, redirect, errs, bytes int64
		for i := g.from; i < g.to; i++ {
			b := kept[i]
			total += b.Total
			success += b.Success
			redirect += b.Redirect
			errs += b.ClientErr + b.ServerErr
			bytes += b.Bytes
		}
		agg["ts"] = kept[g.from].TS
		agg["total"] = total
		agg["success"] = success
		agg["redirect"] = redirect
		agg["error"] = errs
		agg["bytes"] = bytes
		out = append(out, agg)
	}
	return out
}

// run is a half-open index range [from, to).
type run struct{ from, to int }

// groupRuns splits n items into at most `max` contiguous, near-equal groups.
// Used for counter series, where neighbouring buckets must be summed rather
// than sampled.
func groupRuns(n, max int) []run {
	if n <= 0 {
		return nil
	}
	if n <= max {
		out := make([]run, n)
		for i := range out {
			out[i] = run{i, i + 1}
		}
		return out
	}
	out := make([]run, 0, max)
	for i := 0; i < max; i++ {
		from := i * n / max
		to := (i + 1) * n / max
		if to <= from {
			to = from + 1
		}
		if to > n {
			to = n
		}
		out = append(out, run{from, to})
	}
	return out
}

func (c *Collector) ConnectionsTimeseries(minutes int) []ConnectionSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	all := []ConnectionSnapshot{}
	for _, s := range c.conns {
		if s.TS >= since {
			all = append(all, s)
		}
	}
	out := make([]ConnectionSnapshot, 0, len(all))
	for _, i := range pickIndices(len(all)) {
		out = append(out, all[i])
	}
	return out
}

func (c *Collector) TopIPs(minutes int, limit int) []TopEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)
	// For simplicity, top IPs are tracked globally with rolling 24h; we approximate.
	entries := make([]TopEntry, 0, len(c.topIPs))
	for k, v := range c.topIPs {
		entries = append(entries, TopEntry{Key: k, Count: v.count, Bytes: v.bytes})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Count > entries[j].Count })
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	_ = cutoff
	return entries
}

func (c *Collector) TopPaths(minutes int, limit int) []TopEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := make([]TopEntry, 0, len(c.topPaths))
	for k, v := range c.topPaths {
		entries = append(entries, TopEntry{Key: k, Count: v.count, Bytes: v.bytes})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Count > entries[j].Count })
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

func (c *Collector) StatusDistribution(minutes int) map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	res := map[string]int64{"2xx": 0, "3xx": 0, "4xx": 0, "5xx": 0, "other": 0}
	for _, b := range c.buckets {
		if b.TS < since {
			continue
		}
		res["2xx"] += b.Success
		res["3xx"] += b.Redirect
		res["4xx"] += b.ClientErr
		res["5xx"] += b.ServerErr
	}
	return res
}

// ── Background collection ──────────────────────────────────

func (c *Collector) loop() {
	// Parse logs every 30s; snapshot connections every 10s; sample
	// TCP/UDP counts and server resources every 5s (live charts).
	logTicker := time.NewTicker(30 * time.Second)
	connTicker := time.NewTicker(10 * time.Second)
	resTicker := time.NewTicker(5 * time.Second)
	gcTicker := time.NewTicker(10 * time.Minute)
	saveTicker := time.NewTicker(SaveInterval)
	defer logTicker.Stop()
	defer connTicker.Stop()
	defer resTicker.Stop()
	defer gcTicker.Stop()
	defer saveTicker.Stop()

	// Initial run
	c.parseLogs()
	c.snapshotConnections()
	c.sampleProto()
	c.sampleResources()
	for {
		select {
		case <-logTicker.C:
			c.parseLogs()
		case <-connTicker.C:
			c.snapshotConnections()
		case <-resTicker.C:
			c.sampleProto()
			c.sampleResources()
		case <-gcTicker.C:
			c.gc()
		case <-saveTicker.C:
			// Every 5 minutes, not every sample: samples arrive every 5
			// seconds and this data is only ever read after a restart, so
			// writing more often would be pointless SSD wear.
			if err := c.Save(); err != nil {
				log.Printf("[shahrag] stats: could not save: %v", err)
			}
		}
	}
}

// parseLogs parses the nginx access log incrementally.
//
// Expected combined format:
// 1.2.3.4 - - [13/Aug/2026:12:00:00 +0000] "GET /path HTTP/1.1" 200 1234 "ref" "ua"
//
// Two properties matter and both were wrong before:
//
//   - Only NEW bytes are read. The whole file used to be re-parsed every
//     30 seconds, so a log holding 10,000 requests added 10,000 requests to
//     the counters twice a minute. Traffic charts were pure fiction, growing
//     with the size of the log rather than with actual traffic.
//   - Rotation is detected by inode and by the file shrinking. logrotate
//     either renames (new inode) or truncates (size below the offset); in
//     both cases reading resumes from the start of the new file instead of
//     seeking past its end and going silent.
var accessLogPath = envOrDefault("SHAHRAG_ACCESS_LOG", "/var/log/nginx/access.log")

var accessRe = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "(\S+) (\S+) [^"]*" (\d{3}) (\d+|-)`)

// accessTimeLayout is nginx's default log time format.
const accessTimeLayout = "02/Jan/2006:15:04:05 -0700"

func (c *Collector) parseLogs() {
	f, err := os.Open(accessLogPath)
	if err != nil {
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return
	}
	inode := statInode(fi)

	c.mu.Lock()
	pos, prevInode := c.logPos, c.logInode
	c.mu.Unlock()

	switch {
	case prevInode == 0:
		// First pass of this process. Start at the end: history comes back
		// from the persisted state file, and replaying a rotated 200 MB log
		// at boot would both stall startup and date every old request to
		// now.
		pos = fi.Size()
	case inode != prevInode, fi.Size() < pos:
		// Rotated or truncated — the offset belongs to a file that is gone.
		pos = 0
	}

	if pos > fi.Size() {
		pos = fi.Size()
	}
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	read := pos
	for scanner.Scan() {
		line := scanner.Text()
		read += int64(len(line)) + 1 // +1 for the newline the scanner ate
		m := accessRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ip := m[1]
		status, _ := strconv.Atoi(m[5])
		bytes, _ := strconv.ParseInt(m[6], 10, 64)
		if m[6] == "-" {
			bytes = 0
		}
		// The line carries its own timestamp; use it. Stamping everything
		// with time.Now() piled a whole rotation's worth of requests into
		// the current minute whenever the panel caught up after downtime.
		when := time.Now()
		if ts, err := time.Parse(accessTimeLayout, m[2]); err == nil {
			when = ts
		}
		c.recordAt(when, ip, m[4], status, bytes)
	}
	if err := scanner.Err(); err != nil {
		// A partially read pass must not advance past the bytes actually
		// consumed, otherwise the tail of the file is skipped forever.
		read = pos
	}

	c.mu.Lock()
	c.logPos, c.logInode = read, inode
	c.mu.Unlock()
}

// statInode returns the inode of a stat result, or 0 when the platform does
// not expose one (rotation is then detected by size alone).
func statInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 1
}

// record attributes a request to the current minute.
func (c *Collector) record(ip, path string, status int, bytes int64) {
	c.recordAt(time.Now(), ip, path, status, bytes)
}

// recordAt attributes a request to the minute `when` falls in.
//
// The bucket list is kept sorted by timestamp. Log lines normally arrive in
// order, so the common path is still "append or reuse the last bucket", but
// an out-of-order line (a rotated log, a clock step) now lands in its own
// minute instead of corrupting the newest one.
func (c *Collector) recordAt(when time.Time, ip, path string, status int, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	minute := when.Truncate(time.Minute).Unix()

	b := c.bucketFor(minute)
	b.Total++
	b.Bytes += bytes
	// Buckets restored from disk have no IP set: it is deliberately not
	// persisted because it is the heaviest field and only meaningful live.
	// Writing into that nil map crashed the whole panel on the first
	// request after a restart.
	if b.UniqIPs == nil {
		b.UniqIPs = map[string]bool{}
	}
	b.UniqIPs[ip] = true
	switch {
	case status >= 200 && status < 300:
		b.Success++
	case status >= 300 && status < 400:
		b.Redirect++
	case status >= 400 && status < 500:
		b.ClientErr++
	case status >= 500:
		b.ServerErr++
	}

	if agg, ok := c.topIPs[ip]; ok {
		agg.count++
		agg.bytes += bytes
	} else {
		c.topIPs[ip] = &ipAgg{count: 1, bytes: bytes}
	}
	// Truncate path to first 2 path segments
	short := path
	if len(short) > 80 {
		short = short[:80]
	}
	if agg, ok := c.topPaths[short]; ok {
		agg.count++
		agg.bytes += bytes
	} else {
		c.topPaths[short] = &pathAgg{count: 1, bytes: bytes}
	}
}

// bucketFor returns the bucket for a minute, creating it in sorted position
// if it does not exist. Caller holds the lock.
func (c *Collector) bucketFor(minute int64) *MinuteBucket {
	n := len(c.buckets)
	if n > 0 && c.buckets[n-1].TS == minute {
		return c.buckets[n-1] // hot path: same minute as the last line
	}
	if n == 0 || c.buckets[n-1].TS < minute {
		b := &MinuteBucket{TS: minute, UniqIPs: map[string]bool{}}
		c.buckets = append(c.buckets, b)
		return b
	}
	i := sort.Search(n, func(i int) bool { return c.buckets[i].TS >= minute })
	if i < n && c.buckets[i].TS == minute {
		return c.buckets[i]
	}
	b := &MinuteBucket{TS: minute, UniqIPs: map[string]bool{}}
	c.buckets = append(c.buckets, nil)
	copy(c.buckets[i+1:], c.buckets[i:])
	c.buckets[i] = b
	return b
}

func (c *Collector) snapshotConnections() {
	snap := ConnectionSnapshot{TS: time.Now().Unix()}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8081/nginx_status")
	if err != nil {
		c.mu.Lock()
		c.conns = append(c.conns, snap)
		c.mu.Unlock()
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	txt := string(data)
	if m := regexp.MustCompile(`Active connections:\s*(\d+)`).FindStringSubmatch(txt); m != nil {
		snap.Active, _ = strconv.Atoi(m[1])
	}
	if m := regexp.MustCompile(`Reading:\s*(\d+)\s+Writing:\s*(\d+)\s+Waiting:\s*(\d+)`).FindStringSubmatch(txt); m != nil {
		snap.Reading, _ = strconv.Atoi(m[1])
		snap.Writing, _ = strconv.Atoi(m[2])
		snap.Waiting, _ = strconv.Atoi(m[3])
	}
	c.mu.Lock()
	c.conns = append(c.conns, snap)
	c.mu.Unlock()
}

// ── Proto (TCP/UDP) sampling ──────────────────────────────

func (c *Collector) sampleProto() {
	tcp := countTCPEstablished()
	udp := countUDPSockets()
	c.mu.Lock()
	c.protos = append(c.protos, ProtoSnap{TS: time.Now().Unix(), TCP: tcp, UDP: udp})
	c.mu.Unlock()
}

// countTCPEstablished counts ESTABLISHED TCP connections (v4+v6) from
// /proc/net/tcp{6} — state field "01" = ESTABLISHED.
func countTCPEstablished() int {
	n := 0
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue
			}
			f := strings.Fields(line)
			if len(f) >= 4 && f[3] == "01" {
				n++
			}
		}
	}
	return n
}

// countUDPSockets counts open UDP sockets (v4+v6) from /proc/net/udp{6}.
func countUDPSockets() int {
	n := 0
	for _, table := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue
			}
			f := strings.Fields(line)
			if len(f) >= 2 {
				n++
			}
		}
	}
	return n
}

// ProtoTimeseries returns TCP/UDP samples for the last `minutes`.
func (c *Collector) ProtoTimeseries(minutes int) []ProtoSnap {
	c.mu.RLock()
	defer c.mu.RUnlock()
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	all := []ProtoSnap{}
	for _, s := range c.protos {
		if s.TS >= since {
			all = append(all, s)
		}
	}
	out := make([]ProtoSnap, 0, len(all))
	for _, i := range pickIndices(len(all)) {
		out = append(out, all[i])
	}
	return out
}

// ── Server resources (CPU/RAM/Disk/Swap) ───────────────────

func (c *Collector) sampleResources() {
	snap := ResourceSnap{TS: time.Now().Unix()}
	if st, ok := readCPUStat(); ok {
		if c.cpuPrev.total > 0 {
			dTot := st.total - c.cpuPrev.total
			dIdle := st.idle - c.cpuPrev.idle
			if dTot > 0 {
				snap.CPU = clampPct((dTot - dIdle) / dTot * 100)
			}
		}
		c.cpuPrev = st
	}
	mem := readMeminfo()
	if mem.memTotal > 0 {
		snap.RAM = clampPct(float64(mem.memTotal-mem.memAvail) / float64(mem.memTotal) * 100)
	}
	if mem.swapTotal > 0 {
		snap.Swap = clampPct(float64(mem.swapTotal-mem.swapFree) / float64(mem.swapTotal) * 100)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err == nil && st.Blocks > 0 {
		used := st.Blocks - st.Bfree
		snap.Disk = clampPct(float64(used) / float64(st.Blocks) * 100)
	}
	c.mu.Lock()
	c.resources = append(c.resources, snap)
	c.mu.Unlock()
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func readCPUStat() (cpuCounters, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuCounters{}, false
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuCounters{}, false
	}
	var nums [8]float64
	for i := 1; i < len(f) && i <= 8; i++ {
		nums[i-1], _ = strconv.ParseFloat(f[i], 64)
	}
	total := nums[0] + nums[1] + nums[2] + nums[3] + nums[4] + nums[5] + nums[6] + nums[7]
	idle := nums[3] + nums[4] // idle + iowait
	return cpuCounters{total: total, idle: idle}, true
}

type memInfo struct {
	memTotal, memAvail, swapTotal, swapFree uint64
}

func readMeminfo() memInfo {
	var m memInfo
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			m.memTotal = v
		case "MemAvailable:":
			m.memAvail = v
		case "SwapTotal:":
			m.swapTotal = v
		case "SwapFree:":
			m.swapFree = v
		}
	}
	return m
}

// maxChartPoints caps how many samples any timeseries returns.
//
// Resources are sampled every 5 seconds, so 24 hours is 17,280 points —
// well over a megabyte of JSON for a chart a few hundred pixels wide. The
// browser then has to parse and plot all of it on every 5-second refresh,
// which is what made the long ranges feel wrong: the request is slow, the
// tail arrives late, and the visible axis lags the range you picked.
//
// 720 keeps a 1-hour view at full 5-second resolution and downsamples
// anything longer, which no screen can distinguish anyway.
const maxChartPoints = 720

// downsampleRes thins a slice to at most maxChartPoints, keeping the FIRST
// and LAST sample so the axis still spans the full requested window. Picking
// evenly spaced indices (rather than averaging) keeps spikes visible, which
// is the whole point of a CPU chart.
func downsampleRes(in []ResourceSnap) []ResourceSnap {
	n := len(in)
	if n <= maxChartPoints {
		return in
	}
	out := make([]ResourceSnap, 0, maxChartPoints)
	// step is fractional so the samples stay evenly spread across the range.
	step := float64(n-1) / float64(maxChartPoints-1)
	for i := 0; i < maxChartPoints; i++ {
		idx := int(float64(i)*step + 0.5)
		if idx >= n {
			idx = n - 1
		}
		out = append(out, in[idx])
	}
	return out
}

// pickIndices returns at most maxChartPoints evenly spaced indices over n,
// always including the first and the last so the axis still spans the whole
// requested window.
//
// Only the resource series was capped before. Requests, connections and
// TCP/UDP were not, so selecting "1 year" shipped 8,760 points per chart
// while the resource chart shipped 720 — three charts stuttered and one did
// not, on the very same page.
func pickIndices(n int) []int {
	if n <= maxChartPoints {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	out := make([]int, 0, maxChartPoints)
	step := float64(n-1) / float64(maxChartPoints-1)
	for i := 0; i < maxChartPoints; i++ {
		j := int(float64(i)*step + 0.5)
		if j >= n {
			j = n - 1
		}
		out = append(out, j)
	}
	return out
}

// ResourceTimeseries returns resource samples for the last `minutes`.
func (c *Collector) ResourceTimeseries(minutes int) []ResourceSnap {
	c.mu.RLock()
	defer c.mu.RUnlock()
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	out := []ResourceSnap{}
	for _, s := range c.resources {
		if s.TS >= since {
			out = append(out, s)
		}
	}
	return downsampleRes(out)
}

func (c *Collector) gc() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	// Tiered compaction, not a flat cutoff. Old samples are rolled into
	// coarser buckets rather than deleted, so a year of history costs a
	// fixed couple of megabytes instead of growing without bound.
	c.buckets = compactBuckets(c.buckets, now)
	c.conns = compactConns(c.conns, now)
	c.protos = compactProtos(c.protos, now)
	c.resources = compactResources(c.resources, now)

	// Trim top maps to prevent unbounded growth
	if len(c.topIPs) > 1000 {
		type kv struct {
			k string
			v *ipAgg
		}
		all := make([]kv, 0, len(c.topIPs))
		for k, v := range c.topIPs {
			all = append(all, kv{k, v})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].v.count > all[j].v.count })
		c.topIPs = make(map[string]*ipAgg)
		for _, a := range all[:500] {
			c.topIPs[a.k] = a.v
		}
	}
	if len(c.topPaths) > 1000 {
		type kv struct {
			k string
			v *pathAgg
		}
		all := make([]kv, 0, len(c.topPaths))
		for k, v := range c.topPaths {
			all = append(all, kv{k, v})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].v.count > all[j].v.count })
		c.topPaths = make(map[string]*pathAgg)
		for _, a := range all[:500] {
			c.topPaths[a.k] = a.v
		}
	}
}

// MarshalJSON for snapshot list.
func (c *Collector) MarshalState() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return json.Marshal(struct {
		Buckets []*MinuteBucket      `json:"buckets"`
		Conns   []ConnectionSnapshot `json:"conns"`
	}{c.buckets, c.conns})
}

func (c *Collector) String() string { return fmt.Sprintf("Collector(buckets=%d)", len(c.buckets)) }
