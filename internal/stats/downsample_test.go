package stats

// Resources are sampled every 5 seconds and kept for 24 hours, which is
// 17,280 points. Shipping all of them made the long ranges feel wrong: over
// a megabyte of JSON per refresh, parsed and plotted every 5 seconds, so the
// chart lagged the range that was selected. These tests pin the fix.

import (
	"testing"
	"time"
)

func fillRes(c *Collector, dur time.Duration, every time.Duration) {
	now := time.Now()
	for t := now.Add(-dur); !t.After(now); t = t.Add(every) {
		c.resources = append(c.resources, ResourceSnap{TS: t.Unix()})
	}
}

// The window must be honoured exactly at every range — that is the bug the
// user reported, where 12h and 24h did not match the selection.
func TestResourceTimeseriesSpansTheRequestedWindow(t *testing.T) {
	c := &Collector{maxAge: 24 * time.Hour}
	fillRes(c, 24*time.Hour, 5*time.Second)

	for _, tc := range []struct {
		minutes int
		wantH   float64
	}{{60, 1}, {360, 6}, {720, 12}, {1440, 24}} {
		got := c.ResourceTimeseries(tc.minutes)
		if len(got) < 2 {
			t.Fatalf("minutes=%d returned %d points", tc.minutes, len(got))
		}
		spanH := float64(got[len(got)-1].TS-got[0].TS) / 3600
		// Allow a sample's worth of slack at each end.
		if spanH < tc.wantH-0.05 || spanH > tc.wantH+0.05 {
			t.Errorf("minutes=%d spans %.2fh, want %.0fh", tc.minutes, spanH, tc.wantH)
		}
		if len(got) > maxChartPoints {
			t.Errorf("minutes=%d returned %d points, over the %d cap",
				tc.minutes, len(got), maxChartPoints)
		}
	}
}

// Downsampling must keep the endpoints, or the axis silently shrinks.
func TestDownsampleKeepsFirstAndLast(t *testing.T) {
	in := make([]ResourceSnap, 5000)
	for i := range in {
		in[i] = ResourceSnap{TS: int64(i)}
	}
	out := downsampleRes(in)
	if len(out) != maxChartPoints {
		t.Fatalf("got %d points, want %d", len(out), maxChartPoints)
	}
	if out[0].TS != in[0].TS {
		t.Errorf("first sample lost: %d != %d", out[0].TS, in[0].TS)
	}
	if out[len(out)-1].TS != in[len(in)-1].TS {
		t.Errorf("last sample lost: %d != %d", out[len(out)-1].TS, in[len(in)-1].TS)
	}
	// Strictly increasing: a duplicated index would draw a flat step that
	// is not in the data.
	for i := 1; i < len(out); i++ {
		if out[i].TS <= out[i-1].TS {
			t.Fatalf("samples not increasing at %d: %d then %d",
				i, out[i-1].TS, out[i].TS)
		}
	}
}

// A short series must pass through untouched — no point thinning 20 points.
func TestDownsampleLeavesShortSeriesAlone(t *testing.T) {
	in := make([]ResourceSnap, 20)
	for i := range in {
		in[i] = ResourceSnap{TS: int64(i)}
	}
	out := downsampleRes(in)
	if len(out) != len(in) {
		t.Fatalf("got %d, want %d", len(out), len(in))
	}
	if len(downsampleRes(nil)) != 0 {
		t.Error("nil input should stay empty")
	}
	if len(downsampleRes([]ResourceSnap{{TS: 7}})) != 1 {
		t.Error("a single sample should survive")
	}
}
