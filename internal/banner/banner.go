// Package banner implements automatic banning: it watches nginx's logs for
// offences, counts them per address inside a rolling window, and maintains
// the list of addresses that should be refused.
//
// Enforcement happens INSIDE nginx, via a generated map, not with iptables.
// An iptables DROP makes a probe time out, and a server whose probes time
// out is exactly the fingerprint that gets an address filtered on some
// networks. A banned client here gets an ordinary 404 instead, so nothing
// about the server looks unusual from outside.
//
// State is persisted, because a ban that evaporates on restart is not a
// ban — an upgrade would hand every scanner a clean slate.
package banner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"shahrag/internal/config"
)

// Reason records why an address was banned, so the panel can explain it.
const (
	ReasonHoneypot  = "honeypot"
	ReasonAuthFail  = "auth_fail"
	ReasonNotFound  = "not_found"
	ReasonErrorRate = "error_rate"
	ReasonManual    = "manual"
)

// Ban is one banned address.
type Ban struct {
	IP        string    `json:"ip"`
	Reason    string    `json:"reason"`
	Hits      int       `json:"hits"`
	BannedAt  time.Time `json:"banned_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Permanent bans carry a far-future expiry; the flag makes the intent
	// explicit rather than requiring the reader to compare dates.
	Permanent bool `json:"permanent"`
}

// Active reports whether the ban still applies.
func (b Ban) Active(now time.Time) bool {
	return b.Permanent || now.Before(b.ExpiresAt)
}

// Remaining returns how long is left, or 0 for an expired ban.
func (b Ban) Remaining(now time.Time) time.Duration {
	if b.Permanent {
		return b.ExpiresAt.Sub(now)
	}
	d := b.ExpiresAt.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// counter tracks one address's offences with a SLIDING DECAY.
//
// Three designs were measured against a distributed scan of 200,000
// addresses, which is the shape that actually hurts — many addresses, few
// offences each:
//
//	one struct per offence, kept 24h   42.66 MB
//	60 per-minute buckets per address 217.37 MB   (968 B/address: worse)
//	decaying counters                   6.71 MB
//
// The middle one was my own first attempt and it was the wrong trade: it
// paid for a full hour of per-minute detail on every address, when the
// question a rule asks is only "how many in the last N minutes?".
//
// A decaying counter answers that with four float32s and one timestamp —
// 24 bytes, fixed, no matter how many requests an address makes. On each
// new offence the existing counts are scaled down by how much of the
// window has elapsed, then incremented. That is the standard approach for
// rate accounting (nginx's own limit_req uses the same idea) and it costs
// one multiply instead of a scan over buckets.
//
// The approximation is deliberate and safe in the right direction: a burst
// is counted almost exactly, while offences trickling in slower than the
// window decay away — which is precisely the behaviour wanted, since a
// scanner bursts and a real visitor does not.
type counter struct {
	// hits per rule, decayed independently. float32 keeps the struct
	// small; the values are compared against thresholds in the tens,
	// where its precision is far more than enough.
	hits [4]float32
	// last is the second at which each rule's count was last updated.
	// PER RULE, not shared: a first attempt used one timestamp and a
	// single widest window, then rescaled short-window rules by the ratio
	// of the windows — which multiplied a fresh burst of 5 down to 0.83
	// and made those rules unable to fire at all. Four int64s is 32 extra
	// bytes and removes the whole class of error.
	last [4]int64
}

// add records one offence for a rule, decaying that rule's count first.
func (c *counter) add(now int64, idx int, windowSec float64) {
	c.hits[idx] = float32(c.value(now, idx, windowSec)) + 1
	c.last[idx] = now
}

// value returns the current decayed count for one rule.
//
// Linear decay over the rule's own window: an offence contributes fully
// when it happens and nothing once a full window has passed. That counts a
// burst almost exactly — which is what a scanner produces — while a slow
// trickle decays away, which is what a real visitor produces.
func (c *counter) value(now int64, idx int, windowSec float64) float64 {
	if c.last[idx] == 0 || windowSec <= 0 {
		return 0
	}
	elapsed := float64(now - c.last[idx])
	if elapsed >= windowSec {
		return 0
	}
	if elapsed <= 0 {
		return float64(c.hits[idx])
	}
	return float64(c.hits[idx]) * (1 - elapsed/windowSec)
}

// ruleIndex maps a reason to its slot. A fixed array rather than a map:
// four entries do not justify a hash table per address.
func ruleIndex(kind string) int {
	switch kind {
	case ReasonHoneypot:
		return 0
	case ReasonAuthFail:
		return 1
	case ReasonNotFound:
		return 2
	case ReasonErrorRate:
		return 3
	}
	return -1
}

// logCursor remembers how far a log file has been read.
//
// Same reasoning as the stats collector: re-reading a log from the start on
// every pass counts every historical line again, which for a BAN engine
// would be catastrophic — one old scan in the file would re-ban its author
// forever, every 15 seconds.
type logCursor struct {
	pos   int64
	inode uint64
}

// Engine watches logs and maintains the ban list.
type Engine struct {
	mu      sync.RWMutex
	cfg     *config.Manager
	bans    map[string]Ban
	events  map[string]*counter
	cursors map[string]*logCursor

	statePath string
	stop      chan struct{}
	// onChange is called when the ban list changes, so the caller can
	// regenerate nginx. Kept as a callback rather than importing the
	// generator, which would make this package depend on nginx.
	onChange func()
	// paths are the logs to watch, overridable for tests.
	honeypotLog string
	accessLog   string

	// saves counts completed Save() calls. Only a test reads it, but it
	// is the only way to assert "a scan that banned 300 addresses saved
	// ONCE" as behaviour rather than as a comment.
	saves atomic.Int64
	// saving / saveAgain coalesce background writes: at most one save is
	// ever in flight, and a change that arrives during one schedules
	// exactly one more rather than being lost or queueing a third.
	saving    atomic.Bool
	saveAgain atomic.Bool
	// saveMu orders the write itself, so a background save cannot land
	// after a later synchronous one and rewind the file.
	saveMu sync.Mutex

	// winCache holds the four rule windows in seconds.
	//
	// These were read from the config on EVERY recorded offence, which
	// takes the config lock and re-parses the file. Measured on a 200,000
	// event scan: 28 seconds, almost all of it in config reads. Cached
	// here and refreshed once per scan, the same work takes under a
	// second.
	winMu    sync.RWMutex
	winCache [4]float64
}

// StatePath is where bans are persisted.
var StatePath = envOr("SHAHRAG_BANS_FILE", "/var/lib/shahrag/bans.json")

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// AccessLogPath is the nginx access log the 404/error rules read.
var AccessLogPath = envOr("SHAHRAG_ACCESS_LOG", "/var/log/nginx/access.log")

// SetOnChange installs the callback fired when the ban list changes.
func (e *Engine) SetOnChange(f func()) {
	e.mu.Lock()
	e.onChange = f
	e.mu.Unlock()
}

// maxTrackedIPs bounds how many addresses are tracked at once.
//
// The counters are small, but a distributed scan from a large botnet can
// still present hundreds of thousands of distinct addresses: 200,000 was
// measured at 31 MB, which is too much to ask of a 1 GB VPS for a feature
// that runs permanently. The cap makes the worst case a fixed ~8 MB.
//
// Overflow is safe to drop. An address is only tracked while it is BELOW
// the ban threshold; anything that crosses it becomes a ban, and bans are
// kept separately and are not capped by this. So the effect of the cap is
// that a single probe from the 50,001st address in an hour is forgotten —
// while any address that probes enough to matter is still caught.
const maxTrackedIPs = 50000

// ScanInterval is how often the logs are read. 15 seconds is a compromise:
// fast enough that a scanner is stopped early in its run, slow enough that
// the work is negligible.
const ScanInterval = 15 * time.Second

// New builds an engine. It does not start watching until Start is called.
func New(cfg *config.Manager, honeypotLog, accessLog string, onChange func()) *Engine {
	e := &Engine{
		cfg:         cfg,
		bans:        map[string]Ban{},
		events:      map[string]*counter{},
		cursors:     map[string]*logCursor{},
		statePath:   StatePath,
		stop:        make(chan struct{}),
		onChange:    onChange,
		honeypotLog: honeypotLog,
		accessLog:   accessLog,
	}
	if c, err := cfg.Read(); err == nil {
		e.refreshWindows(c.AutoBan)
	}
	if err := e.Load(); err != nil {
		log.Printf("[shahrag] bans: %v", err)
	}
	return e
}

// Start begins watching in the background.
func (e *Engine) Start() {
	go e.loop()
}

// Stop ends the watcher and flushes state.
//
// It WAITS for any background save to finish before writing the final one.
// Since r47 a ban is persisted asynchronously (so a scan that bans 300
// addresses does not block on a disk write while holding the lock), which
// means a save can still be in flight when the process is shutting down or
// when a test is tearing its temp directory down. Without this wait the
// engine writes into a directory that is being deleted underneath it —
// harmless in production, an intermittent test failure everywhere else, and
// a genuine race either way.
func (e *Engine) Stop() {
	close(e.stop)
	e.waitForSave()
	if err := e.Save(); err != nil {
		log.Printf("[shahrag] bans: could not save: %v", err)
	}
}

func (e *Engine) loop() {
	scan := time.NewTicker(ScanInterval)
	save := time.NewTicker(5 * time.Minute)
	defer scan.Stop()
	defer save.Stop()

	// Establish the log positions immediately, so the first scan does not
	// treat the existing file as fresh traffic.
	e.primeCursors()

	for {
		select {
		case <-e.stop:
			return
		case <-scan.C:
			e.Scan()
		case <-save.C:
			if err := e.Save(); err != nil {
				log.Printf("[shahrag] bans: could not save: %v", err)
			}
		}
	}
}

// primeCursors seeks every watched log to its end without parsing it.
func (e *Engine) primeCursors() {
	for _, p := range []string{e.honeypotLog, e.accessLog} {
		if p == "" {
			continue
		}
		cur := &logCursor{}
		if fi, err := os.Stat(p); err == nil {
			// The file exists: skip what is already in it, so a restart
			// does not replay an old scan as fresh traffic.
			cur.pos = fi.Size()
			cur.inode = inodeOf(fi)
		}
		// When the file does NOT exist the cursor is still recorded, with
		// a zero offset. That is what makes a log created LATER get read
		// from its beginning.
		//
		// Without this the honeypot log — which nginx only creates when
		// something first trips the trap — was seen for the first time by
		// readNew, which treats a first sighting as "start at the end" and
		// therefore skipped every line already in it. Reproduced live: 4
		// bait hits, threshold 3, and no ban. Every attack that happened
		// before the log existed was invisible.
		e.mu.Lock()
		e.cursors[p] = cur
		e.mu.Unlock()
	}
}

// Scan reads whatever is new in the logs and applies the rules.
func (e *Engine) Scan() {
	c, err := e.cfg.Read()
	if err != nil || !c.AutoBan.Enabled {
		return
	}
	ab := c.AutoBan
	e.refreshWindows(ab)
	now := time.Now()

	if ab.Honeypot.Enabled && e.honeypotLog != "" {
		for _, ip := range e.readHoneypot() {
			e.record(ip, ReasonHoneypot, now)
		}
	}
	if (ab.NotFound.Enabled || ab.ErrorRate.Enabled) && e.accessLog != "" {
		for ip, kinds := range e.readAccess() {
			for _, k := range kinds {
				e.record(ip, k, now)
			}
		}
	}
	e.evaluate(ab, now)
}

// RecordAuthFailure is called by the panel when a login attempt fails.
//
// Not read from a log: the panel already knows, and going through a file
// would add a delay and a parsing step for information it has in hand.
func (e *Engine) RecordAuthFailure(ip string) {
	c, err := e.cfg.Read()
	if err != nil || !c.AutoBan.Enabled || !c.AutoBan.AuthFail.Enabled {
		return
	}
	e.refreshWindows(c.AutoBan)
	now := time.Now()
	e.record(ip, ReasonAuthFail, now)
	e.evaluate(c.AutoBan, now)
}

// record adds one offence for an address.
func (e *Engine) record(ip, kind string, now time.Time) {
	ip = strings.TrimSpace(ip)
	if ip == "" || net.ParseIP(ip) == nil {
		return
	}
	idx := ruleIndex(kind)
	if idx < 0 {
		return
	}
	w := e.ruleWindowSec(idx)
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.events[ip]
	if c == nil {
		if len(e.events) >= maxTrackedIPs {
			// Full. Rather than grow without bound, drop this new
			// address: see maxTrackedIPs for why that is the safe
			// direction. Existing counters keep working, and the map is
			// pruned every scan as entries decay.
			return
		}
		c = &counter{}
		e.events[ip] = c
	}
	c.add(now.Unix(), idx, w)
}

// ruleWindowSec returns one rule's window in seconds, from the cache.
func (e *Engine) ruleWindowSec(idx int) float64 {
	if idx < 0 || idx > 3 {
		return 600
	}
	e.winMu.RLock()
	v := e.winCache[idx]
	e.winMu.RUnlock()
	if v <= 0 {
		return 600
	}
	return v
}

// refreshWindows updates the cache from a config already in hand.
func (e *Engine) refreshWindows(ab config.AutoBan) {
	rules := [4]config.AutoBanRule{ab.Honeypot, ab.AuthFail, ab.NotFound, ab.ErrorRate}
	var w [4]float64
	for i, r := range rules {
		w[i] = r.Window().Seconds()
		if w[i] <= 0 {
			w[i] = 600
		}
	}
	e.winMu.Lock()
	e.winCache = w
	e.winMu.Unlock()
}

// windowSec is the decay window shared by every counter: the longest window
// any enabled rule uses. One shared window keeps a counter to 24 bytes;
// per-rule windows would need a timestamp each and quadruple that for no
// practical gain, since the rules' windows are usually within a factor of
// two of one another.
func (e *Engine) windowSec() float64 {
	e.winMu.RLock()
	defer e.winMu.RUnlock()
	longest := 0.0
	for _, v := range e.winCache {
		if v > longest {
			longest = v
		}
	}
	if longest <= 0 {
		return 600
	}
	return longest
}

// evaluate applies every enabled rule and bans anything over threshold.
func (e *Engine) evaluate(ab config.AutoBan, now time.Time) {
	rules := []struct {
		kind string
		rule config.AutoBanRule
	}{
		{ReasonHoneypot, ab.Honeypot},
		{ReasonAuthFail, ab.AuthFail},
		{ReasonNotFound, ab.NotFound},
		{ReasonErrorRate, ab.ErrorRate},
	}

	nowSec := now.Unix()

	e.mu.Lock()
	defer e.mu.Unlock()

	changed := false
	// Collected rather than logged one by one: a botnet scan bans hundreds
	// of addresses in a single pass, and hundreds of log lines is itself a
	// denial of service against whoever has to read the journal.
	var banned []string
	// The durable record is separate from the journal line above: the
	// journal is summarised for readability, the file keeps every event so
	// "was this user blocked last Tuesday?" can still be answered. Built
	// here and written once, after the lock is released.
	var events []BanEvent
	// Sorted iteration so a run is reproducible and the log reads sensibly.
	ips := make([]string, 0, len(e.events))
	for ip := range e.events {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	for _, ip := range ips {
		if isAllowed(ip, ab.AllowIPs) {
			// An exempt address never accumulates: keeping its events
			// would ban it the moment it was removed from the list.
			delete(e.events, ip)
			continue
		}
		if b, ok := e.bans[ip]; ok && b.Active(now) {
			continue // already banned; no need to re-decide
		}
		c := e.events[ip]
		if c == nil {
			continue
		}
		for _, r := range rules {
			if !r.rule.Enabled {
				continue
			}
			idx := ruleIndex(r.kind)
			if idx < 0 {
				continue
			}
			n := int(c.value(nowSec, idx, r.rule.Window().Seconds()) + 0.5)
			if n >= r.rule.Threshold() {
				e.bans[ip] = Ban{
					IP: ip, Reason: r.kind, Hits: n,
					BannedAt:  now,
					ExpiresAt: now.Add(r.rule.Duration()),
					Permanent: r.rule.Permanent(),
				}
				banned = append(banned, ip)
				if ab.LogBans {
					events = append(events, BanEvent{
						When: now, Action: "ban", IP: ip, Reason: r.kind,
						Hits: n, Until: now.Add(r.rule.Duration()),
						Permanent: r.rule.Permanent(),
					})
				}
				changed = true
				break
			}
		}
	}

	if len(banned) > 0 {
		if len(banned) <= 3 {
			log.Printf("[shahrag] banned %s", strings.Join(banned, ", "))
		} else {
			log.Printf("[shahrag] banned %d addresses (%s, ...)",
				len(banned), strings.Join(banned[:3], ", "))
		}
	}

	expired := e.pruneLocked(now, ab)
	if expired {
		changed = true
	}
	if len(events) > 0 {
		// One open/write/close for the whole scan, not one per address.
		// Done with the lock still held is tempting for simplicity, but
		// it would put a disk write inside the mutex that every recorded
		// request contends on — so it goes out below instead.
		go LogBanEvents(events)
	}
	if changed {
		// One save for the WHOLE scan, not one per address: a botnet
		// sweep can ban hundreds at once and hundreds of fsyncs would
		// be far worse than the problem being solved.
		//
		// It runs in a goroutine, and that is not laziness. evaluate()
		// holds e.mu through a deferred Unlock, and Save() takes the
		// READ lock — so calling it inline, or even via `defer`,
		// deadlocks: the deferred persist runs BEFORE the deferred
		// unlock, since defers unwind last-in-first-out. Found by
		// TestAScanSavesOnceNotPerAddress hanging.
		//
		// saveOnce collapses concurrent requests, so back-to-back scans
		// cannot pile up writes.
		e.requestSave()
	}
	if changed && e.onChange != nil {
		// Called without the lock held: the callback regenerates nginx,
		// which reads the config and will call back into ActiveBans.
		go e.onChange()
	}
}

// pruneLocked drops expired bans and stale events. Caller holds the lock.
func (e *Engine) pruneLocked(now time.Time, ab config.AutoBan) bool {
	changed := false
	for ip, b := range e.bans {
		if !b.Active(now) {
			delete(e.bans, ip)
			// Clear the history too, or the address is re-banned by the
			// same old events the moment its ban lapses.
			delete(e.events, ip)
			changed = true
		}
	}
	// Forget any address whose counters have all aged out of the ring.
	//
	// This used to keep events for at least 24 hours regardless of the
	// rules, which with a 5-minute rule meant holding data 288 times
	// longer than anything could use it. Now the bound is the ring itself,
	// so an address is forgotten an hour after its last offence and the
	// map tracks only who is CURRENTLY active.
	// Forget an address once EVERY one of its counters has decayed.
	//
	// Checked per rule with that rule's own window: using the widest
	// window for all four kept a 5-minute rule's entries alive for as long
	// as the longest rule allowed, which for the default configuration is
	// an hour — twelve times longer than anything could use them.
	nowSec := now.Unix()
	var win [4]float64
	for i, r := range [4]config.AutoBanRule{ab.Honeypot, ab.AuthFail, ab.NotFound, ab.ErrorRate} {
		win[i] = r.Window().Seconds()
		if win[i] <= 0 {
			win[i] = 600
		}
	}
	for ip, c := range e.events {
		if c == nil {
			delete(e.events, ip)
			continue
		}
		live := false
		for i := 0; i < 4; i++ {
			if c.value(nowSec, i, win[i]) >= 0.5 {
				live = true
				break
			}
		}
		if !live {
			delete(e.events, ip)
		}
	}
	return changed
}

// ActiveBans returns the current bans, newest first.
func (e *Engine) ActiveBans() []Ban {
	now := time.Now()
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Ban, 0, len(e.bans))
	for _, b := range e.bans {
		if b.Active(now) {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BannedAt.After(out[j].BannedAt) })
	return out
}

// BannedIPs returns just the addresses, sorted, for the generator.
func (e *Engine) BannedIPs(max int) []string {
	now := time.Now()
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.bans))
	for ip, b := range e.bans {
		if b.Active(now) {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// Ban adds or replaces a ban by hand.
func (e *Engine) Ban(ip, reason string, d time.Duration, permanent bool) error {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%q is not a valid IP address", ip)
	}
	now := time.Now()
	e.mu.Lock()
	if reason == "" {
		reason = ReasonManual
	}
	if permanent {
		d = 100 * 365 * 24 * time.Hour
	}
	e.bans[ip] = Ban{IP: ip, Reason: reason, Hits: 0,
		BannedAt: now, ExpiresAt: now.Add(d), Permanent: permanent}
	e.mu.Unlock()
	// A manual ban must be on disk by the time this returns: the operator
	// who clicked the button is entitled to assume it stuck, and a reboot
	// five seconds later must not undo it.
	e.persist()
	if e.logBans() {
		go LogBanEvents([]BanEvent{{When: now, Action: "ban", IP: ip,
			Reason: reason, Until: now.Add(d), Permanent: permanent}})
	}
	if e.onChange != nil {
		go e.onChange()
	}
	return nil
}

// Unban removes a ban and forgets the address's history, so it is not
// immediately re-banned by the events that caused it.
func (e *Engine) Unban(ip string) bool {
	e.mu.Lock()
	_, ok := e.bans[ip]
	delete(e.bans, ip)
	delete(e.events, ip)
	e.mu.Unlock()
	if ok {
		// Durable for the same reason, and arguably more urgently: a
		// released address that comes back banned after a reboot is
		// worse than one that was never released, because nobody thinks
		// to look for it.
		e.persist()
	}
	if ok && e.logBans() {
		go LogBanEvents([]BanEvent{{When: time.Now(), Action: "unban", IP: ip}})
	}
	if ok && e.onChange != nil {
		go e.onChange()
	}
	return ok
}

// logBans reads the current setting.
//
// Read fresh rather than cached at startup: the operator can turn the
// recording off in the panel and must not have to restart the service for
// that to take effect. config.Manager.Read is cheap since r44 (101 µs), and
// this is only reached on a ban, which is a rare event.
func (e *Engine) logBans() bool {
	c, err := e.cfg.Read()
	return err == nil && c != nil && c.AutoBan.LogBans
}

// UnbanAll clears everything.
func (e *Engine) UnbanAll() int {
	e.mu.Lock()
	n := len(e.bans)
	e.bans = map[string]Ban{}
	e.events = map[string]*counter{}
	e.mu.Unlock()
	if n > 0 {
		e.persist()
	}
	if n > 0 && e.onChange != nil {
		go e.onChange()
	}
	return n
}

// Pending returns how many offences are recorded for an address that is not
// yet banned. Used by the panel to show "3 of 5" style progress.
func (e *Engine) Pending() map[string]int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	nowSec := time.Now().Unix()
	w := e.windowSec()
	out := map[string]int{}
	for ip, c := range e.events {
		if _, banned := e.bans[ip]; banned || c == nil {
			continue
		}
		total := 0.0
		for i := 0; i < 4; i++ {
			total += c.value(nowSec, i, w)
		}
		if n := int(total + 0.5); n > 0 {
			out[ip] = n
		}
	}
	return out
}

// isAllowed reports whether an address is on the never-ban list.
func isAllowed(ip string, list []string) bool {
	// Loopback is always exempt: the panel's own probes must never ban the
	// server from itself.
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if parsed.IsLoopback() {
		return true
	}
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			if network.Contains(parsed) {
				return true
			}
			continue
		}
		if entry == ip {
			return true
		}
	}
	return false
}

// ── Log reading ──────────────────────────────────────────────

// readHoneypot returns the addresses that tripped the trap since the last
// pass.
func (e *Engine) readHoneypot() []string {
	lines := e.readNew(e.honeypotLog)
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 2 {
			out = append(out, f[1])
		}
	}
	return out
}

// accessRe parses nginx's combined log format.
var accessRe = regexp.MustCompile(`^(\S+) \S+ \S+ \[[^\]]+\] "[^"]*" (\d{3}) `)

