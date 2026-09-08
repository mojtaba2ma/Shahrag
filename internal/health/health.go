// Package health answers one question — "is this server all right?" — with
// numbers rather than adjectives.
//
// Why this exists as its own package, and not as another handler inside
// internal/web:
//
//   - The panel page and the Telegram bot must show THE SAME answer. If the
//     bot re-derived "swap is at 100%" from its own reading of /proc, the two
//     would eventually disagree and the operator would not know which to
//     believe. There is one Report type and one Collect(), and every surface
//     renders it.
//   - Everything here reads /proc and /sys, which are free. The two things
//     that are NOT free — asking systemd whether nginx is up, and asking
//     nginx its version — fork a process, so they are cached. See cost.go.
//
// The single most important thing this package gets right is SWAP.
//
// "Swap used: 287 MB of 383 MB (75%)" is the reading that makes people panic,
// and it is almost always meaningless. Linux never volunteers to bring a page
// back from swap: once something was paged out during a busy minute weeks
// ago, it stays out until it is touched. A high SwapUsed is therefore a
// record of the WORST moment since boot, not a description of now.
//
// The number that describes NOW is the RATE — how many pages went out to
// swap and came back per second, right now. If that rate is zero, the machine
// is not swapping, whatever the gauge says. So this package reports both, and
// the verdict is driven by the rate, never by the gauge.
package health

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Level grades one check. Deliberately only three: an operator glancing at a
// phone needs "fine / look at this / act now", not a score out of 100.
type Level string

const (
	LevelOK   Level = "ok"
	LevelWarn Level = "warn"
	LevelBad  Level = "bad"
)

