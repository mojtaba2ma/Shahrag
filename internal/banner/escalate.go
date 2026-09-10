package banner

// The offender ledger behind progressive banning.
//
// To make a ban longer the second time, the engine has to remember that
// there WAS a first time — and remember it after the first ban has already
// expired and been deleted. That is a different lifetime from a ban and a
// different lifetime from an offence counter, so it is a third structure.
//
// What it costs, because that is the part worth defending:
//
//	offender  = level int32 + count int32 + last int64  = 16 bytes
//	map entry = 16 bytes of payload + ~50 bytes of map overhead per key
//	            plus the address string (~16 bytes of header + 15 bytes)
//
// Measured at a full ledger of 19,000 records (the cap after one eviction
// pass): 1.32 MB of heap. That is the ceiling, and it is chosen against the
// 1 GB VPS this runs on. Twenty thousand DISTINCT addresses
// that have each been banned at least once is already an enormous campaign;
// beyond it the oldest, lowest-level records are dropped first, which is
// exactly the right thing to lose — a level-1 record from three weeks ago
// is one clean decay period from being worthless anyway.
//
// The ledger is persisted with the bans, because its whole purpose is to
// outlive them. A ledger lost on restart would hand every repeat offender
// a first-offence ban again, and an upgrade would reset every ladder in
// the system.

import (
	"sort"
	"time"

	"shahrag/internal/config"
)

// offender is one address's escalation history.
type offender struct {
	// Level is how many bans it has served, after decay. The NEXT ban is
	// level+1.
	Level int32 `json:"level"`
	// Count is how many bans it has ever received, never decayed. Purely
	// informational — it is what lets the panel say "banned 9 times"
	// about an address currently sitting at level 2 because it behaved
	// for a fortnight.
	Count int32 `json:"count"`
	// Last is when it last EARNED a ban, as a unix second. Decay is
	// measured from here, not from when the ban expired: an address that
	// serves a 30-day ban has spent those 30 days unable to offend, and
	// counting that silence as good behaviour would let a long ban buy
	// back the very steps it proves are deserved.
	Last int64 `json:"last"`
}

// maxOffenders bounds the ledger. See the package comment for the measured
// footprint this corresponds to.
const maxOffenders = 20000

// decayedLevel returns an offender's level after forgiveness is applied.
//
// One step is returned for every full decay period of silence. Not a reset:
// a reset lets a patient attacker buy a clean slate by waiting once,
// whereas step decay makes them wait once per step they climbed — so
// climbing to level 6 costs 18 days of genuine silence to undo, and coming
// back on day 17 keeps every rung.
func decayedLevel(o *offender, now time.Time, period time.Duration) int {
	if o == nil {
		return 0
	}
	lvl := int(o.Level)
	if lvl <= 0 || o.Last == 0 || period <= 0 {
		return max0(lvl)
	}
	elapsed := now.Sub(time.Unix(o.Last, 0))
	if elapsed <= 0 {
		return lvl
	}
	steps := int(elapsed / period)
	lvl -= steps
	return max0(lvl)
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// nextBan decides how long an address's next ban should last, and records
// that it happened. The caller holds e.mu.
//
// Returns the duration, whether it is permanent, and the 1-based level the
// ban was issued at — the level goes into the Ban and into the history
// file, because "banned for 8 hours" is not explicable on its own and
// "third offence, so 8 hours" is.
func (e *Engine) nextBanLocked(ip string, ab config.AutoBan, rule config.AutoBanRule, now time.Time) (d time.Duration, permanent bool, level int) {
	esc := ab.EffectiveEscalation()
	if !esc.Enabled {
		// Progressive banning off: the rule's own fixed length, exactly
		// as before. Level 0 marks "no ladder involved" so the UI does
		// not claim a first offence it did not check for.
		return rule.Duration(), rule.Permanent(), 0
	}

	o := e.offenders[ip]
	if o == nil {
		if len(e.offenders) >= maxOffenders {
			e.evictOffendersLocked(now, esc.DecayPeriod())
		}
		o = &offender{}
		e.offenders[ip] = o
	}
	level = decayedLevel(o, now, esc.DecayPeriod()) + 1
	if maxLvl := esc.StepCount(); level > maxLvl {
		level = maxLvl
	}
	o.Level = int32(level)
	o.Count++
	o.Last = now.Unix()

	d, permanent = esc.DurationForLevel(level)

	// With the ladder on, the ladder decides the length — full stop. The
	// rule's own duration is not consulted.
	//
	// An earlier version took the MAXIMUM of the two, on the theory that
	// a strict rule should not be softened. It was wrong in the other
	// direction: an operator who had deliberately set a one-minute rule
	// silently got thirty minutes, with nothing on screen to explain it.
	// Two settings that both claim to control the same number is exactly
	// the shape of bug that produced the empty ban history in r49, and
	// the fix is the same one: pick a single source of truth and make
	// the UI say which it is. The per-rule duration is greyed out in the
	// panel while the ladder is on, for the same reason.
	//
	// The one exception is a rule set to PERMANENT. That is not a length
	// the ladder can express as "shorter" — it is the operator saying
	// "anything that trips this is never coming back", which is an
	// instruction, not a duration.
	if rule.Permanent() {
		return 100 * 365 * 24 * time.Hour, true, level
	}
	return d, permanent, level
}

// evictOffendersLocked makes room in a full ledger.
//
// Lowest level first, then oldest first. That order is deliberate: a
// level-1 record is worth the least (one clean period from expiring
// anyway), and the high-level records are the ones whose loss would
// actually matter — those are the addresses that have proved, repeatedly,
// that they come back.
//
// A tenth of the ledger goes at once rather than a single entry, so a
// sustained flood of new addresses does not pay for a full sort on every
// insertion.
func (e *Engine) evictOffendersLocked(now time.Time, period time.Duration) {
	type kv struct {
		ip  string
		lvl int
		age int64
	}
	all := make([]kv, 0, len(e.offenders))
	for ip, o := range e.offenders {
		lvl := decayedLevel(o, now, period)
		if lvl <= 0 {
			// Fully forgiven: it has no effect on any future decision,
			// so it is pure overhead. Drop it outright.
			delete(e.offenders, ip)
			continue
		}
		all = append(all, kv{ip, lvl, o.Last})
	}
	target := maxOffenders - maxOffenders/10
	if len(e.offenders) <= target {
		return
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].lvl != all[j].lvl {
			return all[i].lvl < all[j].lvl
		}
		return all[i].age < all[j].age
	})
	drop := len(e.offenders) - target
	for i := 0; i < drop && i < len(all); i++ {
		delete(e.offenders, all[i].ip)
	}
}

