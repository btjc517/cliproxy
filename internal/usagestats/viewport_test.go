package usagestats

import (
	"testing"
	"time"
)

func TestUsageViewportPanAndWholeBuckets(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 30, 0, 0, time.UTC)
	store := newTestStore(now)
	for i := 0; i < 72; i++ {
		recordSample(store, sample{auth: "claude-a", at: now.Add(-time.Duration(i) * time.Hour), counters: Counters{Requests: 1, Output: 10}})
	}
	from, to := now.Add(-8*time.Hour), now.Add(-4*time.Hour)
	got := store.UsageBetween(from, to)
	if got.Range != "viewport" || got.BucketSeconds != 3600 || len(got.Starts) != 5 || len(got.Ends) != 5 {
		t.Fatalf("unexpected window: %+v", got)
	}
	if n := usageTotals(got.Accounts["claude-a"]).Requests; n != 5 {
		t.Fatalf("whole bucket total = %d, want 5", n)
	}
	if !got.Starts[0].Equal(from.Truncate(time.Hour)) || !got.Ends[4].Equal(to.Truncate(time.Hour).Add(time.Hour)) {
		t.Fatal("bucket boundaries lost")
	}
	old := store.UsageBetween(from.Add(-24*time.Hour), to.Add(-24*time.Hour))
	if old.Starts[0].Equal(got.Starts[0]) || usageTotals(old.Accounts["claude-a"]).Requests != 5 {
		t.Fatal("pan did not load older buckets")
	}
}

func TestUsageViewportOlderHistoryAndDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		month time.Month
		day   int
		hours time.Duration
	}{{time.March, 29, 23}, {time.October, 25, 25}} {
		from := time.Date(2026, tc.month, tc.day, 0, 0, 0, 0, loc)
		to := from.AddDate(0, 0, 1)
		store := newTestStore(to.AddDate(0, 0, 20))
		recordSample(store, sample{auth: "claude-a", at: from.Add(2 * time.Hour), counters: Counters{Requests: 1, Output: 10}})
		got := store.UsageBetween(from, to)
		if len(got.Starts) != 1 || got.Ends[0].Sub(got.Starts[0]) != tc.hours*time.Hour {
			t.Fatalf("DST day: %+v", got)
		}
		if got.BucketSeconds != 86400 || usageTotals(got.Accounts["claude-a"]).Output != 10 {
			t.Fatal("older daily history was lost")
		}
	}
}

func TestUsageViewportRejectsUnboundedWindows(t *testing.T) {
	now := time.Now()
	store := newTestStore(now)
	for _, to := range []time.Time{now, now.Add(-time.Hour), now.Add(367 * 24 * time.Hour)} {
		if got := store.UsageBetween(now, to); len(got.Starts) != 0 {
			t.Fatalf("invalid window returned %d buckets", len(got.Starts))
		}
	}
}