// worse returns the more serious of two levels, so a report's overall verdict
// is the worst of its parts rather than an average that hides one emergency
// behind nine healthy checks.
func worse(a, b Level) Level {
	rank := map[Level]int{LevelOK: 0, LevelWarn: 1, LevelBad: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Check is one line of the report.
type Check struct {
	// ID is stable and machine-readable; the UI translates it, the bot
	// prints it. Never a sentence — a sentence cannot be translated by the
	// client and cannot be compared between two reports.
	ID    string `json:"id"`
	Level Level  `json:"level"`
	// Value is the headline figure, already formatted ("3.8 MB", "12%").
	Value string `json:"value"`
	// Detail carries the supporting numbers, again pre-formatted.
	Detail string `json:"detail,omitempty"`
	// Hint names a translated explanation the UI can show. Empty when the
	// value speaks for itself.
	Hint string `json:"hint,omitempty"`
}

// CPU describes processor pressure.
type CPU struct {
	Cores int `json:"cores"`
	// UsedPct is instantaneous utilisation, sampled over the interval
	// between two Collect() calls. Zero on the very first call — there is
	// no honest way to produce a rate from a single sample, and inventing
	// one is worse than admitting it.
	UsedPct float64 `json:"used_pct"`
	// Load1/5/15 are the run-queue averages. Reported PER CORE as well,
	// because "load 2.0" means nothing until you know whether the machine
	// has one core (saturated) or eight (idle).
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	Load15       float64 `json:"load15"`
	LoadPerCore1 float64 `json:"load_per_core_1"`
	// IOWaitPct is time the CPU spent doing nothing while waiting for the
	// disk. High iowait with low usage means the disk is the bottleneck,
	// which no amount of CPU tuning will fix.
	IOWaitPct float64 `json:"iowait_pct"`
	// StealPct is time the hypervisor gave to somebody else. On a cheap
	// VPS this is the difference between "my server is slow" and "my
	// neighbour is loud", and it is the one figure the operator cannot do
	// anything about — but they deserve to know it is not their fault.
	StealPct float64 `json:"steal_pct"`
}

// Memory describes RAM. All byte counts, never percentages alone: a
// percentage of an unknown total is not information.
type Memory struct {
	TotalBytes     int64   `json:"total_bytes"`
	AvailableBytes int64   `json:"available_bytes"`
	UsedBytes      int64   `json:"used_bytes"`
	UsedPct        float64 `json:"used_pct"`
	// CacheBytes is page cache plus buffers — memory Linux is using
	// opportunistically and will hand back the instant anything needs it.
	// Shown separately because counting it as "used" is what makes people
	// believe a healthy server is out of memory.
	CacheBytes int64 `json:"cache_bytes"`
}

// Swap is the part this package exists for. See the package comment.
type Swap struct {
	TotalBytes int64   `json:"total_bytes"`
	UsedBytes  int64   `json:"used_bytes"`
	UsedPct    float64 `json:"used_pct"`

	// InPagesPerSec / OutPagesPerSec are the live rates, measured between
	// two Collect() calls. THESE are what "the server is swapping" means.
	InPagesPerSec  float64 `json:"in_pages_per_sec"`
	OutPagesPerSec float64 `json:"out_pages_per_sec"`

	// Active is the verdict: true only when pages are actually moving.
	// A 100%-full swap with Active=false is a healthy machine carrying an
	// old scar.
	Active bool `json:"active"`

	// Measured says whether a rate could be computed at all. False on the
	// first call of the process, when there is no previous sample to
	// subtract. The UI must say "measuring…" rather than "0 — all good",
	// because those are different claims.
	Measured bool `json:"measured"`
}

// Disk describes the root filesystem. Inodes are included because running
// out of them produces "no space left on device" on a disk that df says is
// half empty, and that failure has cost many people an evening.
type Disk struct {
	TotalBytes  int64   `json:"total_bytes"`
	FreeBytes   int64   `json:"free_bytes"`
	UsedPct     float64 `json:"used_pct"`
	InodesTotal int64   `json:"inodes_total"`
	InodesFree  int64   `json:"inodes_free"`
	InodesPct   float64 `json:"inodes_pct"`
}

// Panel is what the panel process itself costs. Published deliberately: a
// control panel that cannot say what it consumes has no business advising
// anyone about resources.
type Panel struct {
	// RssAnonBytes is the real private heap — the number that matters.
	// VmRSS includes the mapped binary and shared pages, which are not a
	// per-process cost and make the panel look three times bigger.
	RssAnonBytes int64  `json:"rss_anon_bytes"`
	RssBytes     int64  `json:"rss_bytes"`
	Goroutines   int    `json:"goroutines"`
	Threads      int    `json:"threads"`
	OpenFDs      int    `json:"open_fds"`
	UptimeSec    int64  `json:"uptime_sec"`
	Build        string `json:"build"`
	GoVersion    string `json:"go_version"`
}

// Nginx is the proxy's state. Filled from the cached probes in cost.go.
type Nginx struct {
	Active            bool   `json:"active"`
	EnabledAtBoot     bool   `json:"enabled_at_boot"`
	Version           string `json:"version"`
	WorkerConnections int    `json:"worker_connections"`
	Workers           int    `json:"workers"`
	// ConfigOK is the result of the last `nginx -t`. Cached hard: it is
	// the most expensive probe here, and the config only changes when the
	// panel changes it.
	ConfigOK    bool   `json:"config_ok"`
	ConfigError string `json:"config_error,omitempty"`
	// ConfigWarning is what `nginx -t` said while still SUCCEEDING.
	// Kept apart from ConfigError because conflating the two graded a
	// healthy server as degraded over a deprecation notice.
	ConfigWarning string `json:"config_warning,omitempty"`
	FailureNote   string `json:"failure_note,omitempty"`
}

// Security summarises the two protective features.
type Security struct {
	HoneypotOn    bool   `json:"honeypot_on"`
	HoneypotMode  string `json:"honeypot_mode,omitempty"`
	AutoBanOn     bool   `json:"autoban_on"`
	AutoBanAction string `json:"autoban_action,omitempty"`
	BansActive    int    `json:"bans_active"`
	BansPending   int    `json:"bans_pending"`
	// Engine says whether the watcher goroutine is actually running. The
	// settings can be on while the engine is not, and calling that
	// "protected" would be a lie.
	Engine bool `json:"engine_running"`
}

// Certs summarises certificate expiry.
type Certs struct {
	Total    int      `json:"total"`
	Expired  []string `json:"expired,omitempty"`
	DueSoon  []string `json:"due_soon,omitempty"`
	Problems []string `json:"problems,omitempty"`
	// SoonestDays is the days left on whichever certificate expires first.
	// A single number the bot can put in a one-line message.
	SoonestDays int    `json:"soonest_days"`
	SoonestName string `json:"soonest_name,omitempty"`
}

// Traffic is the last hour, so the report answers "is anything happening?"
// without the operator having to open the stats page.
type Traffic struct {
	RequestsHour  int64   `json:"requests_hour"`
	ErrorRatePct  float64 `json:"error_rate_pct"`
	UniqueIPsHour int     `json:"unique_ips_hour"`
	ConnActive    int     `json:"conn_active"`
}

// Report is the whole answer. One struct, rendered by the panel page and, in
// the next step, by the Telegram bot.
type Report struct {
	// TS is when this was produced, so a cached copy can say how old it is
	// instead of pretending to be live.
	TS        int64   `json:"ts"`
	Level     Level   `json:"level"`
	Checks    []Check `json:"checks"`
	Host      string  `json:"host"`
	UptimeSec int64   `json:"uptime_sec"`
	Kernel    string  `json:"kernel,omitempty"`

	CPU      CPU      `json:"cpu"`
	Memory   Memory   `json:"memory"`
	Swap     Swap     `json:"swap"`
	Disk     Disk     `json:"disk"`
	Panel    Panel    `json:"panel"`
	Nginx    Nginx    `json:"nginx"`
	Security Security `json:"security"`
	Certs    Certs    `json:"certs"`
	Traffic  Traffic  `json:"traffic"`
}

// ── Thresholds ───────────────────────────────────────────────
//
// Every one of these is a judgement call, so each carries the reasoning.
const (
	// A 1 GB VPS running several services sits at 70–80% RAM permanently
	// and is perfectly happy. Warning below 90% would cry wolf every day.
	memWarnPct = 90
	memBadPct  = 96

	// Disk is different: a full disk stops nginx from writing logs and
	// stops the panel from saving config, so the warning has to come early
	// enough to act on.
	diskWarnPct = 85
	diskBadPct  = 94

	// Inodes get their own, tighter numbers because exhausting them is
	// invisible in every normal tool.
	inodeWarnPct = 85
	inodeBadPct  = 95

	// Load is graded PER CORE. 1.0 per core means the run queue is exactly
	// as long as the machine is wide — busy but keeping up.
	loadWarnPerCore = 1.5
	loadBadPerCore  = 3.0

	// Swap rate. One page is 4 KB, so 50 pages/s is 200 KB/s of paging —
	// small but real, and the point at which latency starts to be felt.
	// 500 pages/s (2 MB/s) is thrashing.
	swapWarnPagesPerSec = 50
	swapBadPagesPerSec  = 500

	// iowait above a quarter of the CPU means the disk is the bottleneck.
	ioWaitWarnPct = 25
	// Steal above 10% means a noisy neighbour is taking a real bite.
	stealWarnPct = 10

	// Certificates. Let's Encrypt issues for 90 days and renewal starts at
	// 30, so 14 days left means renewal has already failed twice.
	certWarnDays = 14
	certBadDays  = 3

	// An error rate this high on a proxy means something is broken
	// downstream, not that a few clients typed a bad URL.
	errRateWarnPct = 15
	errRateBadPct  = 40
)

// pageSize is fixed per architecture; read once rather than per sample.
var pageSize = int64(os.Getpagesize())

// Deps is everything Collect needs from the rest of the program, passed in
// rather than imported. The health package must not depend on web, banner or
// stats — that would be a cycle, and it would also make this untestable
// without standing up half the panel.
type Deps struct {
	// Build and GoVersion identify the running binary.
	Build string

	// Nginx probes. Nil-safe: a nil func means "unknown", not "broken".
	NginxActive    func() bool
	NginxEnabled   func() bool
	NginxVersion   func() string
	NginxWorkerCon func() int
	NginxFailure   func() string
	NginxConfTest  func() (bool, string)

	// Security state.
	HoneypotOn func() (bool, string)
	AutoBanOn  func() (bool, string)
	BanCounts  func() (active, pending int, running bool)

	// Certificates, already inspected by internal/certs.
	CertExpiry func() Certs

	// Traffic, from internal/stats.
	TrafficNow func() Traffic
}

// Collector holds the state a rate needs: the previous sample.
//
// A rate cannot be derived from one reading, and the alternative — sleeping
// 200 ms inside the request to take two — would make every page load slower
// and every bot command hang. Instead the previous sample is kept and the
// rate is measured across whatever interval actually elapsed. The first call
// honestly reports "not measured yet".
type Collector struct {
	deps Deps

	// mu guards the previous-sample fields below.
	//
	// Every rate this package reports is a difference between two
	// samples, which means Collect() both READS and WRITES that state.
	// Two concurrent calls therefore race — and concurrent calls are not
	// hypothetical: the health page polls every five seconds and the
	// Telegram bot will ask for the same report on its own schedule.
	//
	// Found by running the suite under -race, not by reading the code:
	// the collector looked stateless because the state is four small
	// fields tucked at the bottom of the struct.
	//
	// It also makes the rate CORRECT under concurrency. Without the lock
	// two overlapping calls each subtract from whichever previous sample
	// they happened to see, so both report a rate measured over a random
	// fraction of the real interval.
	mu          sync.Mutex
	prevAt      time.Time
	prevCPU     cpuSample
	prevSwapIn  int64
	prevSwapOut int64
	havePrev    bool

	// Cached results of the expensive probes. Guarded by its own lock.
	probes probeCache
}

// NewCollector builds a collector. Cheap: nothing is read until Collect.
func NewCollector(d Deps) *Collector { return &Collector{deps: d} }

type cpuSample struct {
	total, idle, iowait, steal float64
}

// Collect produces a report.
//
// Cost, measured (see health_test.go, TestCollectStaysCheap): one Collect
// reads six small files from /proc, one statfs, and nothing else — no forks,
// no allocations proportional to traffic, no locks held across I/O. The
// expensive probes behind Deps are cached by probeCache and refreshed on
// their own schedule.
func (c *Collector) Collect() Report {
	now := time.Now()
	r := Report{TS: now.Unix(), Level: LevelOK}

	// Held across the whole sample so the CPU and swap rates are computed
	// against the SAME previous sample and the same elapsed interval.
	// Taking it per-field would remove the race but still let two callers
	// interleave and produce two half-measured rates.
	//
	// The expensive probes are NOT under this lock: they have their own,
	// and they are refreshed in the background precisely so that a slow
	// fork cannot block a report.
	c.mu.Lock()
	defer c.mu.Unlock()

	r.Host, _ = os.Hostname()
	r.Kernel = readKernel()
	r.UptimeSec = readUptime()

	c.fillCPU(&r, now)
	c.fillMemAndSwap(&r, now)
	fillDisk(&r)
	fillPanel(&r, c.deps.Build)
	c.fillNginx(&r)
	c.fillSecurity(&r)
	c.fillCerts(&r)
	c.fillTraffic(&r)

	c.prevAt = now
	c.havePrev = true

	r.Checks = grade(&r)
	for _, ch := range r.Checks {
		r.Level = worse(r.Level, ch.Level)
	}
	return r
}

// ── CPU ──────────────────────────────────────────────────────

func (c *Collector) fillCPU(r *Report, now time.Time) {
	r.CPU.Cores = runtime.NumCPU()
	r.CPU.Load1, r.CPU.Load5, r.CPU.Load15 = readLoadAvg()
	if r.CPU.Cores > 0 {
		r.CPU.LoadPerCore1 = r.CPU.Load1 / float64(r.CPU.Cores)
	}

	s, ok := readCPUSample()
	if !ok {
		return
	}
	if c.havePrev && c.prevCPU.total > 0 {
		dTot := s.total - c.prevCPU.total
		if dTot > 0 {
			dIdle := s.idle - c.prevCPU.idle
			r.CPU.UsedPct = clamp((dTot - dIdle) / dTot * 100)
			r.CPU.IOWaitPct = clamp((s.iowait - c.prevCPU.iowait) / dTot * 100)
			r.CPU.StealPct = clamp((s.steal - c.prevCPU.steal) / dTot * 100)
		}
	}
	c.prevCPU = s
}

// ── Memory and swap ──────────────────────────────────────────

func (c *Collector) fillMemAndSwap(r *Report, now time.Time) {
	m := readMeminfo()
	kb := func(v int64) int64 { return v * 1024 }

	r.Memory.TotalBytes = kb(m["MemTotal"])
	r.Memory.AvailableBytes = kb(m["MemAvailable"])
	r.Memory.CacheBytes = kb(m["Cached"] + m["Buffers"])
	if r.Memory.TotalBytes > 0 {
		// Used = total - AVAILABLE, not total - free. MemAvailable is the
		// kernel's own estimate of what a new allocation could get without
		// swapping, and it already accounts for reclaimable cache. Using
		// MemFree instead is exactly the mistake that makes every Linux
		// box look like it is out of memory.
		r.Memory.UsedBytes = r.Memory.TotalBytes - r.Memory.AvailableBytes
		r.Memory.UsedPct = clamp(float64(r.Memory.UsedBytes) / float64(r.Memory.TotalBytes) * 100)
	}

	r.Swap.TotalBytes = kb(m["SwapTotal"])
	if r.Swap.TotalBytes > 0 {
		r.Swap.UsedBytes = kb(m["SwapTotal"] - m["SwapFree"])
		r.Swap.UsedPct = clamp(float64(r.Swap.UsedBytes) / float64(r.Swap.TotalBytes) * 100)
	}

	in, out, ok := readSwapCounters()
	if !ok {
		return
	}
	if c.havePrev {
		elapsed := now.Sub(c.prevAt).Seconds()
		// Guard against a clock jump and against two calls in the same
		// instant, either of which would divide by ~0 and print a
		// spectacular fictional rate.
		if elapsed >= 0.5 {
			r.Swap.InPagesPerSec = perSec(in-c.prevSwapIn, elapsed)
			r.Swap.OutPagesPerSec = perSec(out-c.prevSwapOut, elapsed)
			r.Swap.Measured = true
			r.Swap.Active = r.Swap.InPagesPerSec+r.Swap.OutPagesPerSec >= 1
		}
	}
	c.prevSwapIn, c.prevSwapOut = in, out
}

func perSec(delta int64, seconds float64) float64 {
	// A negative delta means the counter was reset (a reboot between two
	// samples). Report zero rather than a negative rate.
	if delta < 0 || seconds <= 0 {
		return 0
	}
	return float64(delta) / seconds
}

// ── Disk ─────────────────────────────────────────────────────

func fillDisk(r *Report) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return
	}
	bs := int64(st.Bsize)
	r.Disk.TotalBytes = int64(st.Blocks) * bs
	// Bavail, not Bfree: the last few percent are reserved for root, and a
	// non-root process cannot use them. Reporting Bfree tells the operator
	// they have space they cannot actually write to.
	r.Disk.FreeBytes = int64(st.Bavail) * bs
	if st.Blocks > 0 {
		used := int64(st.Blocks - st.Bavail)
		r.Disk.UsedPct = clamp(float64(used) / float64(st.Blocks) * 100)
	}
	r.Disk.InodesTotal = int64(st.Files)
	r.Disk.InodesFree = int64(st.Ffree)
	if st.Files > 0 {
		r.Disk.InodesPct = clamp(float64(st.Files-st.Ffree) / float64(st.Files) * 100)
	}
}