// pruneOffendersLocked drops records that have decayed to nothing.
//
// Run from the same pass that prunes bans and events, so the ledger cannot
// grow without bound on a server that is scanned once by a great many
// addresses and then left alone.
func (e *Engine) pruneOffendersLocked(now time.Time, ab config.AutoBan) {
	period := ab.EffectiveEscalation().DecayPeriod()
	for ip, o := range e.offenders {
		if decayedLevel(o, now, period) <= 0 {
			delete(e.offenders, ip)
		}
	}
}

// OffenderInfo is one row of the ledger, for the panel.
type OffenderInfo struct {
	IP string `json:"ip"`
	// Level is the CURRENT level after decay — what the address would be
	// banned at if it offended again, minus one.
	Level int `json:"level"`
	// NextLevel is the level its next ban would be issued at.
	NextLevel int `json:"next_level"`
	// NextMinutes is how long that ban would last. -1 for permanent.
	NextMinutes int `json:"next_minutes"`
	// TotalBans is the undecayed lifetime count.
	TotalBans int `json:"total_bans"`
	// LastBan is when it last earned a ban.
	LastBan time.Time `json:"last_ban"`
	// DecaysAt is when it will drop one step, if it stays quiet.
	DecaysAt time.Time `json:"decays_at"`
}

// Offenders returns the ledger, worst first. Read-only snapshot.
func (e *Engine) Offenders() []OffenderInfo {
	now := time.Now()
	c, err := e.cfg.Read()
	var esc config.BanEscalation
	if err == nil && c != nil {
		esc = c.AutoBan.EffectiveEscalation()
	} else {
		esc = config.DefaultEscalation()
	}
	period := esc.DecayPeriod()

	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]OffenderInfo, 0, len(e.offenders))
	for ip, o := range e.offenders {
		lvl := decayedLevel(o, now, period)
		if lvl <= 0 {
			continue
		}
		next := lvl + 1
		if next > esc.StepCount() {
			next = esc.StepCount()
		}
		d, perm := esc.DurationForLevel(next)
		mins := int(d / time.Minute)
		if perm {
			mins = -1
		}
		out = append(out, OffenderInfo{
			IP: ip, Level: lvl, NextLevel: next, NextMinutes: mins,
			TotalBans: int(o.Count),
			LastBan:   time.Unix(o.Last, 0),
			// Decay is measured in whole periods from the last ban, so
			// the next drop is at Last + (periods already elapsed + 1).
			// "Periods already elapsed" is the difference between the
			// stored level and the decayed one, which avoids recomputing
			// it from the clock and disagreeing by a rounding.
			DecaysAt: time.Unix(o.Last, 0).Add(period * time.Duration(int(o.Level)-lvl+1)),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		return out[i].LastBan.After(out[j].LastBan)
	})
	return out
}

// ForgiveOffender wipes one address's escalation history, so its next ban
// starts at step one again. The operator's override for a false positive
// that has already climbed the ladder — without it, releasing a wrongly
// banned address still leaves it one offence from a much longer ban.
func (e *Engine) ForgiveOffender(ip string) bool {
	e.mu.Lock()
	_, ok := e.offenders[ip]
	delete(e.offenders, ip)
	e.mu.Unlock()
	if ok {
		e.persist()
	}
	return ok
}

// ForgiveAll clears the whole ledger.
func (e *Engine) ForgiveAll() int {
	e.mu.Lock()
	n := len(e.offenders)
	e.offenders = map[string]*offender{}
	e.mu.Unlock()
	if n > 0 {
		e.persist()
	}
	return n
}

// OffenderLevel reports one address's current level. Test and API helper.
func (e *Engine) OffenderLevel(ip string) int {
	now := time.Now()
	period := config.DefaultEscalation().DecayPeriod()
	if c, err := e.cfg.Read(); err == nil && c != nil {
		period = c.AutoBan.EffectiveEscalation().DecayPeriod()
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return decayedLevel(e.offenders[ip], now, period)
}
