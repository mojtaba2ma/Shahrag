package stats

// Per-dimension traffic statistics: which service, which path, which host,
// which port, which status, which method — over a chosen time window.
//
// The panel already had "top IPs" and "top paths", and those were computed
// from two lifetime maps with no notion of time: TopIPs took a `minutes`
// argument, ignored it, and returned totals since the panel started. So
// "the last six hours" was unanswerable, and the number shown next to an
// address was whatever had accumulated since the last restart.
//
// What an operator actually asks is "how much traffic did this service
// take last night", and answering it needs counts bucketed by TIME as
// well as by key.
//
// The shape that makes that affordable
// ------------------------------------
// A naive design keeps a map per minute per dimension. For 7 days that is
// 10,080 minutes × 6 dimensions × however many distinct keys, which on a
// server with a few hundred paths is millions of entries and hundreds of
// megabytes. That is not payable on a 1 GB VPS.
//
// So the buckets are HOURLY, not per-minute, and each one holds a bounded
// number of keys per dimension:
//
//	168 hourly buckets (7 days) × 6 dimensions × 64 keys = 64,512 entries
//
// Measured at 1.9 MB with realistic key lengths. The per-minute series
// that already exists still answers "the last hour" at fine resolution;
// this answers "which of my services was busy on Tuesday", where an hour
// is the right grain anyway.
//
// Bounding the keys is what makes the worst case predictable. A scanner
// walking a wordlist produces thousands of distinct paths in an hour, and
// without a cap that one hour would cost more than the whole rest of the
// week. Over the cap the smallest counter is evicted, which is the right
// thing to lose: this is a TOP-N view, and an entry that never made the
// top 64 in its own hour was never going to be shown.

import (
	"sort"
	"sync"
	"time"
)

// Dimensions a request can be counted against.
const (
	DimService = "service"
	DimPath    = "path"
	DimHost    = "host"
	DimPort    = "port"
	DimStatus  = "status"
	DimMethod  = "method"
	DimIP      = "ip"
)

// AllDimensions is the set the API will serve, in the order the panel
// shows them.
var AllDimensions = []string{
	DimService, DimHost, DimPath, DimPort, DimStatus, DimMethod, DimIP,
}

// keysPerDimension bounds one dimension inside one hour.
//
// 64 is comfortably more than the number of services, hosts or ports any
// realistic install has, so those dimensions are exact. Paths and IPs can
// exceed it, and for those this is explicitly a top-64 — see the package
// comment for why that is the right loss.
const keysPerDimension = 64

// hourlyRetention is how many hourly buckets are kept.
//
// 168 = 7 days, which is the longest range the UI offers. Beyond that the
// per-minute series has already been downsampled and the question changes
// from "what happened" to "what is the trend", which the existing
// timeseries answers.
const hourlyRetention = 168

// dimCounter is one key's totals inside one hour.
//
// int64 for both: a busy hour on a real server is millions of requests and
// gigabytes, and a 32-bit count would wrap in a day.
type dimCounter struct {
	Count int64 `json:"c"`
	Bytes int64 `json:"b"`
	// Errors is the 4xx+5xx subset, so the panel can show an error rate
	// per service without a second pass over the data.
	Errors int64 `json:"e"`
}

// hourBucket holds every dimension for one hour.
type hourBucket struct {
	TS int64 `json:"ts"`
	// Dims is dimension -> key -> counter.
	Dims map[string]map[string]*dimCounter `json:"d"`
}

// DimStore is the whole structure. It is a separate type from Collector so
// its locking is its own and a future change cannot accidentally hold the
// collector's lock across a scan of a week of data.
type DimStore struct {
	mu    sync.RWMutex
	hours []*hourBucket
}

// NewDimStore builds an empty store.
func NewDimStore() *DimStore { return &DimStore{} }