// ── The panel itself ─────────────────────────────────────────

var processStart = time.Now()

func fillPanel(r *Report, build string) {
	r.Panel.Build = build
	r.Panel.GoVersion = runtime.Version()
	r.Panel.Goroutines = runtime.NumGoroutine()
	r.Panel.UptimeSec = int64(time.Since(processStart).Seconds())

	st := readProcStatus()
	r.Panel.RssAnonBytes = st["RssAnon"] * 1024
	r.Panel.RssBytes = st["VmRSS"] * 1024
	r.Panel.Threads = int(st["Threads"])
	r.Panel.OpenFDs = countFDs()
}

// countFDs counts entries in /proc/self/fd.
//
// A descriptor leak is the classic way a long-running daemon dies after
// three weeks, and it is invisible until the moment it is fatal. Counting
// costs one directory read.
func countFDs() int {
	f, err := os.Open("/proc/self/fd")
	if err != nil {
		return 0
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return 0
	}
	// Minus one: the descriptor opened to read the directory is itself in
	// the listing, so an honest count excludes it.
	if n := len(names) - 1; n > 0 {
		return n
	}
	return 0
}

// ── nginx, security, certs, traffic ──────────────────────────

func (c *Collector) fillNginx(r *Report) {
	p := c.probes.get(c.deps)
	r.Nginx.Active = p.active
	r.Nginx.EnabledAtBoot = p.enabled
	r.Nginx.Version = p.version
	r.Nginx.WorkerConnections = p.workerConn
	r.Nginx.ConfigOK = p.confOK
	if p.confOK {
		// nginx writes warnings to stderr on a SUCCESSFUL test too, so
		// the same field carries both. Split them by outcome.
		r.Nginx.ConfigWarning = extractWarning(p.confErr)
	} else {
		r.Nginx.ConfigError = p.confErr
	}
	r.Nginx.FailureNote = p.failure
	r.Nginx.Workers = countNginxWorkers()
}