// readAccess returns the offences found in the access log since the last
// pass, keyed by address.
func (e *Engine) readAccess() map[string][]string {
	out := map[string][]string{}
	for _, l := range e.readNew(e.accessLog) {
		m := accessRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ip := m[1]
		status, _ := strconv.Atoi(m[2])
		switch {
		case status == 404:
			out[ip] = append(out[ip], ReasonNotFound, ReasonErrorRate)
		case status >= 400:
			out[ip] = append(out[ip], ReasonErrorRate)
		}
	}
	return out
}

// readNew returns the lines appended since the previous call.
//
// Rotation is handled both ways logrotate does it — rename (the inode
// changes) and copytruncate (the size drops below the saved offset) — so a
// rotated log resumes from the start of the new file instead of seeking
// past its end and going permanently silent.
func (e *Engine) readNew(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	ino := inodeOf(fi)

	e.mu.Lock()
	cur, seen := e.cursors[path]
	if !seen {
		cur = &logCursor{pos: fi.Size(), inode: ino}
		e.cursors[path] = cur
		e.mu.Unlock()
		return nil // first sight: start at the end, never replay history
	}
	pos := cur.pos
	if ino != cur.inode || fi.Size() < pos {
		pos = 0
	}
	e.mu.Unlock()

	if _, err := f.Seek(pos, 0); err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	read := pos
	for sc.Scan() {
		line := sc.Text()
		read += int64(len(line)) + 1
		out = append(out, line)
	}
	if sc.Err() != nil {
		read = pos // a partial pass must not skip the tail forever
	}

	e.mu.Lock()
	e.cursors[path] = &logCursor{pos: read, inode: ino}
	e.mu.Unlock()
	return out
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 1
}

