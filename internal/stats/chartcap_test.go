package stats

// Every chart series must stay inside the point cap.
//
// Only the resource series was capped. Requests, connections and TCP/UDP
// were not, so on the same page one chart shipped 720 points for a 1-year
// range while the other three shipped 8,760 each — three charts stuttered
// and one did not.
//
// The counter series (requests) must be SUMMED when thinned, not sampled:
// picking every twelfth bucket would quietly discard eleven twelfths of the
// year's traffic and report it as the truth.

import (
	"testing"
	"time"
)

// fillYear loads one hourly sample per hour for a year, which is what the
// coarsest retention tier leaves behind.
func fillYear(c *Collector) (hours int) {
	now := time.Now()
	hours = 365 * 24
	for h := hours; h > 0; h-- {
		ts := now.Add(-time.Duration(h) * time.Hour).Unix()
		c.conns = append(c.conns, ConnectionSnapshot{TS: ts, Active: 4})
		c.protos = append(c.protos, ProtoSnap{TS: ts, TCP: 3, UDP: 2})
		c.buckets = append(c.buckets, &MinuteBucket{TS: ts, Total: 10, Success: 10, Bytes: 100})
	}
	return hours
}

func TestEveryTimeseriesRespectsTheChartCap(t *testing.T) {
	c := newTestCollector()
	fillYear(c)
	fillRes(c, 365*24*time.Hour, time.Hour)

	const year = 525600
	cases := []struct {
		name string
		n    int
	}{
		{"requests", len(c.RequestsTimeseries(year, 60))},
		{"connections", len(c.ConnectionsTimeseries(year))},
		{"proto", len(c.ProtoTimeseries(year))},
		{"resources", len(c.ResourceTimeseries(year))},
	}
	for _, tc := range cases {
		if tc.n == 0 {
			t.Errorf("%s returned nothing for a 1-year range", tc.name)
		}
		if tc.n > maxChartPoints {
			t.Errorf("%s returned %d points for a 1-year range, over the %d cap",
				tc.name, tc.n, maxChartPoints)
		}
		t.Logf("%-12s 1y -> %d points", tc.name, tc.n)
	}
}

// Thinning a COUNTER by sampling would erase traffic. The total across the
// whole returned series must equal the total that was recorded.
func TestThinningRequestsPreservesTheTotal(t *testing.T) {
	c := newTestCollector()
	hours := fillYear(c)
	wantTotal := int64(hours) * 10

	series := c.RequestsTimeseries(525600, 60)
	var got int64
	for _, p := range series {
		got += p["total"].(int64)
	}
	if got != wantTotal {
		t.Errorf("a year of traffic came back as %d requests, but %d were "+
			"recorded — thinning threw traffic away", got, wantTotal)
	}
}

// The returned window must still span the range that was asked for; a cap
// that silently trims one end would make the axis lie.
func TestThinnedSeriesStillSpansTheWindow(t *testing.T) {
	c := newTestCollector()
	fillYear(c)

	series := c.RequestsTimeseries(525600, 60)
	if len(series) < 2 {
		t.Fatalf("only %d points", len(series))
	}
	first := series[0]["ts"].(int64)
	last := series[len(series)-1]["ts"].(int64)
	spanDays := float64(last-first) / 86400
	if spanDays < 360 {
		t.Errorf("the 1-year series spans only %.0f days", spanDays)
	}

	conns := c.ConnectionsTimeseries(525600)
	spanDays = float64(conns[len(conns)-1].TS-conns[0].TS) / 86400
	if spanDays < 360 {
		t.Errorf("the connections series spans only %.0f days", spanDays)
	}
}

// A short range must be returned untouched — no grouping, no loss of
// resolution on the live view.
func TestShortRangesAreNotThinned(t *testing.T) {
	c := newTestCollector()
	now := time.Now()
	for i := 60; i > 0; i-- {
		ts := now.Add(-time.Duration(i) * time.Minute).Unix()
		c.buckets = append(c.buckets, &MinuteBucket{TS: ts, Total: 1})
		c.conns = append(c.conns, ConnectionSnapshot{TS: ts, Active: 1})
	}
	if n := len(c.RequestsTimeseries(120, 60)); n != 60 {
		t.Errorf("an hour of minutes came back as %d points, want 60", n)
	}
	if n := len(c.ConnectionsTimeseries(120)); n != 60 {
		t.Errorf("connections came back as %d points, want 60", n)
	}
}
