package usagestats

import (
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Padding a window keeps the bucket length the window gets alone: a day
// padded to a day and a half stays hourly, a week padded to ten and a half
// days stays hourly, where asking for the padded span itself turns daily.
func TestUsagePaddedKeepsTheWindowsBuckets(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 30, 0, 0, time.UTC)
	store := newTestStore(now)
	for i := 0; i < 24*12; i++ {
		recordSample(store, sample{auth: "claude-a", at: now.Add(-time.Duration(i) * time.Hour), counters: Counters{Requests: 1, Output: 10}})
	}
	to := now.Truncate(time.Hour).Add(time.Hour)
	week := 7 * 24 * time.Hour
	from := to.Add(-week)
	if plain := store.UsageBetween(from.Add(-week/2), to); plain.BucketSeconds != 86400 {
		t.Fatalf("a ten and a half day window got %ds buckets, want days", plain.BucketSeconds)
	}
	got := store.UsageBetweenPadded(from, to, from.Add(-week/2), to)
	if got.BucketSeconds != 3600 {
		t.Fatalf("padded week got %ds buckets, want the week's hours", got.BucketSeconds)
	}
	if len(got.Starts) != int((week+week/2)/time.Hour) || !got.Starts[0].Equal(from.Add(-week/2)) {
		t.Fatalf("padded week has %d buckets from %v, want %d from %v", len(got.Starts), got.Starts[0], (week+week/2)/time.Hour, from.Add(-week/2))
	}
	if got.ViewStart == nil || got.ViewEnd == nil || !got.ViewStart.Equal(from) || !got.ViewEnd.Equal(to) {
		t.Fatalf("view %v to %v, want the window echoed", got.ViewStart, got.ViewEnd)
	}
	// The window's own buckets hold what the plain reply holds.
	plain := store.UsageBetween(from, to)
	if plain.ViewStart != nil {
		t.Fatal("a plain reply echoes a view")
	}
	offset := len(got.Starts) - len(plain.Starts)
	for i := range plain.Starts {
		if !plain.Starts[i].Equal(got.Starts[offset+i]) || plain.Accounts["claude-a"][i] != got.Accounts["claude-a"][offset+i] {
			t.Fatalf("bucket %d differs: %v %+v and %v %+v", i, plain.Starts[i], plain.Accounts["claude-a"][i], got.Starts[offset+i], got.Accounts["claude-a"][offset+i])
		}
	}
}

// Hourly padding stops at the oldest hour kept, and any padding at one
// window length past each end.
func TestUsagePaddedIsTrimmed(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 30, 0, 0, time.UTC)
	store := newTestStore(now)
	cutoff := hourlyCutoff(now)
	from := cutoff.Add(24 * time.Hour).Truncate(time.Hour)
	to := from.Add(48 * time.Hour)
	got := store.UsageBetweenPadded(from, to, from.Add(-10*24*time.Hour), to.Add(10*24*time.Hour))
	if got.BucketSeconds != 3600 {
		t.Fatalf("got %ds buckets, want hours", got.BucketSeconds)
	}
	if got.Starts[0].Before(cutoff.Truncate(time.Hour)) {
		t.Fatalf("hourly padding starts %v, before the oldest hour kept %v", got.Starts[0], cutoff)
	}
	if last := got.Ends[len(got.Ends)-1]; last.After(to.Add(48 * time.Hour)) {
		t.Fatalf("padding runs to %v, more than a window past %v", last, to)
	}
	day := store.UsageBetweenPadded(now.AddDate(0, -2, 0), now.AddDate(0, -1, 0), now.AddDate(-1, 0, 0), now)
	if day.BucketSeconds != 86400 {
		t.Fatalf("a month got %ds buckets, want days", day.BucketSeconds)
	}
	if first := day.Starts[0]; first.Before(now.AddDate(0, -3, -1)) {
		t.Fatalf("daily padding starts %v, more than a window before the window", first)
	}
}

// A padded performance reply keeps the window's step and its totals and
// percentiles, and its series reach over the padding.
func TestPerformancePaddedSeriesKeepWindowFigures(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, london)
	store := newTestStore(now)
	for i := 1; i <= 24*11; i++ {
		ttft := time.Second
		if i > 7*24 {
			ttft = 9 * time.Second
		}
		store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: now.Add(-time.Duration(i) * time.Hour), TTFT: ttft, Latency: 2 * ttft})
	}
	from, to := now.Add(-7*24*time.Hour), now
	plain := store.PerformanceBetween(from, to, nil, nil)
	padded := store.PerformanceBetweenPadded(from, to, from.Add(-84*time.Hour), to, nil, nil)
	if padded.BucketSeconds != plain.BucketSeconds || plain.BucketSeconds != 3600 {
		t.Fatalf("buckets %ds padded, %ds plain, want 3600s both", padded.BucketSeconds, plain.BucketSeconds)
	}
	if plain.FigureStart != nil {
		t.Fatal("a plain reply names figure edges")
	}
	all, base := padded.Scopes["all"], plain.Scopes["all"]
	if all.Requests != base.Requests || all.TTFT != base.TTFT || all.Failed != base.Failed {
		t.Fatalf("padded figures %d %+v, want the window's %d %+v", all.Requests, all.TTFT, base.Requests, base.TTFT)
	}
	if len(all.Series) != len(base.Series)+84 {
		t.Fatalf("padded series has %d points, want %d", len(all.Series), len(base.Series)+84)
	}
	if padded.FigureStart == nil || !padded.FigureStart.Equal(base.Series[0].Start) || padded.FigureEnd == nil || !padded.FigureEnd.After(now.Add(-time.Hour)) {
		t.Fatalf("figure edges %v to %v, want %v to past now", padded.FigureStart, padded.FigureEnd, base.Series[0].Start)
	}
	if first := all.Series[0]; !first.Start.Equal(from.Add(-84*time.Hour).Truncate(time.Hour)) || first.Requests != 1 {
		t.Fatalf("padded series starts %v with %d requests, want the padding's first hour", first.Start, first.Requests)
	}
}

// Parts of a day are only padded back as far as the stored hours.
func TestPerformancePaddedStopsAtStoredHours(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 20, 0, 0, time.UTC)
	store := newTestStore(now)
	from := now.Add(-perfRetention + 24*time.Hour).Truncate(time.Hour)
	to := from.Add(7 * 24 * time.Hour)
	padded := store.PerformanceBetweenPadded(from, to, from.Add(-7*24*time.Hour), to, nil, nil)
	if padded.BucketSeconds != 3600 {
		t.Fatalf("got %ds buckets, want hours", padded.BucketSeconds)
	}
	if first := padded.Scopes["all"].Series[0].Start; first.Before(now.Add(-perfRetention)) {
		t.Fatalf("hourly series starts %v, before the stored hours from %v", first, now.Add(-perfRetention))
	}
}
