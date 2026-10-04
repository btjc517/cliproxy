package usagestats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, errZone := time.LoadLocation(name)
	if errZone != nil {
		t.Fatal(errZone)
	}
	return loc
}

// checkSeriesHoldsRecords checks that the starts of a series rise, that the
// newest bucket starts no later than now, and that each request was counted in
// the bucket whose span holds it, and nowhere when it is older than the range.
func checkSeriesHoldsRecords(t *testing.T, label string, now time.Time, starts []time.Time, counts []int64, records []time.Time) {
	t.Helper()
	last := len(starts) - 1
	if starts[last].After(now) {
		t.Fatalf("%s: newest bucket starts %s, after now %s", label, starts[last], now)
	}
	want := make([]int64, len(starts))
	for i := range starts {
		if i > 0 && !starts[i].After(starts[i-1]) {
			t.Fatalf("%s: bucket %d starts %s, not after %s", label, i, starts[i], starts[i-1])
		}
	}
	for _, at := range records {
		for i := last; i >= 0; i-- {
			if !at.Before(starts[i]) {
				want[i]++
				break
			}
		}
	}
	for i := range starts {
		if counts[i] != want[i] {
			t.Fatalf("%s: bucket %d (%s) has %d requests, want %d", label, i, starts[i], counts[i], want[i])
		}
	}
}

// checkBucketsAt records requests at fixed distances before now and checks
// every range and the hourly account view.
func checkBucketsAt(t *testing.T, now time.Time) {
	t.Helper()
	store := newTestStore(now)
	store.machineName = func(string) string { return "" }
	var records []time.Time
	for _, ago := range []time.Duration{0, time.Minute, 20 * time.Minute, 50 * time.Minute, 70 * time.Minute,
		5*time.Hour + 50*time.Minute, 6*time.Hour + 10*time.Minute, 11*time.Hour + 50*time.Minute,
		12*time.Hour + 10*time.Minute, 25 * time.Hour, 47 * time.Hour, 49 * time.Hour} {
		at := now.Add(-ago)
		records = append(records, at)
		store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: at})
	}
	for _, key := range []string{"24h", "7d", "14d"} {
		window, _ := LookupWindow(key)
		series := store.SummaryFor(10, window).Performance.Scopes["all"].Series
		starts := make([]time.Time, len(series))
		counts := make([]int64, len(series))
		for i, point := range series {
			starts[i], counts[i] = point.Start, point.Requests
		}
		checkSeriesHoldsRecords(t, fmt.Sprintf("%s at %s", key, now), now, starts, counts, records)
		if counts[len(counts)-1] < 1 {
			t.Fatalf("%s at %s: newest bucket has %d requests, want the one at now", key, now, counts[len(counts)-1])
		}
	}
	hourly := store.Summary(10).Accounts["claude-a"].Hourly
	starts := make([]time.Time, len(hourly))
	counts := make([]int64, len(hourly))
	for i, bucket := range hourly {
		starts[i], counts[i] = bucket.Start, bucket.Requests
	}
	checkSeriesHoldsRecords(t, fmt.Sprintf("hourly at %s", now), now, starts, counts, records)
}

// Every bucket boundary comes from the local clock, so the newest bucket holds
// now and every request lands in the bucket that spans it, through DST changes
// of an hour and of half an hour.
func TestBucketsHoldTheirRequestsAcrossDSTChanges(t *testing.T) {
	for _, tc := range []struct {
		zone       string
		transition time.Time
	}{
		{"Europe/London", time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)},
		{"Europe/London", time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)},
		{"America/New_York", time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)},
		{"America/New_York", time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		// Lord Howe moves by half an hour: 02:00 to 02:30 on 4 Oct and 02:00 back
		// to 01:30 on 5 Apr.
		{"Australia/Lord_Howe", time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)},
		{"Australia/Lord_Howe", time.Date(2026, 4, 4, 15, 0, 0, 0, time.UTC)},
		{"Asia/Kolkata", time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.zone+" "+tc.transition.Format("2006-01-02"), func(t *testing.T) {
			loc := mustZone(t, tc.zone)
			for at := tc.transition.Add(-14 * time.Hour); at.Before(tc.transition.Add(14 * time.Hour)); at = at.Add(25 * time.Minute) {
				checkBucketsAt(t, at.In(loc))
			}
		})
	}
}

