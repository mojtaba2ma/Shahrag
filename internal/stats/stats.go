// Package stats collects and aggregates request/connection metrics
// using in-memory ring buffers. No external dependencies.
package stats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
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

type TopEntry struct {
	Key   string `json:"ip"`
	Count int64  `json:"cnt"`
	Bytes int64  `json:"bytes"`
}

type Collector struct {
	mu       sync.RWMutex
	buckets  []*MinuteBucket
	conns    []ConnectionSnapshot
	topIPs   map[string]*ipAgg
	topPaths map[string]*pathAgg
	maxAge   time.Duration
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
		maxAge:   24 * time.Hour,
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
	out := []map[string]interface{}{}
	for _, b := range c.buckets {
		if b.TS < since {
			continue
		}
		out = append(out, map[string]interface{}{
			"ts":      b.TS,
			"total":   b.Total,
			"success": b.Success,
			"redirect": b.Redirect,
			"error":   b.ClientErr + b.ServerErr,
			"bytes":   b.Bytes,
		})
	}
	return out
}

func (c *Collector) ConnectionsTimeseries(minutes int) []ConnectionSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).Unix()
	out := []ConnectionSnapshot{}
	for _, s := range c.conns {
		if s.TS >= since {
			out = append(out, s)
		}
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
	// Parse logs every 30s; snapshot connections every 10s.
	logTicker := time.NewTicker(30 * time.Second)
	connTicker := time.NewTicker(10 * time.Second)
	gcTicker := time.NewTicker(10 * time.Minute)
	defer logTicker.Stop()
	defer connTicker.Stop()
	defer gcTicker.Stop()

	// Initial run
	c.parseLogs()
	c.snapshotConnections()
	for {
		select {
		case <-logTicker.C:
			c.parseLogs()
		case <-connTicker.C:
			c.snapshotConnections()
		case <-gcTicker.C:
			c.gc()
		}
	}
}

// parseLogs parses the nginx access log (best-effort).
// Expected combined format:
// 1.2.3.4 - - [13/Aug/2026:12:00:00 +0000] "GET /path HTTP/1.1" 200 1234 "ref" "ua"
var accessRe = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "(\S+) (\S+) [^"]*" (\d{3}) (\d+|-)`)

func (c *Collector) parseLogs() {
	path := "/var/log/nginx/access.log"
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
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
		path := m[4]
		c.record(ip, path, status, bytes)
	}
}

func (c *Collector) record(ip, path string, status int, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	minute := now.Truncate(time.Minute).Unix()

	var b *MinuteBucket
	if len(c.buckets) > 0 && c.buckets[len(c.buckets)-1].TS == minute {
		b = c.buckets[len(c.buckets)-1]
	} else {
		b = &MinuteBucket{TS: minute, UniqIPs: map[string]bool{}}
		c.buckets = append(c.buckets, b)
	}
	b.Total++
	b.Bytes += bytes
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

func (c *Collector) gc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-c.maxAge).Unix()
	out := c.buckets[:0]
	for _, b := range c.buckets {
		if b.TS >= cutoff {
			out = append(out, b)
		}
	}
	c.buckets = out
	out2 := c.conns[:0]
	for _, s := range c.conns {
		if s.TS >= cutoff {
			out2 = append(out2, s)
		}
	}
	c.conns = out2

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
		Buckets []*MinuteBucket     `json:"buckets"`
		Conns   []ConnectionSnapshot `json:"conns"`
	}{c.buckets, c.conns})
}

func (c *Collector) String() string { return fmt.Sprintf("Collector(buckets=%d)", len(c.buckets)) }