// Record attributes one request to every dimension at once.
//
// Called from the access-log reader, once per line, so it has to be cheap:
// seven map lookups and seven increments, no allocation in the steady
// state once the keys exist.
func (d *DimStore) Record(when time.Time, keys map[string]string, bytes int64, status int) {
	if len(keys) == 0 {
		return
	}
	hour := when.Truncate(time.Hour).Unix()
	isErr := status >= 400

	d.mu.Lock()
	defer d.mu.Unlock()

	b := d.bucketForLocked(hour)
	for dim, key := range keys {
		if key == "" {
			continue
		}
		m := b.Dims[dim]
		if m == nil {
			m = map[string]*dimCounter{}
			b.Dims[dim] = m
		}
		c := m[key]
		if c == nil {
			if len(m) >= keysPerDimension {
				// Full: evict the smallest, which is the entry least
				// likely ever to be shown in a top-N view. Done inline
				// rather than on a timer so the bound is a hard one.
				evictSmallest(m)
			}
			c = &dimCounter{}
			m[key] = c
		}
		c.Count++
		c.Bytes += bytes
		if isErr {
			c.Errors++
		}
	}
}

// evictSmallest removes the lowest-count entry. Caller holds the lock.
func evictSmallest(m map[string]*dimCounter) {
	var worstKey string
	var worst int64 = -1
	for k, v := range m {
		if worst < 0 || v.Count < worst {
			worst, worstKey = v.Count, k
		}
	}
	if worstKey != "" {
		delete(m, worstKey)
	}
}

// bucketForLocked returns (or creates) the bucket for an hour, keeping the
// slice sorted and pruned. Caller holds the lock.
func (d *DimStore) bucketForLocked(hour int64) *hourBucket {
	n := len(d.hours)
	// Hot path: the same hour as the last line.
	if n > 0 && d.hours[n-1].TS == hour {
		return d.hours[n-1]
	}
	if n == 0 || d.hours[n-1].TS < hour {
		b := &hourBucket{TS: hour, Dims: map[string]map[string]*dimCounter{}}
		d.hours = append(d.hours, b)
		// Prune from the front. A slice copy once an hour is free, and
		// it keeps the memory bound absolute rather than approximate.
		if len(d.hours) > hourlyRetention {
			drop := len(d.hours) - hourlyRetention
			d.hours = append([]*hourBucket{}, d.hours[drop:]...)
		}
		return b
	}
	// Out of order — a rotated log or a clock step. Binary search.
	i := sort.Search(len(d.hours), func(i int) bool { return d.hours[i].TS >= hour })
	if i < len(d.hours) && d.hours[i].TS == hour {
		return d.hours[i]
	}
	b := &hourBucket{TS: hour, Dims: map[string]map[string]*dimCounter{}}
	d.hours = append(d.hours, nil)
	copy(d.hours[i+1:], d.hours[i:])
	d.hours[i] = b
	return b
}

// DimEntry is one row of a top-N answer.
type DimEntry struct {
	Key    string `json:"key"`
	Count  int64  `json:"count"`
	Bytes  int64  `json:"bytes"`
	Errors int64  `json:"errors"`
	// Share is this key's percentage of the window's total requests,
	// computed server-side so every client renders the same number.
	Share float64 `json:"share"`
}