func (c *Collector) fillSecurity(r *Report) {
	if c.deps.HoneypotOn != nil {
		r.Security.HoneypotOn, r.Security.HoneypotMode = c.deps.HoneypotOn()
	}
	if c.deps.AutoBanOn != nil {
		r.Security.AutoBanOn, r.Security.AutoBanAction = c.deps.AutoBanOn()
	}
	if c.deps.BanCounts != nil {
		r.Security.BansActive, r.Security.BansPending, r.Security.Engine = c.deps.BanCounts()
	}
}

func (c *Collector) fillCerts(r *Report) {
	if c.deps.CertExpiry != nil {
		r.Certs = c.deps.CertExpiry()
	}
}

func (c *Collector) fillTraffic(r *Report) {
	if c.deps.TrafficNow != nil {
		r.Traffic = c.deps.TrafficNow()
	}
}

// countNginxWorkers counts nginx worker processes by walking /proc.
//
// Deliberately not `pgrep`: that forks, and this runs on every report. The
// walk reads one small file per PID and takes well under a millisecond on a
// machine with a few hundred processes.
func countNginxWorkers() int {
	d, err := os.Open("/proc")
	if err != nil {
		return 0
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return 0
	}
	n := 0
	for _, name := range names {
		if name[0] < '0' || name[0] > '9' {
			continue
		}
		// /proc/<pid>/comm is one short line and is far cheaper to read
		// than cmdline, which can be kilobytes.
		b, err := os.ReadFile("/proc/" + name + "/comm")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == "nginx" {
			n++
		}
	}
	// The master process is one of them; report workers only when there is
	// clearly a master plus children.
	if n > 1 {
		return n - 1
	}
	return n
}

