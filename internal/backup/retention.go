package backup

// Generational retention — grandfather / father / son.
//
// "Keep the last N" is the obvious policy and it is the wrong one. Keeping
// the last 30 hourly backups covers thirty HOURS; the mistake that needs a
// backup is very often noticed weeks later — a certificate that quietly
// stopped renewing, a service somebody disabled in March, a path that was
// changed and broke one client nobody tested.
//
// So a backup is PROMOTED rather than duplicated. The newest backup taken
// on a given day is that day's daily; the newest in a given ISO week is
// that week's weekly; the newest in a month is that month's monthly. One
// file can hold several of those roles at once, which is what makes a year
// of history cost about forty files instead of four hundred.
//
// A backup is deleted only when it holds NO role. That is the whole rule,
// and stating it that way is what makes it testable: for any policy and
// any set of files, a file survives if and only if it is the newest in a
// bucket that is still within its generation's count.

import (
	"os"
	"sort"
	"time"

	"shahrag/internal/config"
)

// Generations, in order of increasing coarseness.
const (
	GenManual  = "manual"
	GenHourly  = "hourly"
	GenDaily   = "daily"
	GenWeekly  = "weekly"
	GenMonthly = "monthly"
)

// bucketKeys returns the hour, day, week and month a time falls in.
//
// Computed as strings rather than as numbers so a week boundary crossing a
// year does not collapse onto the same key: ISO week 1 of 2027 and week 1
// of 2026 are different buckets, which an integer week number alone would
// not distinguish.
func bucketKeys(t time.Time) (hour, day, week, month string) {
	hour = t.Format("2006-01-02T15")
	day = t.Format("2006-01-02")
	y, w := t.ISOWeek()
	week = time.Date(y, 1, 1, 0, 0, 0, 0, t.Location()).Format("2006") +
		"-W" + pad2(w)
	month = t.Format("2006-01")
	return
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// Keep decides which archives survive a policy.
//
// Pure and exported so it can be tested exhaustively without touching the
// disk — the dangerous bug in a retention policy is deleting something it
// should have kept, and that must be provable rather than observed.
//
// A MANUAL backup is never pruned. An operator who pressed the button
// before making a change is relying on that file being there afterwards,
// and silently rotating it away because it happened to be a busy hour
// would be a betrayal of the one backup they consciously took.
func Keep(list []Archive, r config.BackupRetention) (keep, drop []Archive) {
	// Newest first, so the first archive seen in a bucket is that
	// bucket's representative.
	sorted := append([]Archive{}, list...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	seenHour := map[string]bool{}
	seenDay := map[string]bool{}
	seenWeek := map[string]bool{}
	seenMonth := map[string]bool{}
	nHour, nDay, nWeek, nMonth := 0, 0, 0, 0

	for _, a := range sorted {
		if a.Generation == GenManual {
			keep = append(keep, a)
			continue
		}
		h, d, w, m := bucketKeys(a.CreatedAt)
		wanted := false

		// Each generation takes the NEWEST archive in each of its
		// buckets, up to its count. Checked in order from finest to
		// coarsest so a single recent file can satisfy several roles.
		if !seenHour[h] && nHour < r.Hourly {
			seenHour[h] = true
			nHour++
			wanted = true
		}
		if !seenDay[d] && nDay < r.Daily {
			seenDay[d] = true
			nDay++
			wanted = true
		}
		if !seenWeek[w] && nWeek < r.Weekly {
			seenWeek[w] = true
			nWeek++
			wanted = true
		}
		if !seenMonth[m] && nMonth < r.Monthly {
			seenMonth[m] = true
			nMonth++
			wanted = true
		}

		if wanted {
			keep = append(keep, a)
		} else {
			drop = append(drop, a)
		}
	}
	return keep, drop
}

// Prune applies the policy on disk and returns what it removed.
func (e *Engine) Prune() ([]Archive, error) {
	c, err := e.cfg.Read()
	if err != nil {
		return nil, err
	}
	list, err := e.List()
	if err != nil {
		return nil, err
	}
	_, drop := Keep(list, c.Backup.EffectiveRetention())
	removed := make([]Archive, 0, len(drop))
	for _, a := range drop {
		if err := os.Remove(a.Path); err == nil {
			removed = append(removed, a)
		}
		// A failed delete is deliberately not fatal. Leaving an extra
		// file costs a few kilobytes; aborting the run because of one
		// permission problem would stop the NEXT backup from being
		// taken, which is the thing that actually matters.
	}
	return removed, nil
}