// The cases the review named: the last hour before the next 6 or 12 hour
// boundary on a day the clocks go back, and the half hour DST change.
func TestNewestBucketHoldsNowOnDSTDays(t *testing.T) {
	london := mustZone(t, "Europe/London")
	newYork := mustZone(t, "America/New_York")
	lordHowe := mustZone(t, "Australia/Lord_Howe")
	for _, tc := range []struct {
		name      string
		window    string
		now       time.Time
		request   time.Time
		wantStart time.Time
	}{
		// 25 Oct 2026: the newest 6h bucket runs from 00:00 BST to 06:00 GMT, 7 hours.
		{"london fall back 7d", "7d", time.Date(2026, 10, 25, 5, 30, 0, 0, london), time.Date(2026, 10, 25, 5, 10, 0, 0, london), time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)},
		{"london fall back 14d", "14d", time.Date(2026, 10, 25, 11, 30, 0, 0, london), time.Date(2026, 10, 25, 11, 10, 0, 0, london), time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)},
		{"london fall back 24h", "24h", time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC).In(london), time.Date(2026, 10, 25, 1, 10, 0, 0, time.UTC), time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)},
		// 29 Mar 2026: the newest 6h bucket runs from 00:00 GMT to 06:00 BST, 5 hours.
		{"london spring forward 7d", "7d", time.Date(2026, 3, 29, 5, 30, 0, 0, london), time.Date(2026, 3, 29, 5, 10, 0, 0, london), time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)},
		{"london spring forward 14d", "14d", time.Date(2026, 3, 29, 11, 30, 0, 0, london), time.Date(2026, 3, 29, 11, 10, 0, 0, london), time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)},
		{"new york fall back 7d", "7d", time.Date(2026, 11, 1, 5, 30, 0, 0, newYork), time.Date(2026, 11, 1, 5, 10, 0, 0, newYork), time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)},
		{"new york fall back 14d", "14d", time.Date(2026, 11, 1, 11, 30, 0, 0, newYork), time.Date(2026, 11, 1, 11, 10, 0, 0, newYork), time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)},
		// 4 Oct 2026 03:15 on Lord Howe: a 01:45 request from before the change
		// belongs to the bucket that starts at 01:00, not one at 00:30.
		{"lord howe half hour 24h", "24h", time.Date(2026, 10, 4, 3, 15, 0, 0, lordHowe), time.Date(2026, 10, 4, 1, 45, 0, 0, lordHowe), time.Date(2026, 10, 3, 14, 30, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(tc.now)
			store.machineName = func(string) string { return "" }
			store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: tc.request})
			window, _ := LookupWindow(tc.window)
			all := store.SummaryFor(10, window).Performance.Scopes["all"]
			if all.Requests != 1 {
				t.Fatalf("total requests = %d, want 1", all.Requests)
			}
			for i, point := range all.Series {
				if point.Requests == 0 {
					continue
				}
				if !point.Start.Equal(tc.wantStart) {
					t.Fatalf("request counted in bucket %d starting %s, want the one starting %s", i, point.Start, tc.wantStart)
				}
				if i+1 < len(all.Series) && !tc.request.Before(all.Series[i+1].Start) {
					t.Fatalf("bucket %d starting %s does not hold %s", i, point.Start, tc.request)
				}
			}
		})
	}
}