// ── Grading ──────────────────────────────────────────────────

// grade turns the raw numbers into the list of checks.
//
// The order is the order they matter in, not alphabetical: whatever is wrong
// should be near the top of the page and near the top of a bot message.
func grade(r *Report) []Check {
	out := make([]Check, 0, 12)

	// nginx first. If the proxy is down, nothing else on this page matters.
	switch {
	case !r.Nginx.Active:
		out = append(out, Check{ID: "nginx", Level: LevelBad,
			Value: "down", Detail: r.Nginx.FailureNote, Hint: "nginx_down"})
	case !r.Nginx.ConfigOK:
		out = append(out, Check{ID: "nginx", Level: LevelWarn,
			Value: "config", Detail: firstLine(r.Nginx.ConfigError), Hint: "nginx_conf"})
	case r.Nginx.ConfigWarning != "":
		// `nginx -t` succeeded but said something. A warning is not a
		// failure and must not be graded as one — the CLI showed WARN
		// for "the user directive makes sense only if..." on a config
		// nginx was perfectly happy with. It is still worth surfacing,
		// just not as a problem with the server.
		out = append(out, Check{ID: "nginx", Level: LevelOK,
			Value: "up", Detail: firstLine(r.Nginx.ConfigWarning)})
	case !r.Nginx.EnabledAtBoot:
		out = append(out, Check{ID: "nginx", Level: LevelWarn,
			Value: "no-boot", Hint: "nginx_boot"})
	default:
		out = append(out, Check{ID: "nginx", Level: LevelOK, Value: "up",
			Detail: fmt.Sprintf("%d workers", r.Nginx.Workers)})
	}

	// Swap: the rate decides, the gauge is context. This is the whole
	// reason the page exists, so it is graded before RAM.
	out = append(out, gradeSwap(r.Swap))

	// Memory.
	memLevel := LevelOK
	switch {
	case r.Memory.UsedPct >= memBadPct:
		memLevel = LevelBad
	case r.Memory.UsedPct >= memWarnPct:
		memLevel = LevelWarn
	}
	out = append(out, Check{ID: "memory", Level: memLevel,
		Value:  fmt.Sprintf("%.0f%%", r.Memory.UsedPct),
		Detail: fmt.Sprintf("%s / %s", humanBytes(r.Memory.UsedBytes), humanBytes(r.Memory.TotalBytes)),
		Hint:   "memory_available"})

	// CPU, graded on load per core rather than the instantaneous percent:
	// a 100% spike lasting one sample is normal, a run queue three deep is
	// not.
	cpuLevel := LevelOK
	switch {
	case r.CPU.LoadPerCore1 >= loadBadPerCore:
		cpuLevel = LevelBad
	case r.CPU.LoadPerCore1 >= loadWarnPerCore:
		cpuLevel = LevelWarn
	}
	out = append(out, Check{ID: "cpu", Level: cpuLevel,
		Value: fmt.Sprintf("%.0f%%", r.CPU.UsedPct),
		Detail: fmt.Sprintf("load %.2f / %.2f / %.2f · %d cores",
			r.CPU.Load1, r.CPU.Load5, r.CPU.Load15, r.CPU.Cores),
		Hint: "cpu_load"})

	if r.CPU.IOWaitPct >= ioWaitWarnPct {
		out = append(out, Check{ID: "iowait", Level: LevelWarn,
			Value: fmt.Sprintf("%.0f%%", r.CPU.IOWaitPct), Hint: "iowait"})
	}
	if r.CPU.StealPct >= stealWarnPct {
		out = append(out, Check{ID: "steal", Level: LevelWarn,
			Value: fmt.Sprintf("%.0f%%", r.CPU.StealPct), Hint: "steal"})
	}

	// Disk.
	diskLevel := LevelOK
	switch {
	case r.Disk.UsedPct >= diskBadPct:
		diskLevel = LevelBad
	case r.Disk.UsedPct >= diskWarnPct:
		diskLevel = LevelWarn
	}
	out = append(out, Check{ID: "disk", Level: diskLevel,
		Value:  fmt.Sprintf("%.0f%%", r.Disk.UsedPct),
		Detail: fmt.Sprintf("%s free", humanBytes(r.Disk.FreeBytes))})

	if r.Disk.InodesTotal > 0 {
		inLevel := LevelOK
		switch {
		case r.Disk.InodesPct >= inodeBadPct:
			inLevel = LevelBad
		case r.Disk.InodesPct >= inodeWarnPct:
			inLevel = LevelWarn
		}
		if inLevel != LevelOK {
			out = append(out, Check{ID: "inodes", Level: inLevel,
				Value: fmt.Sprintf("%.0f%%", r.Disk.InodesPct), Hint: "inodes"})
		}
	}

	// Certificates.
	out = append(out, gradeCerts(r.Certs))

	// Protection.
	out = append(out, gradeSecurity(r.Security))

	// Error rate, only when there is enough traffic for a percentage to
	// mean anything. Two requests, one of them a 404, is not a 50% error
	// rate — it is nothing at all.
	if r.Traffic.RequestsHour >= 50 {
		lvl := LevelOK
		switch {
		case r.Traffic.ErrorRatePct >= errRateBadPct:
			lvl = LevelBad
		case r.Traffic.ErrorRatePct >= errRateWarnPct:
			lvl = LevelWarn
		}
		if lvl != LevelOK {
			out = append(out, Check{ID: "errors", Level: lvl,
				Value:  fmt.Sprintf("%.0f%%", r.Traffic.ErrorRatePct),
				Detail: fmt.Sprintf("%d req/h", r.Traffic.RequestsHour),
				Hint:   "error_rate"})
		}
	}

	// The panel's own footprint. Never a warning: it is information, and
	// grading our own process would be marking our own homework.
	out = append(out, Check{ID: "panel", Level: LevelOK,
		Value: humanBytes(r.Panel.RssAnonBytes),
		Detail: fmt.Sprintf("%d goroutines · %d fds · %s",
			r.Panel.Goroutines, r.Panel.OpenFDs, r.Panel.Build)})

	return out
}