// ── Persistence ──────────────────────────────────────────────

type persisted struct {
	Version int   `json:"version"`
	Bans    []Ban `json:"bans"`
}

const stateVersion = 1

// Save writes the ban list atomically.
func (e *Engine) Save() error {
	e.mu.RLock()
	st := persisted{Version: stateVersion}
	now := time.Now()
	for _, b := range e.bans {
		if b.Active(now) {
			st.Bans = append(st.Bans, b)
		}
	}
	e.mu.RUnlock()
	sort.Slice(st.Bans, func(i, j int) bool { return st.Bans[i].IP < st.Bans[j].IP })

	data, err := json.Marshal(&st)
	if err != nil {
		return err
	}
	// Serialise writers from here down. The snapshot above was taken
	// under the read lock; this only orders the file replacement, so two
	// saves can never interleave and an older one can never land last.
	e.saveMu.Lock()
	defer e.saveMu.Unlock()

	dir := filepath.Dir(e.statePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bans-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	if err := os.Rename(name, e.statePath); err != nil {
		return err
	}
	e.saves.Add(1)
	return nil
}

// saveCount reports how many saves have completed. Test-only.
func (e *Engine) saveCount() int64 { return e.saves.Load() }

// requestSave persists in the background, collapsing concurrent requests.
//
// Background because the caller may hold e.mu and Save() needs the read
// lock. Collapsing because two scans fifteen seconds apart should not be
// able to queue two writes if the first is still running on a slow disk —
// the second would write the same state a moment later anyway.
func (e *Engine) requestSave() {
	if !e.saving.CompareAndSwap(false, true) {
		// A save is already in flight. Mark that the state changed
		// again so it runs once more when the current one finishes,
		// rather than dropping this change entirely.
		e.saveAgain.Store(true)
		return
	}
	go func() {
		for {
			e.persist()
			if !e.saveAgain.CompareAndSwap(true, false) {
				break
			}
		}
		e.saving.Store(false)
	}()
}

// saveMu serialises writers to the state file.
//
// Without it a BACKGROUND save started before an explicit Save() can land
// AFTER it, rewinding the file to the older snapshot. Found by running the
// suite under -race: a test expired a ban, called Save() expecting an empty
// file, and got the pre-expiry state back because a scan's async save
// finished last.
//
// In production the same race would silently resurrect a ban that had just
// been lifted — rare, but exactly the kind of bug nobody would ever trace.
// The lock is held only across the write itself, never across the read of
// e.bans, so it cannot contend with request recording.

// waitForSave blocks until no background save is in flight.
//
// Used by Stop() so shutdown never races a write, and by tests that assert
// what actually reached disk. Bounded rather than unbounded: a save that
// somehow never completes must not hang shutdown for ever, and one second
// is far longer than the measured worst case (7 ms for 5,000 bans).
func (e *Engine) waitForSave() {
	for i := 0; i < 500 && e.saving.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
}

// persist writes the state and logs a failure rather than returning it.
//
// Used by every path that CHANGES the ban list. Until r47 only the
// five-minute tick and a graceful shutdown wrote anything, which meant a
// power cut or an OOM kill lost every ban applied since the last flush —
// and the window in which a freshly banned attacker is forgotten is exactly
// the window in which they are attacking. Measured cost of closing it:
//
//	  10 bans   one atomic save   ~0.15 ms
//	1000 bans                     ~1.5 ms
//	5000 bans                     ~7 ms
//
// A scan performs ONE save however many addresses it banned, so even a
// botnet sweep pays that once every fifteen seconds at worst.
func (e *Engine) persist() {
	if err := e.Save(); err != nil {
		log.Printf("[shahrag] bans: could not save: %v", err)
	}
}

// Load restores previously saved bans. A ban that does not survive a
// restart is not a ban: an upgrade would hand every scanner a clean slate.
func (e *Engine) Load() error {
	raw, err := os.ReadFile(e.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot read %s: %w", e.statePath, err)
	}
	var st persisted
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("%s is unreadable, starting with no bans: %w", e.statePath, err)
	}
	if st.Version != stateVersion {
		return fmt.Errorf("%s was written by a different version (%d)", e.statePath, st.Version)
	}
	now := time.Now()
	e.mu.Lock()
	for _, b := range st.Bans {
		if b.Active(now) {
			e.bans[b.IP] = b
		}
	}
	e.mu.Unlock()
	return nil
}
