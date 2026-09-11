package backup

// Retention.
//
// This is the file that matters most in the package. Every other bug here
// costs disk space; a bug here silently deletes the backup somebody is
// about to need, and it does it weeks before they find out.
//
// So the policy is tested as a pure function over synthetic timelines,
// exhaustively, rather than by making files and looking at the directory.

import (
	"fmt"
	"testing"
	"time"

	"shahrag/internal/config"
)

// timeline builds archives every `step` apart, newest last.
func timeline(n int, step time.Duration, end time.Time) []Archive {
	out := make([]Archive, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, Archive{
			Name:       fmt.Sprintf("shahrag-%d.tar.gz", i),
			CreatedAt:  end.Add(-time.Duration(i) * step),
			Generation: GenHourly,
		})
	}
	return out
}

// The headline promise: an hourly backup taken for a year does not leave a
// year of hourly files, and it does not leave only the last day either.
func TestAYearOfHourlyBackupsKeepsAYearOfHistoryInFortyFiles(t *testing.T) {
	end := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	// One a day for a year is enough to exercise every generation
	// without building 8,760 structs.
	list := timeline(365, 24*time.Hour, end)
	r := config.DefaultRetention()

	keep, drop := Keep(list, r)
	t.Logf("365 daily backups -> kept %d, dropped %d", len(keep), len(drop))

	if len(keep) > r.TotalKept()+2 {
		t.Errorf("kept %d files, which is more than the policy's %d", len(keep), r.TotalKept())
	}
	if len(keep) < 20 {
		t.Fatalf("kept only %d files from a year of backups", len(keep))
	}

	// The property that actually matters: history reaches back a year.
	oldest := keep[0].CreatedAt
	for _, a := range keep {
		if a.CreatedAt.Before(oldest) {
			oldest = a.CreatedAt
		}
	}
	span := end.Sub(oldest)
	if span < 300*24*time.Hour {
		t.Fatalf("the oldest kept backup is only %.0f days old; a year of history was not retained", span.Hours()/24)
	}
	t.Logf("oldest kept backup is %.0f days old", span.Hours()/24)
}

// The newest backup is ALWAYS kept. Everything else is a judgement call;
// this one is not.
func TestTheNewestBackupIsAlwaysKept(t *testing.T) {
	end := time.Now()
	for _, n := range []int{1, 2, 5, 50, 500} {
		list := timeline(n, time.Hour, end)
		keep, _ := Keep(list, config.DefaultRetention())
		if len(keep) == 0 {
			t.Fatalf("%d backups: everything was deleted", n)
		}
		newest := keep[0]
		for _, a := range keep {
			if a.CreatedAt.After(newest.CreatedAt) {
				newest = a
			}
		}
		if !newest.CreatedAt.Equal(end) {
			t.Errorf("%d backups: the newest kept is %v, not the newest taken", n, newest.CreatedAt)
		}
	}
}

// A manual backup is never rotated away. Somebody pressed that button on
// purpose before doing something risky.
func TestAManualBackupIsNeverPruned(t *testing.T) {
	end := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	list := timeline(500, time.Hour, end)
	// One manual backup, deliberately ancient and at an awkward hour so
	// no generation would keep it by accident.
	list = append(list, Archive{
		Name:       "shahrag-manual.tar.gz",
		CreatedAt:  end.Add(-400 * 24 * time.Hour),
		Generation: GenManual,
	})

	keep, drop := Keep(list, config.DefaultRetention())
	for _, a := range drop {
		if a.Generation == GenManual {
			t.Fatal("a manual backup was pruned; the one backup the operator took deliberately is the one they will look for")
		}
	}
	found := false
	for _, a := range keep {
		if a.Generation == GenManual {
			found = true
		}
	}
	if !found {
		t.Fatal("the manual backup vanished entirely")
	}
}

// Retention is idempotent: applying the policy twice deletes nothing extra.
// A policy that erodes on every run eventually leaves one file.
func TestApplyingThePolicyTwiceDeletesNothingExtra(t *testing.T) {
	end := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	list := timeline(300, 2*time.Hour, end)
	r := config.DefaultRetention()

	keep1, _ := Keep(list, r)
	keep2, drop2 := Keep(keep1, r)

	if len(drop2) != 0 {
		t.Fatalf("re-applying the policy dropped %d more files; retention erodes and would eventually leave nothing", len(drop2))
	}
	if len(keep2) != len(keep1) {
		t.Fatalf("second pass kept %d, first kept %d", len(keep2), len(keep1))
	}
}

// Nothing is kept twice, and nothing is both kept and dropped.
func TestKeepAndDropPartitionTheInput(t *testing.T) {
	end := time.Now()
	list := timeline(200, 90*time.Minute, end)
	keep, drop := Keep(list, config.DefaultRetention())

	if len(keep)+len(drop) != len(list) {
		t.Fatalf("keep(%d) + drop(%d) != input(%d)", len(keep), len(drop), len(list))
	}
	seen := map[string]int{}
	for _, a := range keep {
		seen[a.Name]++
	}
	for _, a := range drop {
		seen[a.Name]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times across keep and drop", name, n)
		}
	}
}

// A policy of "keep nothing but hourly" behaves like a simple last-N.
func TestASingleGenerationBehavesLikeLastN(t *testing.T) {
	end := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	list := timeline(50, time.Hour, end)
	keep, _ := Keep(list, config.BackupRetention{Hourly: 5})
	if len(keep) != 5 {
		t.Fatalf("kept %d with an hourly-only policy of 5", len(keep))
	}
	// And they are the five most recent.
	for _, a := range keep {
		if end.Sub(a.CreatedAt) > 5*time.Hour {
			t.Errorf("kept a backup %v old under a last-5-hourly policy", end.Sub(a.CreatedAt))
		}
	}
}