func gradeSwap(s Swap) Check {
	if s.TotalBytes == 0 {
		return Check{ID: "swap", Level: LevelOK, Value: "off", Hint: "swap_none"}
	}
	gauge := fmt.Sprintf("%s / %s (%.0f%%)",
		humanBytes(s.UsedBytes), humanBytes(s.TotalBytes), s.UsedPct)

	if !s.Measured {
		return Check{ID: "swap", Level: LevelOK, Value: "…",
			Detail: gauge, Hint: "swap_measuring"}
	}
	rate := s.InPagesPerSec + s.OutPagesPerSec
	detail := fmt.Sprintf("%s · in %.0f p/s · out %.0f p/s",
		gauge, s.InPagesPerSec, s.OutPagesPerSec)

	switch {
	case rate >= swapBadPagesPerSec:
		return Check{ID: "swap", Level: LevelBad,
			Value: fmt.Sprintf("%.0f p/s", rate), Detail: detail, Hint: "swap_thrashing"}
	case rate >= swapWarnPagesPerSec:
		return Check{ID: "swap", Level: LevelWarn,
			Value: fmt.Sprintf("%.0f p/s", rate), Detail: detail, Hint: "swap_active"}
	}
	// The important case: the gauge can read 100% and this is still OK.
	// The hint spells out why, because that is the question being asked.
	return Check{ID: "swap", Level: LevelOK, Value: "idle",
		Detail: detail, Hint: "swap_idle"}
}

