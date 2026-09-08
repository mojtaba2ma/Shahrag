package health

// Keeping the health page cheap enough to poll.
//
// Everything in health.go reads /proc, which is a few microseconds. Four
// things are NOT cheap, and all four of them fork a child process:
//
//	systemctl is-active nginx     ~8 ms
//	systemctl is-enabled nginx    ~8 ms
//	nginx -v                      ~5 ms
//	nginx -t                     ~25 ms   (parses every config file)
//
// Measured on the 2-core test box; see health_cost_test.go. Together that is
// roughly 46 ms of fork/exec per report. A page that refreshes every 5
// seconds would spend about 1% of one core doing nothing but asking
// questions whose answers almost never change — and the Telegram bot, once
// it exists, would add its own polling on top.
//
// So they are cached, each with a TTL chosen from how fast its answer can
// actually change:
//
//   - nginx running / enabled: 10 s. It can genuinely stop at any moment, so
//     this is short enough that the page is not misleading.
//   - version: forever. It changes when the operator upgrades the package,
//     at which point the panel restarts anyway.
//   - nginx -t: 60 s, AND invalidated explicitly whenever the panel itself
//     regenerates the config. Nothing else can change those files behind our
//     back except a human editing them by hand, and 60 s is a fair wait for
//     that.
//
// The cache is refreshed synchronously on the first miss and then, if it is
// merely stale rather than empty, refreshed in the BACKGROUND while the
// caller is served the previous answer. A health page must never block on a
// fork: a hung `systemctl` on a loaded box would otherwise hang the request.

import (
	"sync"
	"time"
)

// TTLs. Package-level vars rather than consts so a test can shorten them
// without sleeping for a minute.
var (
	stateTTL    = 10 * time.Second
	confTestTTL = 60 * time.Second
)

type probeResult struct {
	active     bool
	enabled    bool
	version    string
	workerConn int
	failure    string
	confOK     bool
	confErr    string
}

type probeCache struct {
	mu sync.Mutex

	val       probeResult
	stateAt   time.Time
	confAt    time.Time
	haveState bool
	haveConf  bool

	// refreshing guards against a stampede: while a background refresh is
	// in flight, further callers take the stale value rather than each
	// starting their own fork.
	refreshing bool
}

// get returns the probe results, refreshing what has expired.
func (p *probeCache) get(d Deps) probeResult {
	now := time.Now()

	p.mu.Lock()
	stateStale := !p.haveState || now.Sub(p.stateAt) > stateTTL
	confStale := !p.haveConf || now.Sub(p.confAt) > confTestTTL
	haveAnything := p.haveState
	val := p.val
	busy := p.refreshing
	if (stateStale || confStale) && !busy {
		p.refreshing = true
	}
	p.mu.Unlock()

	if !stateStale && !confStale {
		return val
	}
	if busy {
		// Somebody else is already doing the work.
		return val
	}

	// The very first call has nothing to serve, so it must wait. Every
	// later call gets the previous answer immediately and the refresh
	// happens behind it.
	if !haveAnything {
		p.refresh(d, stateStale, confStale)
		p.mu.Lock()
		val = p.val
		p.mu.Unlock()
		return val
	}
	go p.refresh(d, stateStale, confStale)
	return val
}

func (p *probeCache) refresh(d Deps, state, conf bool) {
	defer func() {
		p.mu.Lock()
		p.refreshing = false
		p.mu.Unlock()
	}()

	p.mu.Lock()
	next := p.val
	p.mu.Unlock()

	if state {
		if d.NginxActive != nil {
			next.active = d.NginxActive()
		}
		if d.NginxEnabled != nil {
			next.enabled = d.NginxEnabled()
		}
		if d.NginxWorkerCon != nil {
			next.workerConn = d.NginxWorkerCon()
		}
		if d.NginxFailure != nil {
			next.failure = d.NginxFailure()
		}
		// The version is read once and then kept: it cannot change while
		// this process runs without the package being replaced, which
		// restarts the panel.
		if next.version == "" && d.NginxVersion != nil {
			next.version = d.NginxVersion()
		}
	}
	if conf && d.NginxConfTest != nil {
		next.confOK, next.confErr = d.NginxConfTest()
	} else if conf {
		// No probe wired (tests, CLI): claim nothing rather than
		// reporting a failure that was never checked.
		next.confOK = true
	}

	now := time.Now()
	p.mu.Lock()
	p.val = next
	if state {
		p.stateAt = now
		p.haveState = true
	}
	if conf {
		p.confAt = now
		p.haveConf = true
	}
	p.mu.Unlock()
}

// InvalidateConfig drops the cached `nginx -t` result.
//
// Called right after the panel regenerates and reloads: at that instant the
// cached answer describes the PREVIOUS config, and showing "config OK" for a
// file that has just been replaced is exactly the moment the operator most
// needs the truth.
func (c *Collector) InvalidateConfig() {
	c.probes.mu.Lock()
	c.probes.haveConf = false
	c.probes.mu.Unlock()
}
