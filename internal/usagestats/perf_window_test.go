package usagestats

import (
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// The step is the finest of 1, 3, 6, 12 and 24 hours that keeps the window
// to 200 buckets, and whole days once the window reaches the rolled up days.
func TestPerfStepPicksFinestStepUnder200Buckets(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, london)
	longAgo := now.AddDate(-2, 0, 0)
	for _, tc := range []struct {
		name  string
		span  time.Duration
		since time.Time
		want  int
	}{
		{"one hour", time.Hour, longAgo, 1},
		{"one day", 24 * time.Hour, longAgo, 1},
		{"seven days", 7 * 24 * time.Hour, longAgo, 1},
		{"200 hours", 200 * time.Hour, longAgo, 1},
		{"201 hours", 201 * time.Hour, longAgo, 3},
		{"25 days", 25 * 24 * time.Hour, longAgo, 3},
		{"26 days", 26 * 24 * time.Hour, longAgo, 6},
		{"50 days", 50 * 24 * time.Hour, longAgo, 6},
		{"51 days", 51 * 24 * time.Hour, longAgo, 12},
		{"100 days", 100 * 24 * time.Hour, longAgo, 12},
		{"101 days", 101 * 24 * time.Hour, longAgo, 24},
		{"366 days", 366 * 24 * time.Hour, longAgo, 24},
		{"one hour in the rolled up days", time.Hour, now.Add(-30 * time.Minute), 24},
		{"seven days reaching the rolled up days", 7 * 24 * time.Hour, now.Add(-3 * 24 * time.Hour), 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from := now.Add(-tc.span)
			if got := perfStep(from, now, tc.since); got != tc.want {
				t.Fatalf("perfStep(%v) = %dh, want %dh", tc.span, got, tc.want)
			}
		})
	}
}

// The series runs from the bucket that holds the start to the bucket that
// holds now, even when the window runs on into the future.
func TestPerformanceBetweenClampsSeriesToNow(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, london)
	store := newTestStore(now)
	store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: now.Add(-time.Minute), TTFT: time.Second, Latency: 2 * time.Second})

	from := now.Add(-84 * time.Hour)
	to := now.Add(84 * time.Hour)
	perf := store.PerformanceBetween(from, to, nil, nil)
	if perf.Range != CustomRange || perf.BucketSeconds != 3600 {
		t.Fatalf("range %q bucket %ds, want custom and 3600s for a 7 day window", perf.Range, perf.BucketSeconds)
	}
	series := perf.Scopes["all"].Series
	first := time.Date(2026, 10, 6, 2, 0, 0, 0, london)
	last := time.Date(2026, 10, 9, 14, 0, 0, 0, london)
	if len(series) == 0 || !series[0].Start.Equal(first) || !series[len(series)-1].Start.Equal(last) {
		t.Fatalf("series has %d points from %v to %v, want hourly from %v to %v", len(series), series[0].Start, series[len(series)-1].Start, first, last)
	}
	if len(series) != 85 {
		t.Fatalf("series has %d points, want 85 hours", len(series))
	}
	if series[len(series)-1].Requests != 1 || perf.Scopes["all"].Requests != 1 {
		t.Fatalf("newest point %+v total %d, want the one request", series[len(series)-1], perf.Scopes["all"].Requests)
	}

	future := store.PerformanceBetween(now.Add(time.Hour), now.Add(5*time.Hour), nil, nil)
	if got := future.Scopes["all"]; len(got.Series) != 0 || got.Requests != 0 {
		t.Fatalf("window after now has %d points and %d requests, want none", len(got.Series), got.Requests)
	}

	past := store.PerformanceBetween(now.Add(-10*time.Hour), now.Add(-4*time.Hour-30*time.Minute), nil, nil)
	pastSeries := past.Scopes["all"].Series
	if len(pastSeries) != 6 || !pastSeries[0].Start.Equal(now.Add(-10*time.Hour).Truncate(time.Hour)) || !pastSeries[5].Start.Equal(time.Date(2026, 10, 9, 9, 0, 0, 0, london)) {
		t.Fatalf("past window has %d points, want 04:00 to 09:00 local", len(pastSeries))
	}
}

// Requests outside the window count nowhere: totals, percentiles and
// histograms are those of the requests inside it.
func TestPerformanceBetweenPercentilesCoverOnlyTheWindow(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, london)
	store := newTestStore(now)
	from := time.Date(2026, 10, 8, 0, 0, 0, 0, london)
	to := time.Date(2026, 10, 9, 0, 0, 0, 0, london)
	var inside histogram
	for i := 0; i < 40; i++ {
		ttft := time.Duration(200+10*i) * time.Millisecond
		store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: from.Add(time.Duration(i) * 30 * time.Minute), TTFT: ttft, Latency: ttft + time.Second})
		inside.observe(float64(ttft/time.Millisecond), ttftBase)
	}
	// Slow requests just before and just after the window.
	for i := 0; i < 100; i++ {
		store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: from.Add(-time.Duration(i+1) * time.Minute), TTFT: 9 * time.Second, Latency: 10 * time.Second})
		store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: to.Add(time.Duration(i) * time.Minute), TTFT: 9 * time.Second, Latency: 10 * time.Second, Failed: i%2 == 0})
	}

	perf := store.PerformanceBetween(from, to, nil, nil)
	all := perf.Scopes["all"]
	if all.Requests != 40 || all.Failed != 0 {
		t.Fatalf("requests %d failed %d, want 40 and 0 inside the window", all.Requests, all.Failed)
	}
	if want := roundMillis(inside.percentile(0.5, ttftBase)); all.TTFT.P50 != want {
		t.Fatalf("ttft p50 = %d, want %d from the window's requests only", all.TTFT.P50, want)
	}
	if want := roundMillis(inside.percentile(0.99, ttftBase)); all.TTFT.P99 != want {
		t.Fatalf("ttft p99 = %d, want %d from the window's requests only", all.TTFT.P99, want)
	}
	var binned int64
	for _, bin := range all.TTFTHist {
		binned += bin.Count
	}
	if binned != 40 {
		t.Fatalf("ttft histogram holds %d requests, want 40", binned)
	}
	if len(all.Series) != 24 || !all.Series[0].Start.Equal(from) {
		t.Fatalf("series has %d points from %v, want 24 hours from %v", len(all.Series), all.Series[0].Start, from)
	}
}