func gradeCerts(c Certs) Check {
	switch {
	case c.Total == 0:
		return Check{ID: "certs", Level: LevelOK, Value: "—", Hint: "certs_none"}
	case len(c.Expired) > 0:
		return Check{ID: "certs", Level: LevelBad,
			Value:  fmt.Sprintf("%d expired", len(c.Expired)),
			Detail: strings.Join(c.Expired, ", "), Hint: "certs_expired"}
	case len(c.Problems) > 0:
		return Check{ID: "certs", Level: LevelWarn,
			Value:  fmt.Sprintf("%d problem", len(c.Problems)),
			Detail: strings.Join(c.Problems, ", "), Hint: "certs_problem"}
	case c.SoonestDays <= certBadDays:
		return Check{ID: "certs", Level: LevelBad,
			Value: fmt.Sprintf("%dd", c.SoonestDays), Detail: c.SoonestName, Hint: "certs_soon"}
	case c.SoonestDays <= certWarnDays:
		return Check{ID: "certs", Level: LevelWarn,
			Value: fmt.Sprintf("%dd", c.SoonestDays), Detail: c.SoonestName, Hint: "certs_soon"}
	}
	return Check{ID: "certs", Level: LevelOK,
		Value:  fmt.Sprintf("%dd", c.SoonestDays),
		Detail: fmt.Sprintf("%d total", c.Total)}
}

func gradeSecurity(s Security) Check {
	// Settings on but the engine not running is the dangerous state: the
	// panel looks protected and nothing is watching. Say so loudly.
	if s.AutoBanOn && !s.Engine {
		return Check{ID: "protection", Level: LevelWarn,
			Value: "engine off", Hint: "protection_engine"}
	}
	parts := make([]string, 0, 3)
	if s.HoneypotOn {
		parts = append(parts, "honeypot:"+s.HoneypotMode)
	}
	if s.AutoBanOn {
		parts = append(parts, "autoban:"+s.AutoBanAction)
	}
	if len(parts) == 0 {
		return Check{ID: "protection", Level: LevelOK, Value: "off", Hint: "protection_off"}
	}
	return Check{ID: "protection", Level: LevelOK,
		Value:  fmt.Sprintf("%d banned", s.BansActive),
		Detail: strings.Join(parts, " · ")}
}

// ── /proc readers ────────────────────────────────────────────

func readLoadAvg() (float64, float64, float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0
	}
	a, _ := strconv.ParseFloat(f[0], 64)
	c, _ := strconv.ParseFloat(f[1], 64)
	d, _ := strconv.ParseFloat(f[2], 64)
	return a, c, d
}

func readCPUSample() (cpuSample, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, false
	}
	line := string(b)
	if i := strings.IndexByte(line, '\n'); i > 0 {
		line = line[:i]
	}
	f := strings.Fields(line)
	if len(f) < 9 || f[0] != "cpu" {
		return cpuSample{}, false
	}
	// Fields after "cpu": user nice system idle iowait irq softirq steal ...
	var v [8]float64
	for i := 0; i < 8 && i+1 < len(f); i++ {
		v[i], _ = strconv.ParseFloat(f[i+1], 64)
	}
	var total float64
	for _, x := range v {
		total += x
	}
	// idle for the purpose of "used %" includes iowait: the CPU really was
	// doing nothing. iowait is then reported separately so the operator can
	// tell "idle because nothing to do" from "idle because the disk is
	// slow" — two very different problems with the same percentage.
	return cpuSample{total: total, idle: v[3] + v[4], iowait: v[4], steal: v[7]}, true
}