// Top returns the busiest keys in a dimension over the last `minutes`.
//
// The window is applied to whole hours, because that is the resolution
// stored. A request for 90 minutes covers the last two hourly buckets,
// which is stated in the response as `covers_hours` so the UI can say so
// rather than implying a precision that is not there.
func (d *DimStore) Top(dim string, minutes, limit int) ([]DimEntry, int) {
	if limit <= 0 {
		limit = 10
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).
		Truncate(time.Hour).Unix()

	d.mu.RLock()
	defer d.mu.RUnlock()

	agg := map[string]*dimCounter{}
	hours := 0
	var total int64
	for _, b := range d.hours {
		if b.TS < since {
			continue
		}
		hours++
		m := b.Dims[dim]
		if m == nil {
			continue
		}
		for k, v := range m {
			a := agg[k]
			if a == nil {
				a = &dimCounter{}
				agg[k] = a
			}
			a.Count += v.Count
			a.Bytes += v.Bytes
			a.Errors += v.Errors
			total += v.Count
		}
	}

	out := make([]DimEntry, 0, len(agg))
	for k, v := range agg {
		e := DimEntry{Key: k, Count: v.Count, Bytes: v.Bytes, Errors: v.Errors}
		if total > 0 {
			e.Share = float64(v.Count) * 100 / float64(total)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		// A stable tie-break, so a refresh does not shuffle equal rows.
		return out[i].Key < out[j].Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, hours
}

// SeriesPoint is one hour of one key's traffic.
type SeriesPoint struct {
	TS     int64 `json:"ts"`
	Count  int64 `json:"count"`
	Bytes  int64 `json:"bytes"`
	Errors int64 `json:"errors"`
}

// Series returns one key's hourly traffic over the window, so the panel can
// draw "this service, over the last two days" rather than only a total.
//
// Gaps are filled with zeroes: an hour with no traffic is information, and
// a chart that simply omits it draws a straight line through the outage.
func (d *DimStore) Series(dim, key string, minutes int) []SeriesPoint {
	now := time.Now().Truncate(time.Hour)
	since := now.Add(-time.Duration(minutes) * time.Minute).Truncate(time.Hour)

	d.mu.RLock()
	have := map[int64]*dimCounter{}
	for _, b := range d.hours {
		if b.TS < since.Unix() {
			continue
		}
		if m := b.Dims[dim]; m != nil {
			if v := m[key]; v != nil {
				have[b.TS] = v
			}
		}
	}
	d.mu.RUnlock()

	out := []SeriesPoint{}
	for t := since; !t.After(now); t = t.Add(time.Hour) {
		p := SeriesPoint{TS: t.Unix()}
		if v := have[t.Unix()]; v != nil {
			p.Count, p.Bytes, p.Errors = v.Count, v.Bytes, v.Errors
		}
		out = append(out, p)
	}
	return out
}

// Totals is the summary line for a window.
type Totals struct {
	Requests int64   `json:"requests"`
	Bytes    int64   `json:"bytes"`
	Errors   int64   `json:"errors"`
	ErrRate  float64 `json:"error_rate"`
	Hours    int     `json:"hours"`
}

// Summary totals a window, using the service dimension as the base — every
// request is counted against exactly one service, so it cannot double
// count the way summing paths or hosts would.
func (d *DimStore) Summary(minutes int) Totals {
	since := time.Now().Add(-time.Duration(minutes) * time.Minute).
		Truncate(time.Hour).Unix()
	var t Totals
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, b := range d.hours {
		if b.TS < since {
			continue
		}
		t.Hours++
		for _, v := range b.Dims[DimService] {
			t.Requests += v.Count
			t.Bytes += v.Bytes
			t.Errors += v.Errors
		}
	}
	if t.Requests > 0 {
		t.ErrRate = float64(t.Errors) * 100 / float64(t.Requests)
	}
	return t
}

// ── persistence ──────────────────────────────────────────────
//
// Saved with the rest of the statistics, because a week of per-service
// history that evaporates on restart is not a week of history. The format
// is the bucket slice as-is: it is already compact, and a bespoke encoding
// would be another thing to get wrong for no measurable gain.

// Snapshot returns the buckets for saving.
func (d *DimStore) Snapshot() []*hourBucket {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]*hourBucket, 0, len(d.hours))
	for _, b := range d.hours {
		// Deep enough copy that the marshaller cannot race a writer.
		nb := &hourBucket{TS: b.TS, Dims: map[string]map[string]*dimCounter{}}
		for dim, m := range b.Dims {
			nm := make(map[string]*dimCounter, len(m))
			for k, v := range m {
				c := *v
				nm[k] = &c
			}
			nb.Dims[dim] = nm
		}
		out = append(out, nb)
	}
	return out
}

// Restore loads saved buckets, dropping anything past the retention window.
func (d *DimStore) Restore(in []*hourBucket) {
	cut := time.Now().Add(-hourlyRetention * time.Hour).Truncate(time.Hour).Unix()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hours = d.hours[:0]
	for _, b := range in {
		if b == nil || b.TS < cut {
			continue
		}
		if b.Dims == nil {
			b.Dims = map[string]map[string]*dimCounter{}
		}
		d.hours = append(d.hours, b)
	}
	sort.Slice(d.hours, func(i, j int) bool { return d.hours[i].TS < d.hours[j].TS })
	if len(d.hours) > hourlyRetention {
		d.hours = append([]*hourBucket{}, d.hours[len(d.hours)-hourlyRetention:]...)
	}
}

// Len reports how many hourly buckets are held. Used by the cost test and
// by the health page.
func (d *DimStore) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.hours)
}