// The selection scope merges the selected credentials over the custom window
// exactly as it does over a fixed range.
func TestPerformanceBetweenSelectionMergesCredentials(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, london)
	store := newTestStore(now)
	add := func(auth string, n int, ttft time.Duration) {
		for i := 0; i < n; i++ {
			store.record(coreusage.Record{AuthID: auth, Provider: "claude", RequestedAt: now.Add(-time.Duration(i+1) * 10 * time.Minute), TTFT: ttft, Latency: ttft + time.Second})
		}
	}
	add("claude-fast", 30, 100*time.Millisecond)
	add("claude-slow", 10, 5*time.Second)
	add("claude-other", 200, 50*time.Millisecond)

	from, to := now.Add(-48*time.Hour), now
	custom := store.PerformanceBetween(from, to, []string{"claude-fast", "claude-slow", "unknown"}, nil)
	selection, ok := custom.Scopes[SelectionScope]
	if !ok {
		t.Fatal("no selection scope over the custom window")
	}
	if selection.Requests != 40 {
		t.Fatalf("selection has %d requests, want 40", selection.Requests)
	}
	var merged histogram
	for i := 0; i < 30; i++ {
		merged.observe(100, ttftBase)
	}
	for i := 0; i < 10; i++ {
		merged.observe(5000, ttftBase)
	}
	if want := roundMillis(merged.percentile(0.9, ttftBase)); selection.TTFT.P90 != want {
		t.Fatalf("selection ttft p90 = %d, want %d from the merged histograms", selection.TTFT.P90, want)
	}
	for _, key := range []string{"all", "claude", "codex", "claude-fast", "claude-slow", "claude-other"} {
		if _, ok := custom.Scopes[key]; !ok {
			t.Fatalf("scope %q missing", key)
		}
	}
	if none := store.PerformanceBetween(from, to, []string{"unknown"}, nil); len(none.Scopes) == 0 {
		t.Fatal("no scopes")
	} else if _, ok := none.Scopes[SelectionScope]; ok {
		t.Fatal("selection scope present without a known id")
	}
	fixed := store.SummaryForSelection(0, windows["7d"], []string{"claude-fast", "claude-slow"}, nil).Performance.Scopes[SelectionScope]
	if fixed.Requests != selection.Requests || fixed.TTFT != selection.TTFT {
		t.Fatalf("fixed range selection %+v differs from custom %+v over the same requests", fixed.TTFT, selection.TTFT)
	}
}

// A window that reaches the rolled up days uses whole local days and reads
// the rollups, so nothing older than 35 days is lost.
func TestPerformanceBetweenReadsRolledUpDays(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, london)
	store := newTestStore(now)
	old := time.Date(2026, 8, 20, 10, 0, 0, 0, london)
	store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: old, TTFT: time.Second, Latency: 2 * time.Second})
	store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: now.Add(-time.Hour), TTFT: time.Second, Latency: 2 * time.Second})

	perf := store.PerformanceBetween(now.AddDate(0, 0, -60), now, nil, nil)
	if perf.BucketSeconds != 86400 {
		t.Fatalf("bucket %ds, want one day", perf.BucketSeconds)
	}
	all := perf.Scopes["all"]
	if all.Requests != 2 {
		t.Fatalf("requests %d, want 2 with the rolled up one", all.Requests)
	}
	found := false
	for _, point := range all.Series {
		if point.Start.Equal(time.Date(2026, 8, 20, 0, 0, 0, 0, london)) && point.Requests == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("rolled up request not in its day")
	}
	if len(store.perf["claude-a"]) != 1 || len(store.perfDaily["claude-a"]) != 1 {
		t.Fatal("old hour not rolled up as expected")
	}
}

// Out of order, too short or too long windows give empty scopes; the handler
// turns them into a 400 before this.
func TestPerformanceBetweenRejectsBadWindows(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, time.UTC)
	store := newTestStore(now)
	for _, tc := range []struct {
		name     string
		from, to time.Time
	}{
		{"reversed", now, now.Add(-2 * time.Hour)},
		{"equal", now, now},
		{"59 minutes", now.Add(-59 * time.Minute), now},
		{"367 days", now.AddDate(0, 0, -367), now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ValidPerfWindow(tc.from, tc.to) == nil {
				t.Fatal("window accepted")
			}
			if perf := store.PerformanceBetween(tc.from, tc.to, nil, nil); len(perf.Scopes) != 0 || perf.Range != CustomRange {
				t.Fatalf("got %d scopes, want none", len(perf.Scopes))
			}
		})
	}
	if ValidPerfWindow(now.Add(-time.Hour), now) != nil || ValidPerfWindow(now.AddDate(0, 0, -366), now) != nil {
		t.Fatal("1 hour or 366 day window refused")
	}
}
