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

// offence is one recorded event awaiting a verdict.
type offence struct {
	when time.Time
	kind string
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
	events  map[string][]offence
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

// ScanInterval is how often the logs are read. 15 seconds is a compromise:
// fast enough that a scanner is stopped early in its run, slow enough that
// the work is negligible.
const ScanInterval = 15 * time.Second

// New builds an engine. It does not start watching until Start is called.
func New(cfg *config.Manager, honeypotLog, accessLog string, onChange func()) *Engine {
	e := &Engine{
		cfg:         cfg,
		bans:        map[string]Ban{},
		events:      map[string][]offence{},
		cursors:     map[string]*logCursor{},
		statePath:   StatePath,
		stop:        make(chan struct{}),
		onChange:    onChange,
		honeypotLog: honeypotLog,
		accessLog:   accessLog,
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
func (e *Engine) Stop() {
	close(e.stop)
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
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		e.mu.Lock()
		e.cursors[p] = &logCursor{pos: fi.Size(), inode: inodeOf(fi)}
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
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events[ip] = append(e.events[ip], offence{when: now, kind: kind})
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

	e.mu.Lock()
	defer e.mu.Unlock()

	changed := false
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
		for _, r := range rules {
			if !r.rule.Enabled {
				continue
			}
			cutoff := now.Add(-r.rule.Window())
			n := 0
			for _, ev := range e.events[ip] {
				if ev.kind == r.kind && ev.when.After(cutoff) {
					n++
				}
			}
			if n >= r.rule.Threshold() {
				e.bans[ip] = Ban{
					IP: ip, Reason: r.kind, Hits: n,
					BannedAt:  now,
					ExpiresAt: now.Add(r.rule.Duration()),
					Permanent: r.rule.Permanent(),
				}
				log.Printf("[shahrag] banned %s: %d %s events in %s",
					ip, n, r.kind, r.rule.Window())
				changed = true
				break
			}
		}
	}

	if e.pruneLocked(now, ab) {
		changed = true
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
	// Events older than the longest window can never matter again.
	longest := 24 * time.Hour
	for _, r := range []config.AutoBanRule{ab.Honeypot, ab.AuthFail, ab.NotFound, ab.ErrorRate} {
		if w := r.Window(); w > longest {
			longest = w
		}
	}
	cutoff := now.Add(-longest)
	for ip, evs := range e.events {
		keep := evs[:0]
		for _, ev := range evs {
			if ev.when.After(cutoff) {
				keep = append(keep, ev)
			}
		}
		if len(keep) == 0 {
			delete(e.events, ip)
			continue
		}
		e.events[ip] = keep
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
	if ok && e.onChange != nil {
		go e.onChange()
	}
	return ok
}

// UnbanAll clears everything.
func (e *Engine) UnbanAll() int {
	e.mu.Lock()
	n := len(e.bans)
	e.bans = map[string]Ban{}
	e.events = map[string][]offence{}
	e.mu.Unlock()
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
	out := map[string]int{}
	for ip, evs := range e.events {
		if _, banned := e.bans[ip]; banned {
			continue
		}
		out[ip] = len(evs)
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
	return os.Rename(name, e.statePath)
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
