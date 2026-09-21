package stats

// Per-dimension statistics.
//
// The two properties that matter are correctness of the WINDOW (the old
// code took a `minutes` argument and ignored it, which is the bug this
// replaces) and a hard bound on memory, because this is the only
// structure in the panel whose size is driven by attacker-controlled
// input: a scanner invents as many distinct paths as it likes.

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

func TestTheWindowIsActuallyApplied(t *testing.T) {
	d := NewDimStore()
	now := time.Now()

	// Old traffic, outside every window we will ask for.
	d.Record(now.Add(-50*time.Hour), map[string]string{DimService: "old"}, 100, 200)
	// Recent traffic.
	d.Record(now.Add(-30*time.Minute), map[string]string{DimService: "new"}, 100, 200)

	rows, _ := d.Top(DimService, 60, 10)
	if len(rows) != 1 || rows[0].Key != "new" {
		t.Fatalf("a one-hour window returned %+v; the window is being ignored, "+
			"which is exactly the bug this replaced", rows)
	}

	rows, _ = d.Top(DimService, 7*24*60, 10)
	if len(rows) != 2 {
		t.Fatalf("a seven-day window returned %d rows, want 2", len(rows))
	}
}

func TestSharesAndErrorsAreComputed(t *testing.T) {
	d := NewDimStore()
	now := time.Now()
	for i := 0; i < 75; i++ {
		d.Record(now, map[string]string{DimService: "busy"}, 10, 200)
	}
	for i := 0; i < 25; i++ {
		d.Record(now, map[string]string{DimService: "quiet"}, 10, 500)
	}

	rows, _ := d.Top(DimService, 60, 10)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].Key != "busy" || rows[0].Count != 75 {
		t.Fatalf("the busiest row is %+v", rows[0])
	}
	if got := int(rows[0].Share + 0.5); got != 75 {
		t.Errorf("share is %.1f%%, want 75%%", rows[0].Share)
	}
	// Only the 500s counted as errors.
	if rows[0].Errors != 0 {
		t.Errorf("a 200-only service reports %d errors", rows[0].Errors)
	}
	if rows[1].Errors != 25 {
		t.Errorf("the 500-only service reports %d errors, want 25", rows[1].Errors)
	}

	sum := d.Summary(60)
	if sum.Requests != 100 {
		t.Errorf("summary counted %d requests, want 100", sum.Requests)
	}
	if int(sum.ErrRate+0.5) != 25 {
		t.Errorf("summary error rate is %.1f%%, want 25%%", sum.ErrRate)
	}
}

// A series has one point per hour, including hours with no traffic.
//
// A chart that silently omits an empty hour draws a straight line through
// an outage, which is the opposite of what the operator needs to see.
func TestSeriesFillsEmptyHours(t *testing.T) {
	d := NewDimStore()
	now := time.Now()
	d.Record(now.Add(-3*time.Hour), map[string]string{DimService: "s"}, 1, 200)
	d.Record(now, map[string]string{DimService: "s"}, 1, 200)

	pts := d.Series(DimService, "s", 6*60)
	if len(pts) < 6 {
		t.Fatalf("a six-hour series has %d points", len(pts))
	}
	zeros := 0
	for _, p := range pts {
		if p.Count == 0 {
			zeros++
		}
	}
	if zeros == 0 {
		t.Fatal("no empty hours in the series; a gap would be drawn as a straight line")
	}
}

// The key budget is a hard cap, and what survives is the busiest.
//
// This is the attacker-facing property: a wordlist scan invents thousands
// of paths in one hour, and without the cap that single hour would cost
// more than the whole rest of the week.
func TestOneHourCannotExceedItsKeyBudget(t *testing.T) {
	d := NewDimStore()
	now := time.Now()

	// One genuinely busy path...
	for i := 0; i < 500; i++ {
		d.Record(now, map[string]string{DimPath: "/real"}, 10, 200)
	}
	// ...and a scan inventing five thousand others.
	for i := 0; i < 5000; i++ {
		d.Record(now, map[string]string{DimPath: fmt.Sprintf("/junk-%d", i)}, 10, 404)
	}

	d.mu.RLock()
	n := len(d.hours[0].Dims[DimPath])
	d.mu.RUnlock()
	if n > keysPerDimension {
		t.Fatalf("one hour holds %d path keys, past the cap of %d",
			n, keysPerDimension)
	}

	// The real path must still be there: it is the busiest by far, and
	// losing it to the noise would make the whole view useless during an
	// attack — which is when it is most needed.
	rows, _ := d.Top(DimPath, 60, 10)
	found := false
	for _, r := range rows {
		if r.Key == "/real" {
			found = true
			if r.Count != 500 {
				t.Errorf("the busy path was counted %d times, want 500", r.Count)
			}
		}
	}
	if !found {
		t.Fatal("the busiest path was evicted by a flood of one-hit paths")
	}
}