// readMeminfo returns the fields we need, in kB, in one pass.
//
// Only the handful of keys that are used are kept: /proc/meminfo has ~50
// lines and building a map of all of them on every sample would allocate for
// nothing.
func readMeminfo() map[string]int64 {
	want := map[string]bool{
		"MemTotal": true, "MemFree": true, "MemAvailable": true,
		"Buffers": true, "Cached": true, "SwapTotal": true, "SwapFree": true,
	}
	out := make(map[string]int64, len(want))
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := line[:i]
		if !want[key] {
			continue
		}
		v, _ := strconv.ParseInt(strings.Fields(line[i+1:])[0], 10, 64)
		out[key] = v
		if len(out) == len(want) {
			break
		}
	}
	return out
}

// readSwapCounters returns the cumulative pages swapped in and out.
//
// pswpin/pswpout in /proc/vmstat are monotonic counters since boot. The
// difference between two readings, divided by the elapsed time, is the rate —
// which is exactly what `vmstat`'s si/so columns show, and exactly what tells
// a stale swap gauge apart from a machine that is actually thrashing.
func readSwapCounters() (in, out int64, ok bool) {
	b, err := os.ReadFile("/proc/vmstat")
	if err != nil {
		return 0, 0, false
	}
	found := 0
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "pswpin "):
			in, _ = strconv.ParseInt(strings.TrimSpace(line[7:]), 10, 64)
			found++
		case strings.HasPrefix(line, "pswpout "):
			out, _ = strconv.ParseInt(strings.TrimSpace(line[8:]), 10, 64)
			found++
		}
		if found == 2 {
			break
		}
	}
	return in, out, found == 2
}

func readProcStatus() map[string]int64 {
	want := map[string]bool{"VmRSS": true, "RssAnon": true, "Threads": true}
	out := make(map[string]int64, len(want))
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := line[:i]
		if !want[key] {
			continue
		}
		f := strings.Fields(line[i+1:])
		if len(f) == 0 {
			continue
		}
		v, _ := strconv.ParseInt(f[0], 10, 64)
		out[key] = v
	}
	return out
}

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}

func readKernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ── Formatting ───────────────────────────────────────────────

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// humanBytes formats a byte count the way an operator reads it.
//
// Base 1024 with the short suffixes, because that is what free, df and every
// other tool on the box prints; using base 1000 here would make the panel
// disagree with the terminal by 7% and start an argument.
func humanBytes(b int64) string {
	if b < 0 {
		b = 0
	}
	const unit = 1024
	if b < unit {
		return strconv.FormatInt(b, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	suffix := [...]string{"KB", "MB", "GB", "TB"}[exp]
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, suffix)
	}
	return fmt.Sprintf("%.1f %s", v, suffix)
}

// extractWarning pulls a genuine warning out of a successful `nginx -t`.
//
// A successful run always prints two "syntax is ok" / "test is successful"
// lines; anything else it said is worth showing. Returning "" when there is
// nothing but the success lines keeps the check silent in the normal case.
func extractWarning(out string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" ||
			strings.Contains(line, "syntax is ok") ||
			strings.Contains(line, "test is successful") {
			continue
		}
		keep = append(keep, line)
	}
	if len(keep) == 0 {
		return ""
	}
	return strings.Join(keep, "; ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// SortChecks orders checks worst-first. Used by the bot, where there is no
// room to print everything and the bad news has to come first.
func SortChecks(in []Check) []Check {
	out := append([]Check(nil), in...)
	rank := map[Level]int{LevelBad: 0, LevelWarn: 1, LevelOK: 2}
	sort.SliceStable(out, func(i, j int) bool {
		return rank[out[i].Level] < rank[out[j].Level]
	})
	return out
}

// SleepForRate waits long enough between two Collect() calls for a rate to
// be measurable.
//
// Exported so the CLI can take two samples without duplicating the constant.
// The floor in fillMemAndSwap is 0.5 s; 1.2 s gives comfortable margin
// without making `shahrag health` feel slow.
func SleepForRate() { time.Sleep(1200 * time.Millisecond) }

// PersistedBanCount reads how many bans are currently on disk.
//
// The CLI has no running ban engine — that lives in the serve process — so
// counting the persisted list is the honest answer to "how many addresses is
// nginx blocking right now?". Returns 0 on any error: a missing or damaged
// file means the operator sees zero rather than a stack trace, and the
// number is informational.
func PersistedBanCount() int {
	raw, err := os.ReadFile(bansStatePath())
	if err != nil {
		return 0
	}
	var st struct {
		Bans []struct {
			ExpiresAt time.Time `json:"expires_at"`
			Permanent bool      `json:"permanent"`
		} `json:"bans"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return 0
	}
	now := time.Now()
	n := 0
	for _, b := range st.Bans {
		// Count only what is still in force. A file full of yesterday's
		// expired bans would otherwise report a frightening number for
		// a server under no attack at all.
		if b.Permanent || b.ExpiresAt.After(now) {
			n++
		}
	}
	return n
}

// bansStatePath mirrors banner.StatePath without importing that package,
// which would be a cycle: the ban engine already depends on config, and the
// web server wires health and banner together.
func bansStatePath() string {
	if v := os.Getenv("SHAHRAG_BANS_FILE"); v != "" {
		return v
	}
	return "/var/lib/shahrag/bans.json"
}