// The week bucket must not collapse across a year boundary. ISO week 1 of
// two different years is two different weeks, and an integer week number
// alone would merge them — deleting a year-old backup that the policy
// promised to keep.
func TestWeekBucketsDoNotCollideAcrossYears(t *testing.T) {
	a := time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC) // 2026 week 2
	b := time.Date(2027, 1, 4, 3, 0, 0, 0, time.UTC) // 2027 week 1
	c := time.Date(2025, 1, 6, 3, 0, 0, 0, time.UTC) // 2025 week 2
	_, _, wa, _ := bucketKeys(a)
	_, _, wb, _ := bucketKeys(b)
	_, _, wc, _ := bucketKeys(c)
	if wa == wb || wa == wc || wb == wc {
		t.Fatalf("week buckets collide across years: %s %s %s", wa, wb, wc)
	}
}

// Pruning on disk removes exactly what Keep said to remove.
func TestPruneRemovesExactlyWhatThePolicySays(t *testing.T) {
	e, mgr, _ := newEngine(t)
	if _, err := mgr.Mutate(func(c *config.Config) error {
		// A tight policy so the test does not need a hundred files.
		c.Backup.Retention = config.BackupRetention{Hourly: 2}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Four backups, each in its own hour bucket.
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		e.now = func() time.Time { return at }
		if _, err := e.Create(config.BackupConfig, GenHourly); err != nil {
			t.Fatal(err)
		}
	}
	e.now = time.Now

	before, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 4 {
		t.Fatalf("expected 4 archives on disk, found %d", len(before))
	}

	removed, err := e.Prune()
	if err != nil {
		t.Fatal(err)
	}
	after, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("4 archives, hourly=2 -> removed %d, left %d", len(removed), len(after))
	if len(after) != 2 {
		t.Fatalf("after pruning there are %d archives, want 2", len(after))
	}
	// And the two that survived are the newest two.
	for _, a := range after {
		if a.CreatedAt.Before(base.Add(2 * time.Hour)) {
			t.Errorf("pruning kept %v, which is not one of the newest two", a.CreatedAt)
		}
	}
}

// ── the schedule ─────────────────────────────────────────────

// A daily backup fires at its chosen hour and only once.
func TestADailyScheduleFiresOnceAtTheChosenHour(t *testing.T) {
	b := config.DefaultBackup()
	b.IntervalHours = 24
	b.Hour = 3

	last := time.Date(2026, 9, 10, 3, 0, 0, 0, time.UTC)
	fires := 0
	// Walk a full day minute by minute.
	for i := 0; i < 24*60; i++ {
		now := last.Add(time.Duration(i+1) * time.Minute)
		if Due(b, last, now) {
			fires++
			if now.Hour() != 3 {
				t.Fatalf("fired at %02d:%02d, not at the chosen hour", now.Hour(), now.Minute())
			}
			last = now
		}
	}
	if fires != 1 {
		t.Fatalf("a daily schedule fired %d times in a day", fires)
	}
}

// The one that matters for reliability: a restart must not cause a burst.
func TestRestartsDoNotCauseABurstOfBackups(t *testing.T) {
	b := config.DefaultBackup()
	b.IntervalHours = 24
	b.Hour = 3

	// The panel is restarted repeatedly around the scheduled hour. The
	// last-run time is persisted, so every one of these sees the same
	// stored value.
	last := time.Date(2026, 9, 11, 3, 0, 30, 0, time.UTC)
	for _, mins := range []int{0, 1, 2, 5, 30, 60} {
		now := last.Add(time.Duration(mins) * time.Minute)
		if Due(b, last, now) {
			t.Fatalf("a backup was due again %d minutes after the last one", mins)
		}
	}
}

// A never-configured install does not back up the instant it starts: it
// waits for the scheduled hour. Otherwise every restart during setup
// writes an archive.
func TestAFreshInstallWaitsForItsScheduledHour(t *testing.T) {
	b := config.DefaultBackup()
	b.Hour = 3
	noon := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	if Due(b, time.Time{}, noon) {
		t.Fatal("a fresh install took a backup at noon instead of waiting for 03:00")
	}
	three := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	if !Due(b, time.Time{}, three) {
		t.Fatal("a fresh install did not back up at its scheduled hour")
	}
}

// A sub-daily schedule ignores the hour and just uses the interval.
func TestASubDailyScheduleUsesTheIntervalNotTheHour(t *testing.T) {
	b := config.DefaultBackup()
	b.IntervalHours = 6
	b.Hour = 3
	last := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)

	if Due(b, last, last.Add(5*time.Hour)) {
		t.Error("a 6-hour schedule fired after 5 hours")
	}
	if !Due(b, last, last.Add(6*time.Hour+time.Minute)) {
		t.Error("a 6-hour schedule did not fire after 6 hours, because it was waiting for 03:00")
	}
}

// Disabled means disabled.
func TestADisabledScheduleNeverFires(t *testing.T) {
	b := config.DefaultBackup()
	b.Enabled = false
	base := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 48; i++ {
		if Due(b, time.Time{}, base.Add(time.Duration(i)*time.Hour)) {
			t.Fatal("a disabled schedule fired")
		}
	}
}