// Retention bounds the number of hours.
func TestOldHoursAreDropped(t *testing.T) {
	d := NewDimStore()
	base := time.Now().Add(-400 * time.Hour)
	for i := 0; i < 400; i++ {
		d.Record(base.Add(time.Duration(i)*time.Hour),
			map[string]string{DimService: "s"}, 1, 200)
	}
	if d.Len() > hourlyRetention {
		t.Fatalf("holding %d hourly buckets, past the retention of %d",
			d.Len(), hourlyRetention)
	}
}

// The whole structure has a bounded, affordable footprint.
//
// The STANDING RULE: the cost is locked as behaviour, not left to a
// benchmark. A week at full occupancy is the worst case a real server can
// reach, and it has to fit on a 1 GB VPS alongside everything else.
func TestAFullWeekOfDimensionsIsAffordable(t *testing.T) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	d := NewDimStore()
	base := time.Now().Add(-time.Duration(hourlyRetention-1) * time.Hour)
	// Every hour, every dimension, filled to its key budget.
	for h := 0; h < hourlyRetention; h++ {
		when := base.Add(time.Duration(h) * time.Hour)
		for k := 0; k < keysPerDimension; k++ {
			keys := map[string]string{}
			for _, dim := range AllDimensions {
				keys[dim] = fmt.Sprintf("%s-key-that-is-realistically-long-%d", dim, k)
			}
			d.Record(when, keys, 1024, 200)
		}
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc)

	t.Logf("a full week at maximum occupancy: %d hours, %.2f MB",
		d.Len(), float64(used)/(1<<20))

	// 24 MB is a generous ceiling for the absolute worst case — a real
	// server has far fewer than 64 distinct services or hosts, so the
	// service/host/port dimensions are a fraction of this. It exists to
	// catch a regression that removes the cap, not to police the megabyte.
	const budget = 24 << 20
	if used > budget {
		t.Fatalf("a full week costs %.2f MB, over the %d MB budget",
			float64(used)/(1<<20), budget>>20)
	}
}

// Recording is cheap enough to run per log line.
func TestRecordingIsCheapPerRequest(t *testing.T) {
	d := NewDimStore()
	now := time.Now()
	keys := map[string]string{
		DimService: "xray", DimPath: "/take", DimHost: "vpn.example.com",
		DimStatus: "2xx", DimMethod: "GET", DimIP: "203.0.113.5",
	}

	const n = 200000
	start := time.Now()
	for i := 0; i < n; i++ {
		d.Record(now, keys, 1024, 200)
	}
	elapsed := time.Since(start)
	per := elapsed / n
	t.Logf("%d records in %v = %v each", n, elapsed, per)

	// The access log reader calls this once per line. 10 µs each would
	// make a 100,000-line catch-up take a second, which is fine; the
	// ceiling exists to catch something accidentally quadratic.
	if per > 10*time.Microsecond {
		t.Fatalf("recording costs %v per request", per)
	}
}

// Persistence round-trips, because a week of history that does not
// survive a restart is not a week of history.
func TestDimensionsSurviveASaveAndLoad(t *testing.T) {
	d := NewDimStore()
	now := time.Now()
	for i := 0; i < 10; i++ {
		d.Record(now, map[string]string{DimService: "keepme"}, 100, 200)
	}

	snap := d.Snapshot()
	d2 := NewDimStore()
	d2.Restore(snap)

	rows, _ := d2.Top(DimService, 60, 10)
	if len(rows) != 1 || rows[0].Key != "keepme" || rows[0].Count != 10 {
		t.Fatalf("after a restore the data is %+v", rows)
	}
}

// Anything older than the retention window is dropped on restore, so a
// panel that was down for a fortnight does not come back with stale hours.
func TestRestoreDropsExpiredHours(t *testing.T) {
	old := &hourBucket{
		TS:   time.Now().Add(-400 * time.Hour).Truncate(time.Hour).Unix(),
		Dims: map[string]map[string]*dimCounter{DimService: {"gone": {Count: 5}}},
	}
	fresh := &hourBucket{
		TS:   time.Now().Truncate(time.Hour).Unix(),
		Dims: map[string]map[string]*dimCounter{DimService: {"here": {Count: 5}}},
	}
	d := NewDimStore()
	d.Restore([]*hourBucket{old, fresh})

	if d.Len() != 1 {
		t.Fatalf("restored %d buckets, want only the fresh one", d.Len())
	}
	rows, _ := d.Top(DimService, 7*24*60, 10)
	if len(rows) != 1 || rows[0].Key != "here" {
		t.Fatalf("restored the wrong data: %+v", rows)
	}
}

