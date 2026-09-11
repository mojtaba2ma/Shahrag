package backup

// The scheduler.
//
// It ticks once a minute and asks a single question: is it time yet? That
// is far simpler than a cron parser and it is the right trade here,
// because the interval is "every N hours at hour H" rather than an
// arbitrary expression — and a minute of granularity on a daily backup is
// irrelevant.
//
// The important property is that it survives a restart without either
// skipping a backup or taking a burst of them. It does that by recording
// the last run in the config rather than in memory: a panel restarted at
// 02:59 and again at 03:01 takes exactly one 03:00 backup.

import (
	"log"
	"sync"
	"time"

	"shahrag/internal/config"
)

// Scheduler runs backups on a timer.
type Scheduler struct {
	eng *Engine
	cfg *config.Manager

	stop chan struct{}
	mu   sync.Mutex
	// running guards against a slow backup overlapping the next tick.
	// On a small VPS an encrypted full backup takes about a tenth of a
	// second, but a busy disk can make that much worse, and two backups
	// writing at once would both be pruning the same directory.
	running bool

	// notify sends a line to Telegram when configured.
	notify func(string)
	// offsite is called with a finished archive.
	offsite func(*Archive) error
}

// NewScheduler builds one.
func NewScheduler(eng *Engine, cfg *config.Manager) *Scheduler {
	return &Scheduler{eng: eng, cfg: cfg, stop: make(chan struct{})}
}

// SetNotifier installs the alerting hook.
func (s *Scheduler) SetNotifier(f func(string)) { s.notify = f }

// SetOffsite installs the off-site sender.
func (s *Scheduler) SetOffsite(f func(*Archive) error) { s.offsite = f }

// tickInterval is how often the scheduler wakes.
//
// One minute. The cost is one config read per minute (101 µs since r44),
// which is 0.0002% of a core; the benefit is that "daily at 03:00" happens
// at 03:00 rather than at whatever time the service last started.
const tickInterval = time.Minute

// Start begins the timer.
func (s *Scheduler) Start() {
	go s.loop()
}

// Stop ends it.
func (s *Scheduler) Stop() { close(s.stop) }

func (s *Scheduler) loop() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.tick(time.Now())
		}
	}
}

// Due reports whether a backup should run now.
//
// Pure, and exported for the test: "does the schedule fire at the right
// moment" is the entire behaviour of this file, and it must be provable
// across restarts, midnights and month ends without waiting a day.
func Due(b config.BackupSettings, last time.Time, now time.Time) bool {
	if !b.Enabled {
		return false
	}
	interval := b.EffectiveInterval()

	// Never run: run at the next matching hour rather than immediately.
	// Taking a backup the instant the panel starts sounds helpful and is
	// not — it means every restart during an install writes one, and the
	// operator ends up with fifteen identical archives from the
	// afternoon they set the server up.
	if last.IsZero() {
		return atScheduledHour(b, now)
	}

	if now.Sub(last) < interval {
		return false
	}
	// For a daily-or-coarser schedule, hold until the chosen hour so the
	// backup lands in the quiet part of the night rather than drifting.
	if interval >= 24*time.Hour {
		return atScheduledHour(b, now)
	}
	return true
}

func atScheduledHour(b config.BackupSettings, now time.Time) bool {
	return now.Hour() == b.Hour && now.Minute() < 2
}

// generationFor labels an automatic run.
//
// Every scheduled backup is written as "hourly" and PROMOTED by the
// retention policy, rather than being labelled daily or weekly when taken.
// That way changing the policy re-interprets the history that already
// exists instead of only applying to files taken from now on.
func generationFor() string { return GenHourly }

func (s *Scheduler) tick(now time.Time) {
	c, err := s.cfg.Read()
	if err != nil {
		return
	}
	if !Due(c.Backup, c.Shahrag.LastBackup, now) {
		return
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	// The timestamp is recorded BEFORE the work, not after.
	//
	// If a backup fails every time — a full disk, say — recording only
	// on success would make it retry every single minute for ever, and
	// the log would be the only sign. Recording first means one attempt
	// per interval whatever happens, which is also what an operator
	// would do by hand.
	if _, err := s.cfg.Mutate(func(cc *config.Config) error {
		cc.Shahrag.LastBackup = now
		return nil
	}); err != nil {
		log.Printf("[shahrag] backup: cannot record the run time: %v", err)
	}

	a, err := s.eng.Create(c.Backup.EffectiveScope(), generationFor())
	if err != nil {
		log.Printf("[shahrag] backup failed: %v", err)
		if s.notify != nil {
			s.notify("Backup FAILED: " + err.Error())
		}
		return
	}
	log.Printf("[shahrag] backup written: %s (%d bytes)", a.Name, a.Size)

	if removed, err := s.eng.Prune(); err == nil && len(removed) > 0 {
		log.Printf("[shahrag] backup: pruned %d old archive(s)", len(removed))
	}

	if s.offsite != nil && c.Backup.Offsite.AnyOffsite() {
		if err := s.offsite(a); err != nil {
			log.Printf("[shahrag] backup: off-site copy failed: %v", err)
			if s.notify != nil {
				// A failed off-site copy is reported loudly. It is the
				// silent failure that matters: the local file exists, the
				// panel looks healthy, and the copy the operator is
				// actually relying on has not arrived for six weeks.
				s.notify("Backup off-site copy FAILED: " + err.Error())
			}
		}
	}
}

// RunNow takes a backup immediately, as the panel's button does.
func (s *Scheduler) RunNow(scope, generation string) (*Archive, error) {
	a, err := s.eng.Create(scope, generation)
	if err != nil {
		return nil, err
	}
	_, _ = s.eng.Prune()
	if c, rerr := s.cfg.Read(); rerr == nil && s.offsite != nil &&
		c.Backup.Offsite.AnyOffsite() {
		if err := s.offsite(a); err != nil {
			// Reported to the caller, but the local backup still
			// succeeded and the archive is returned — a failed copy is
			// not a failed backup.
			return a, err
		}
	}
	return a, nil
}