// A stats file written when hours were stored on the UTC hour grid holds, in
// a +05:30 zone, hours that start half an hour before a local hour. The one
// that starts before the oldest local hour shown is out of range.
func TestLegacyHourBeforeRangeIsDropped(t *testing.T) {
	india := mustZone(t, "Asia/Kolkata")
	now := time.Date(2026, 10, 4, 10, 15, 0, 0, india)
	firstHour := time.Date(2026, 10, 2, 11, 0, 0, 0, india)
	before := firstHour.Add(-30 * time.Minute).Unix()
	inside := firstHour.Add(30 * time.Minute).Unix()
	data := fmt.Sprintf(`{"version":2,"hourly":{"claude-a":{"%d":{"requests":5},"%d":{"requests":1}}},"daily":{},"sessions":{}}`, before, inside)
	path := filepath.Join(t.TempDir(), "usage-stats.json")
	if errWrite := os.WriteFile(path, []byte(data), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := newTestStore(now)
	store.machineName = func(string) string { return "" }
	store.path = path
	store.loadLocked()

	hourly := store.Summary(10).Accounts["claude-a"].Hourly
	if !hourly[0].Start.Equal(firstHour) || hourly[0].Requests != 1 {
		t.Fatalf("oldest hour = %+v, want %s with only the in-range request", hourly[0], firstHour)
	}
	var total int64
	for _, bucket := range hourly {
		total += bucket.Requests
	}
	if total != 1 {
		t.Fatalf("hourly total = %d, want 1", total)
	}
}

// A damaged stats file can hold histogram indexes outside the histogram. They
// would become infinite bounds and break the dashboard JSON.
func TestLoadDropsInvalidHistogramEntries(t *testing.T) {
	now := time.Date(2026, 10, 4, 16, 0, 0, 0, time.UTC)
	hour := now.Add(-time.Hour).Unix()
	stamp := now.Add(-time.Minute).Format(time.RFC3339)
	data := fmt.Sprintf(`{"version":2,"hourly":{},"daily":{},`+
		`"sessions":{"claude:s1":{"ttft_hist":{"999":1,"-1":4,"20":1},"first_seen":%q,"last_seen":%q}},`+
		`"perf":{"claude-a":{"%d":{"requests":3,"ttft":{"-4":1,"20":2},"latency":{"10000":1,"21":0},"throughput":{"10000":1,"30":2,"31":-5}}}}}`,
		stamp, stamp, hour)
	path := filepath.Join(t.TempDir(), "usage-stats.json")
	if errWrite := os.WriteFile(path, []byte(data), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := newTestStore(now)
	store.machineName = func(string) string { return "" }
	store.path = path
	store.loadLocked()

	summary := store.Summary(10)
	if _, errMarshal := json.Marshal(summary); errMarshal != nil {
		t.Fatalf("summary does not serialise: %v", errMarshal)
	}
	all := summary.Performance.Scopes["all"]
	if len(all.ThroughputHist) != 1 || all.ThroughputHist[0].Count != 2 {
		t.Fatalf("throughput hist = %+v, want only index 30 with 2", all.ThroughputHist)
	}
	if len(all.TTFTHist) != 1 || all.TTFTHist[0].Count != 2 || all.Latency.P50 != 0 {
		t.Fatalf("ttft hist = %+v latency %+v, want index 20 with 2 and no latency", all.TTFTHist, all.Latency)
	}
	if got := sessionsByID(summary)["claude:s1"].TTFTP50; got != roundMillis(histMid(20, ttftBase)) {
		t.Fatalf("session ttft p50 = %d, want the index 20 value", got)
	}
}

// On 1 Nov 2026 Havana reaches local midnight twice, at 04:00 UTC and again at
// 05:00 UTC. Today starts at the first, so it keeps the requests from both
// midnight hours, while the hourly view still shows them as separate hours.
func TestTodayCountsBothRepeatedMidnightHours(t *testing.T) {
	havana := mustZone(t, "America/Havana")
	firstMidnight := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	secondMidnight := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	for _, now := range []time.Time{
		secondMidnight.Add(30 * time.Minute).In(havana),
		time.Date(2026, 11, 1, 18, 0, 0, 0, havana),
	} {
		store := newTestStore(now)
		store.machineName = func(string) string { return "" }
		for _, at := range []time.Time{
			firstMidnight.Add(-10 * time.Minute), // 23:50 on 31 Oct
			firstMidnight.Add(10 * time.Minute),
			secondMidnight.Add(10 * time.Minute),
		} {
			store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: at})
		}
		summary := store.Summary(10)
		account := summary.Accounts["claude-a"]
		if account.Today.Requests != 2 || summary.Totals["today"].Requests != 2 {
			t.Fatalf("at %s: today = %d, totals today = %d, want 2 from both midnight hours",
				now, account.Today.Requests, summary.Totals["today"].Requests)
		}
		var midnightHours int
		for _, bucket := range account.Hourly {
			if bucket.Start.Equal(firstMidnight) || bucket.Start.Equal(secondMidnight) {
				if bucket.Requests != 1 {
					t.Fatalf("at %s: hour %s has %d requests, want 1", now, bucket.Start, bucket.Requests)
				}
				midnightHours++
			}
		}
		if midnightHours != 2 {
			t.Fatalf("at %s: found %d midnight hours in the hourly view, want 2", now, midnightHours)
		}
	}
}