// A path is reduced to something an operator thinks in.
func TestPathsAreNormalised(t *testing.T) {
	cases := map[string]string{
		"/api/users/8831?tab=2": "/api/users",
		"/api/users":            "/api/users",
		"/take":                 "/take",
		"/":                     "/",
		"":                      "/",
		"/a/b/c/d/e":            "/a/b",
	}
	for in, want := range cases {
		if got := normalisePath(in); got != want {
			t.Errorf("normalisePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The panel must never CLAIM a wider window than the operator chose.
//
// Reported from a live panel: the 1-hour button said "based on 2 hours of
// data" and the 6-hour button said 7.
//
// The first attempt at a fix made the window one bucket narrower, which
// made the label right and started DROPPING traffic: asked at 21:05, a
// request made at 20:35 lives in the 20:00 bucket and is unmistakably
// inside "the last hour". TestTheWindowIsActuallyApplied has guarded that
// since r53 and caught it immediately.
//
// So the contract is deliberately asymmetric, and this test states it:
//   - the SELECTION is generous — it touches every bucket the window
//     overlaps, so nothing recent is lost;
//   - the LABEL is honest — it never exceeds the range that was asked for,
//     and never exceeds the data that actually exists.
func TestReportedHoursNeverExceedTheChosenRange(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	d := NewDimStore()
	for i := 0; i < 8; i++ {
		d.Record(now.Add(-time.Duration(i)*time.Hour),
			map[string]string{DimService: "svc"}, 100, 200)
	}
	for _, c := range []struct{ minutes, maxHours int }{
		{60, 1}, {6 * 60, 6}, {24 * 60, 24},
	} {
		_, hours := d.Top(DimService, c.minutes, 10)
		if hours > c.maxHours {
			t.Errorf("%d minutes: covers_hours = %d, which is MORE than the %d hours "+
				"the operator selected", c.minutes, hours, c.maxHours)
		}
		if hours < 1 {
			t.Errorf("%d minutes: covers_hours = %d, want at least 1", c.minutes, hours)
		}
		if sum := d.Summary(c.minutes); sum.Hours > c.maxHours {
			t.Errorf("%d minutes: Summary.Hours = %d, more than the %d selected",
				c.minutes, sum.Hours, c.maxHours)
		}
		// The table and the totals must agree, or the page contradicts
		// itself on screen.
		if sum := d.Summary(c.minutes); sum.Hours != hours {
			t.Errorf("%d minutes: the table says %d hours and the totals say %d",
				c.minutes, hours, sum.Hours)
		}
	}
	// Only eight hours of data exist, so a week-wide window must report
	// eight, not 168.
	if _, hours := d.Top(DimService, 7*24*60, 10); hours != 8 {
		t.Errorf("a week-wide window over 8 hours of data reported %d hours, want 8", hours)
	}
}

// Traffic inside the window must never be dropped, which is the failure the
// first attempt at the label fix introduced.
func TestRecentTrafficIsNeverLostToBucketAlignment(t *testing.T) {
	d := NewDimStore()
	now := time.Now()
	// Half an hour ago is inside "the last hour" by any reading, but when
	// the clock is a few minutes past the hour it sits in the PREVIOUS
	// bucket.
	d.Record(now.Add(-30*time.Minute), map[string]string{DimService: "recent"}, 100, 200)
	rows, _ := d.Top(DimService, 60, 10)
	found := false
	for _, r := range rows {
		if r.Key == "recent" {
			found = true
		}
	}
	if !found {
		t.Errorf("traffic from 30 minutes ago is missing from a 1-hour window: %+v", rows)
	}
}

// The chart behind a breakdown row must span the same buckets the table
// aggregates, zero-filled so an hour with no traffic is visible as a gap
// rather than smoothed over.
func TestSeriesMatchesTheTable(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	d := NewDimStore()
	for i := 0; i < 10; i++ {
		d.Record(now.Add(-time.Duration(i)*time.Hour),
			map[string]string{DimService: "svc"}, 100, 200)
	}
	for _, minutes := range []int{60, 6 * 60, 8 * 60} {
		pts := d.Series(DimService, "svc", minutes)
		if len(pts) == 0 {
			t.Errorf("%d minutes: the chart is empty", minutes)
			continue
		}
		// Every bucket in range holds exactly one request, so a zero here
		// would mean the series and the store disagree about alignment.
		for _, pt := range pts {
			if pt.Count != 1 {
				t.Errorf("%d minutes: a bucket reports %d, want 1 — the series is "+
					"misaligned with the hourly buckets", minutes, pt.Count)
				break
			}
		}
	}
}
