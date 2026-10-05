package usagestats

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// sample is one upstream attempt a test records, kept to check sums against.
type sample struct {
	auth     string
	at       time.Time
	counters Counters
}

func recordSample(store *Store, s sample) {
	store.record(coreusage.Record{
		AuthID: s.auth, Provider: providerOf("", s.auth), RequestedAt: s.at, Failed: s.counters.Failed > 0,
		Detail: coreusage.Detail{InputTokens: s.counters.Input, OutputTokens: s.counters.Output,
			CacheReadTokens: s.counters.CacheRead, CacheCreationTokens: s.counters.CacheWrite},
	})
}

// sumSamples adds the samples of auth ("" for every credential) that fall in
// [from, to).
func sumSamples(samples []sample, auth string, from, to time.Time) Counters {
	var total Counters
	for _, s := range samples {
		if (auth == "" || s.auth == auth) && !s.at.Before(from) && s.at.Before(to) {
			total.add(s.counters)
		}
	}
	return total
}

// seriesTotals adds up a performance series as usage counters.
func seriesTotals(series []PerfPoint) Counters {
	var total Counters
	for _, point := range series {
		total.add(Counters{Requests: point.Requests, Failed: point.Failed, Input: point.Input, Output: point.Output,
			CacheRead: point.CacheRead, CacheWrite: point.CacheWrite})
	}
	return total
}

func usageTotals(series []Counters) Counters {
	var total Counters
	for _, counters := range series {
		total.add(counters)
	}
	return total
}

func seriesStarts(series []PerfPoint) []time.Time {
	starts := make([]time.Time, len(series))
	for i, point := range series {
		starts[i] = point.Start
	}
	return starts
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	return string(data)
}

func reloadStore(t *testing.T, store *Store, clock *testClock) *Store {
	t.Helper()
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	reloaded := newClockStore(clock)
	reloaded.path = store.path
	reloaded.loadLocked()
	return reloaded
}

func TestResolveWindowFallsBackTo24h(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"", "24h"}, {"bogus", "24h"}, {"90d", "24h"},
		{"24h", "24h"}, {"7d", "7d"}, {"14d", "14d"}, {"30d", "30d"}, {"180d", "180d"}, {"all", "all"},
	} {
		if got := ResolveWindow(tc.key).Key; got != tc.want {
			t.Errorf("ResolveWindow(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// checkDayBounds checks that bounds are local day starts size dates apart,
// that the newest run ends with today and that the last bound is tomorrow.
func checkDayBounds(t *testing.T, label string, now time.Time, bounds []time.Time, size int) {
	t.Helper()
	loc := now.Location()
	count := len(bounds) - 1
	for i, bound := range bounds {
		if !isBucketStart(bound, loc, 24) || bound.In(loc).Format(dayLayout) == bound.Add(-time.Minute).In(loc).Format(dayLayout) {
			t.Fatalf("%s: bound %d (%s) is not the start of a local day", label, i, bound)
		}
		if i > 0 {
			if days := localDaysBetween(bounds[i-1], bound); days != size {
				t.Fatalf("%s: bucket %d spans %d local days (%s to %s), want %d", label, i-1, days, bounds[i-1], bound, size)
			}
		}
	}
	if now.Before(bounds[count-1]) || !now.Before(bounds[count]) {
		t.Fatalf("%s: newest bucket %s to %s does not hold now %s", label, bounds[count-1], bounds[count], now)
	}
	if localDaysBetween(now, bounds[count]) != 1 {
		t.Fatalf("%s: last bound %s is not the start of tomorrow", label, bounds[count])
	}
}

// The day windows follow the local calendar through DST changes of an hour
// and half an hour, a repeated midnight (Havana) and a skipped one (Santiago).
func TestDayWindowBoundsAcrossDSTChanges(t *testing.T) {
	for _, tc := range []struct {
		zone       string
		transition time.Time
	}{
		{"Europe/London", time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)},
		{"Europe/London", time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)},
		{"America/New_York", time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)},
		{"America/New_York", time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		{"Australia/Lord_Howe", time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)},
		{"Australia/Lord_Howe", time.Date(2026, 4, 4, 15, 0, 0, 0, time.UTC)},
		{"America/Havana", time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)},
		{"America/Santiago", time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)},
		{"Asia/Kolkata", time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.zone+" "+tc.transition.Format(dayLayout), func(t *testing.T) {
			loc := mustZone(t, tc.zone)
			for at := tc.transition.Add(-50 * time.Hour); at.Before(tc.transition.Add(50 * time.Hour)); at = at.Add(7*time.Hour + 15*time.Minute) {
				now := at.In(loc)
				for _, key := range []string{"30d", "180d"} {
					window := windows[key]
					bounds, bucket := window.span(now, time.Time{})
					label := fmt.Sprintf("%s at %s", key, now)
					if len(bounds) != window.Buckets+1 || bucket != localDay {
						t.Fatalf("%s: %d bounds, bucket %s", label, len(bounds), bucket)
					}
					checkDayBounds(t, label, now, bounds, 1)
					if want := localBounds(now, 24, window.Buckets); !reflect.DeepEqual(bounds, want) {
						t.Fatalf("%s: day bounds differ from the local day walk", label)
					}
				}
				for _, tc := range []struct {
					firstAgo, size, buckets int
				}{
					{0, 1, 1}, {59, 1, 60}, {119, 1, 120}, {120, 7, 18}, {399, 7, 58},
				} {
					first := time.Date(now.Year(), now.Month(), now.Day()-tc.firstAgo, 15, 0, 0, 0, loc)
					bounds, bucket := windows["all"].span(now, first)
					label := fmt.Sprintf("all from %d days ago at %s", tc.firstAgo, now)
					if len(bounds) != tc.buckets+1 || bucket != time.Duration(tc.size)*localDay {
						t.Fatalf("%s: %d buckets of %s, want %d of %d days", label, len(bounds)-1, bucket, tc.buckets, tc.size)
					}
					checkDayBounds(t, label, now, bounds, tc.size)
					if first.Before(bounds[0]) || localDaysBetween(bounds[0], first) >= tc.size {
						t.Fatalf("%s: first bucket starts %s, want the run that holds %s", label, bounds[0], first)
					}
				}
			}

			// Requests land in the bucket of their local day, both while their
			// hours are kept and after they are rolled up into days.
			transition := tc.transition.In(loc)
			for _, now := range []time.Time{transition.Add(34 * time.Hour), transition.AddDate(0, 0, 40)} {
				for _, firstAgo := range []int{60, 400} {
					clock := &testClock{now: now}
					store := newClockStore(clock)
					var records []time.Time
					add := func(at time.Time) {
						if at.After(now) {
							return
						}
						records = append(records, at)
						recordSample(store, sample{"claude-a", at, Counters{Requests: 1}})
					}
					add(time.Date(now.Year(), now.Month(), now.Day()-firstAgo, 13, 0, 0, 0, loc))
					for _, offset := range []time.Duration{-25 * time.Hour, -23 * time.Hour, -3 * time.Hour, -70 * time.Minute,
						-10 * time.Minute, 10 * time.Minute, 50 * time.Minute, 70 * time.Minute, 3 * time.Hour, 23 * time.Hour, 25 * time.Hour} {
						add(tc.transition.Add(offset))
					}
					local := tc.transition.In(loc)
					for day := -2; day <= 2; day++ {
						start := localDayStart(local.Year(), local.Month(), local.Day()+day, loc)
						add(start.Add(-time.Minute))
						add(start)
						add(start.Add(time.Minute))
					}
					add(now)
					for _, key := range []string{"30d", "180d", "all"} {
						summary := store.SummaryFor(10, windows[key])
						series := summary.Performance.Scopes["all"].Series
						counts := make([]int64, len(series))
						for i, point := range series {
							counts[i] = point.Requests
						}
						label := fmt.Sprintf("%s perf at %s from %d days", key, now, firstAgo)
						checkSeriesHoldsRecords(t, label, now, seriesStarts(series), counts, records)

						usage := summary.UsageRange
						if !reflect.DeepEqual(usage.Starts, seriesStarts(series)) {
							t.Fatalf("%s: usage starts differ from the performance series", label)
						}
						counts = make([]int64, len(usage.Starts))
						for i, counters := range usage.Accounts["claude-a"] {
							counts[i] = counters.Requests
						}
						checkSeriesHoldsRecords(t, strings.Replace(label, "perf", "usage", 1), now, usage.Starts, counts, records)
					}
				}
			}
		})
	}
}

// Fixed cases with the bucket that crosses the change named.
func TestDayBucketLengthsOnDSTDays(t *testing.T) {
	london := mustZone(t, "Europe/London")
	lordHowe := mustZone(t, "Australia/Lord_Howe")
	santiago := mustZone(t, "America/Santiago")
	havana := mustZone(t, "America/Havana")
	for _, tc := range []struct {
		name       string
		key        string
		now        time.Time
		first      time.Time
		start, end time.Time
	}{
		// 25 Oct 2026 has 25 hours in London.
		{"london fall back 30d", "30d", time.Date(2026, 10, 25, 15, 30, 0, 0, london), time.Time{},
			time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC), time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)},
		// 29 Mar 2026 has 23 hours.
		{"london spring forward 180d", "180d", time.Date(2026, 3, 29, 15, 30, 0, 0, london), time.Time{},
			time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 29, 23, 0, 0, 0, time.UTC)},
		// 4 Oct 2026 on Lord Howe has 23 and a half hours.
		{"lord howe half hour 30d", "30d", time.Date(2026, 10, 4, 15, 0, 0, 0, lordHowe), time.Time{},
			time.Date(2026, 10, 3, 13, 30, 0, 0, time.UTC), time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC)},
		// 6 Sep 2026 in Santiago starts at 01:00, as midnight is skipped.
		{"santiago skipped midnight all", "all", time.Date(2026, 9, 6, 15, 0, 0, 0, santiago), time.Date(2026, 9, 1, 12, 0, 0, 0, santiago),
			time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC), time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)},
		// 1 Nov 2026 in Havana starts at the first of its two midnights.
		{"havana repeated midnight 30d", "30d", time.Date(2026, 11, 1, 15, 0, 0, 0, havana), time.Time{},
			time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)},
		// A 7 day run that ends with today, 2 Nov 2026 in London, and holds the
		// 25 hour 25 Oct: 27 Oct to 2 Nov is all GMT, 20 Oct to 26 Oct is not.
		{"london weekly all", "all", time.Date(2026, 11, 2, 12, 0, 0, 0, london), time.Date(2026, 3, 1, 12, 0, 0, 0, london),
			time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bounds, _ := windows[tc.key].span(tc.now, tc.first)
			index, ok := boundsIndex(bounds, tc.start)
			if !ok || !bounds[index].Equal(tc.start) || !bounds[index+1].Equal(tc.end) {
				t.Fatalf("bucket holding %s runs %v, want %s to %s", tc.start, bounds[max(index, 0):min(index+2, len(bounds))], tc.start, tc.end)
			}
		})
	}
	bounds, _ := windows["all"].span(time.Date(2026, 11, 2, 12, 0, 0, 0, london), time.Date(2026, 3, 1, 12, 0, 0, 0, london))
	if previous := bounds[len(bounds)-3]; !previous.Equal(time.Date(2026, 10, 19, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("run before the newest starts %s, want 20 Oct 00:00 BST", previous)
	}
}

// Hours older than 35 days move into one bucket per credential and local day.
// No window loses or double counts a request, before or after a reload.
func TestPerfRollupKeepsTotals(t *testing.T) {
	london := mustZone(t, "Europe/London")
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, london)}
	store := newClockStore(clock)
	store.path = filepath.Join(t.TempDir(), "usage-stats.json")
	auths := []string{"claude-a", "codex-b", "claude-c"}
	var samples []sample
	for day := 0; day <= 70; day++ {
		for i, hour := range []int{0, 13, 23} {
			at := time.Date(2026, 10, 5-day, hour, 20, 0, 0, london)
			if at.After(clock.now) {
				continue
			}
			counters := Counters{Requests: 1, Input: int64(10 + day), Output: int64(100 + i), CacheRead: int64(1000 * i), CacheWrite: int64(day)}
			if (day+i)%7 == 0 {
				counters.Failed = 1
			}
			samples = append(samples, sample{auths[(day+i)%3], at, counters})
		}
	}
	// Hours on both sides of the cutoff, on the same local day.
	cutoff := clock.now.Add(-perfRetention)
	for _, offset := range []time.Duration{-90 * time.Minute, -10 * time.Minute, 10 * time.Minute, 90 * time.Minute} {
		samples = append(samples, sample{"claude-a", cutoff.Add(offset), Counters{Requests: 1, Output: 7}})
	}
	for _, s := range samples {
		recordSample(store, s)
	}

	end := clock.now.Add(time.Nanosecond)
	check := func(t *testing.T, store *Store) map[string]string {
		t.Helper()
		views := make(map[string]string)
		for _, key := range []string{"24h", "7d", "30d", "180d", "all"} {
			summary := store.SummaryFor(10, windows[key])
			perf := summary.Performance
			from := perf.Scopes["all"].Series[0].Start
			for _, scope := range append([]string{"all", "claude", "codex"}, auths...) {
				auth := scope
				if scope == "all" || scope == "claude" || scope == "codex" {
					auth = ""
				}
				want := sumSamples(samples, auth, from, end)
				if scope == "claude" || scope == "codex" {
					want = Counters{}
					for _, s := range samples {
						if providerOf("", s.auth) == scope && !s.at.Before(from) {
							want.add(s.counters)
						}
					}
				}
				got := perf.Scopes[scope]
				if seriesTotals(got.Series) != want || got.Requests != want.Requests || got.Failed != want.Failed {
					t.Fatalf("%s/%s: series totals %+v (scope %d requests %d failed), want %+v", key, scope, seriesTotals(got.Series), got.Requests, got.Failed, want)
				}
			}
			for _, auth := range auths {
				if got, want := usageTotals(summary.UsageRange.Accounts[auth]), sumSamples(samples, auth, from, end); got != want {
					t.Fatalf("%s: usage of %s = %+v, want %+v", key, auth, got, want)
				}
			}
			views[key] = mustJSON(t, perf) + mustJSON(t, summary.UsageRange)
		}
		if got, want := len(store.SummaryFor(10, windows["all"]).Performance.Scopes["all"].Series), 71; got != want {
			t.Fatalf("all window has %d daily buckets, want %d", got, want)
		}
		return views
	}
	first := check(t, store)

	wantDays := make(map[string]map[string]bool)
	for _, s := range samples {
		hour := bucketStart(s.at, london, 1)
		if !hour.Before(cutoff) {
			continue
		}
		if wantDays[s.auth] == nil {
			wantDays[s.auth] = make(map[string]bool)
		}
		wantDays[s.auth][hour.In(london).Format(dayLayout)] = true
	}
	for _, auth := range auths {
		for hourUnix := range store.perf[auth] {
			if hourUnix < cutoff.Unix() {
				t.Fatalf("%s keeps the hour %s, older than 35 days", auth, time.Unix(hourUnix, 0))
			}
		}
		if got := len(store.perfDaily[auth]); got != len(wantDays[auth]) {
			t.Fatalf("%s has %d rolled up days, want one per local day: %d", auth, got, len(wantDays[auth]))
		}
	}
	if store.perfDaily["claude-a"]["2026-08-31"] == nil || len(store.perf["claude-a"]) == 0 {
		t.Fatal("the cutoff day should be split between a rolled up day and kept hours")
	}

	if again := check(t, store); !reflect.DeepEqual(again, first) {
		t.Fatal("a second summary changed the views")
	}
	reloaded := reloadStore(t, store, clock)
	if got := check(t, reloaded); !reflect.DeepEqual(got, first) {
		t.Fatal("the views changed after a save and reload")
	}
	// Saving the reloaded store again must not count anything twice.
	reloaded.dirty = true
	if got := check(t, reloadStore(t, reloaded, clock)); !reflect.DeepEqual(got, first) {
		t.Fatal("the views changed after a second save and reload")
	}
}

// Stats files written before version 3 still load: the per-credential daily
// tally is rebuilt from the hourly buckets, timing buckets get their token
// sums from the hourly bucket with the same key, and a save and reload keeps
// every total.
func TestOldStatsFilesLoad(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 5, 15, 30, 0, 0, london)
	hour := time.Date(2026, 10, 4, 18, 0, 0, 0, london).Unix()
	today := time.Date(2026, 10, 5, 9, 0, 0, 0, london).Unix()
	oldPerf := time.Date(2026, 8, 20, 10, 0, 0, 0, london).Unix()
	h := strconv.FormatInt(hour, 10)
	t2 := strconv.FormatInt(today, 10)
	for _, tc := range []struct {
		name  string
		data  string
		check func(t *testing.T, summary Summary)
	}{
		{
			name: "version 1, hourly only",
			data: `{"version":1,"hourly":{"claude-x.json":{"` + h + `":{"requests":3,"output_tokens":30}}},"sessions":{}}`,
			check: func(t *testing.T, summary Summary) {
				daily := summary.Accounts["claude-x.json"].Daily
				if got := daily[12]; got.Date != "2026-10-04" || got.Requests != 3 || got.Output != 30 {
					t.Fatalf("yesterday = %+v, want the 3 hourly requests", got)
				}
				if got := usageTotals(summary.UsageRange.Accounts["claude-x.json"]); got.Requests != 3 || got.Output != 30 {
					t.Fatalf("usage range = %+v", got)
				}
				if summary.History.Lifetime.Requests != 3 {
					t.Fatalf("lifetime = %+v", summary.History.Lifetime)
				}
			},
		},
		{
			name: "version 2, timing without tokens",
			data: `{"version":2,` +
				`"hourly":{"claude-a":{"` + h + `":{"requests":2,"failed":1,"input_tokens":5,"output_tokens":40,"cache_read_tokens":900,"cache_write_tokens":3},` +
				`"` + t2 + `":{"requests":1,"output_tokens":8}}},` +
				`"daily":{"2026-10-04":{"claude":{"requests":2,"failed":1,"input_tokens":5,"output_tokens":40,"cache_read_tokens":900,"cache_write_tokens":3}},` +
				`"2026-10-05":{"claude":{"requests":1,"output_tokens":8}},"2026-08-20":{"claude":{"requests":4}}},` +
				`"sessions":{},` +
				`"perf":{"claude-a":{"` + h + `":{"requests":2,"failed":1,"ttft":{"20":1},"latency":{"30":1}},` +
				`"` + t2 + `":{"requests":1,"ttft":{"25":1}},` +
				`"` + strconv.FormatInt(oldPerf, 10) + `":{"requests":4,"ttft":{"22":4}}}},` +
				`"auth_providers":{"claude-a":"claude"}}`,
			check: func(t *testing.T, summary Summary) {
				daily := summary.Accounts["claude-a"].Daily
				if daily[12].Requests != 2 || daily[12].CacheRead != 900 || daily[13].Requests != 1 {
					t.Fatalf("daily = %+v / %+v, want the hourly buckets per day", daily[12], daily[13])
				}
				series := summary.Performance.Scopes["claude-a"].Series
				got := seriesTotals(series)
				// The 20 Aug hour is older than 35 days and has no hourly usage
				// bucket, so it adds requests but no tokens.
				if got.Requests != 7 || got.Failed != 1 || got.Input != 5 || got.Output != 48 || got.CacheRead != 900 || got.CacheWrite != 3 {
					t.Fatalf("all window timing totals = %+v", got)
				}
				if summary.Performance.Since == nil || !summary.Performance.Since.Equal(time.Date(2026, 8, 20, 0, 0, 0, 0, london)) {
					t.Fatalf("since = %v, want the rolled up 20 Aug", summary.Performance.Since)
				}
				if got := usageTotals(summary.UsageRange.Accounts["claude-a"]); got.Requests != 3 || got.Output != 48 {
					t.Fatalf("usage range = %+v, want the hourly buckets", got)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &testClock{now: now}
			path := filepath.Join(t.TempDir(), "usage-stats.json")
			if errWrite := os.WriteFile(path, []byte(tc.data), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			store := newClockStore(clock)
			store.path = path
			store.loadLocked()
			summary := store.SummaryFor(10, windows["all"])
			tc.check(t, summary)
			view := func(summary Summary) string {
				return mustJSON(t, summary.Accounts) + mustJSON(t, summary.History) + mustJSON(t, summary.Performance) + mustJSON(t, summary.UsageRange)
			}
			want := view(summary)

			reloaded := reloadStore(t, store, clock)
			if got := reloaded.SummaryFor(10, windows["all"]); view(got) != want {
				t.Fatalf("after reload:\n%s\nwant\n%s", view(got), want)
			}
			var saved fileState
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if errUnmarshal := json.Unmarshal(data, &saved); errUnmarshal != nil || saved.Version != statsFileVersion || saved.AccountDaily == nil {
				t.Fatalf("saved file version %d, account daily %v, err %v", saved.Version, saved.AccountDaily != nil, errUnmarshal)
			}
			reloaded.dirty = true
			if got := reloadStore(t, reloaded, clock).SummaryFor(10, windows["all"]); view(got) != want {
				t.Fatal("a second save and reload changed the totals")
			}
		})
	}
}

// 180d is offered once the earliest data is 180 local calendar days old.
func TestRangesOffer180dFromSixMonthsOfData(t *testing.T) {
	london := mustZone(t, "Europe/London")
	without := []string{"24h", "7d", "30d", "all"}
	with := []string{"24h", "7d", "30d", "180d", "all"}
	for _, now := range []time.Time{time.Date(2026, 10, 5, 15, 30, 0, 0, london), time.Date(2026, 10, 5, 0, 10, 0, 0, london)} {
		for _, tc := range []struct {
			name     string
			ago      int // days before today of the earliest request, or -1 for none
			backfill int // days before today of a backfill day, or -1 for none
			perf     []string
			usage    []string
		}{
			{"no data", -1, -1, without, without},
			{"today only", 0, -1, without, without},
			{"179 days", 179, -1, without, without},
			{"180 days", 180, -1, with, with},
			{"181 days", 181, -1, with, with},
			{"backfill 200 days", 2, 200, without, with},
			{"backfill 179 days", 2, 179, without, without},
		} {
			t.Run(fmt.Sprintf("%s at %s", tc.name, now.Format("15:04")), func(t *testing.T) {
				clock := &testClock{now: now}
				store := newClockStore(clock)
				var earliest time.Time
				if tc.ago >= 0 {
					at := time.Date(now.Year(), now.Month(), now.Day()-tc.ago, 10, 0, 0, 0, london)
					if at.After(now) {
						at = now
					}
					recordSample(store, sample{"claude-a", at, Counters{Requests: 1}})
					recordSample(store, sample{"claude-a", now, Counters{Requests: 1}})
					earliest = localDayStart(at.Year(), at.Month(), at.Day(), london)
					if tc.ago <= 35 {
						earliest = bucketStart(at, london, 1)
					}
				}
				if tc.backfill >= 0 {
					date := time.Date(now.Year(), now.Month(), now.Day()-tc.backfill, 12, 0, 0, 0, london).Format(dayLayout)
					store.backfill = &historyFile{Days: map[string]map[string]Counters{date: {"claude": {Requests: 5}}}}
				}
				for _, key := range []string{"24h", "30d", "all"} {
					summary := store.SummaryFor(10, windows[key])
					if !reflect.DeepEqual(summary.Performance.Ranges, tc.perf) {
						t.Fatalf("%s: performance ranges = %v, want %v", key, summary.Performance.Ranges, tc.perf)
					}
					if !reflect.DeepEqual(summary.UsageRange.Ranges, tc.usage) {
						t.Fatalf("%s: usage ranges = %v, want %v", key, summary.UsageRange.Ranges, tc.usage)
					}
					since := summary.Performance.Since
					if earliest.IsZero() != (since == nil) || (since != nil && !since.Equal(earliest)) {
						t.Fatalf("%s: since = %v, want %v", key, since, earliest)
					}
				}
				payload := mustJSON(t, store.SummaryFor(10, windows["24h"]).Performance)
				if strings.Contains(payload, `"since"`) != !earliest.IsZero() {
					t.Fatalf("since in payload = %v, want %v", strings.Contains(payload, `"since"`), !earliest.IsZero())
				}
			})
		}
	}
}

// Every series point carries full response time, slow throughput, failovers
// and token sums, in every scope.
func TestPerfPointNewFields(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 5, 15, 40, 0, 0, london)
	store := newTestStore(now)
	store.machineName = func(string) string { return "" }
	// Inbound request t1 failed on claude-a and moved to claude-b.
	store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", TraceID: "t1", RequestedAt: now, Failed: true,
		Detail: coreusage.Detail{InputTokens: 3}})
	// Ten streamed replies on claude-b: full response 1 s to 10 s, first token
	// at 500 ms, throughput 100 to 1000 tokens per second.
	var input, output, cacheRead, cacheWrite int64 = 3, 0, 0, 0
	for i := 1; i <= 10; i++ {
		latency := time.Duration(i) * time.Second
		generation := latency - 500*time.Millisecond
		tokens := int64(math.Round(float64(100*i) * generation.Seconds()))
		trace := ""
		if i == 1 {
			trace = "t1"
		}
		store.record(coreusage.Record{AuthID: "claude-b", Provider: "claude", TraceID: trace, RequestedAt: now.Add(-time.Duration(i) * time.Minute),
			UpstreamStream: true, TTFT: 500 * time.Millisecond, Latency: latency,
			Detail: coreusage.Detail{InputTokens: int64(10 * i), OutputTokens: tokens, CacheReadTokens: 1000, CacheCreationTokens: int64(i)}})
		input += int64(10 * i)
		output += tokens
		cacheRead += 1000
		cacheWrite += int64(i)
	}
	near := func(got, want float64) bool { return math.Abs(got-want) <= want*0.06 }
	for _, key := range []string{"24h", "7d", "30d", "all"} {
		perf := store.SummaryFor(10, windows[key]).Performance
		for _, scope := range []string{"all", "claude", "claude-b"} {
			series := perf.Scopes[scope].Series
			point := series[len(series)-1]
			label := fmt.Sprintf("%s/%s", key, scope)
			if !near(float64(point.LatencyP50), 5000) || !near(float64(point.LatencyP90), 9000) {
				t.Fatalf("%s: latency p50 %d p90 %d, want about 5000 and 9000", label, point.LatencyP50, point.LatencyP90)
			}
			if !near(point.ThroughputP10, 100) || !near(point.ThroughputP50, 500) {
				t.Fatalf("%s: throughput p10 %v p50 %v, want about 100 and 500", label, point.ThroughputP10, point.ThroughputP50)
			}
			if point.Failovers != 1 {
				t.Fatalf("%s: failovers = %d, want 1", label, point.Failovers)
			}
			wantInput := input
			if scope == "claude-b" {
				wantInput -= 3
			}
			if point.Input != wantInput || point.Output != output || point.CacheRead != cacheRead || point.CacheWrite != cacheWrite {
				t.Fatalf("%s: tokens %d/%d/%d/%d, want %d/%d/%d/%d", label, point.Input, point.Output, point.CacheRead, point.CacheWrite,
					wantInput, output, cacheRead, cacheWrite)
			}
		}
		if got := perf.Scopes["claude-a"].Series; got[len(got)-1].Failovers != 0 || got[len(got)-1].Input != 3 || got[len(got)-1].Failed != 1 {
			t.Fatalf("%s/claude-a: newest point %+v, want the failed try with 3 input tokens", key, got[len(got)-1])
		}
	}
	var fields map[string]any
	series := store.SummaryFor(10, windows["24h"]).Performance.Scopes["all"].Series
	if errUnmarshal := json.Unmarshal([]byte(mustJSON(t, series[0])), &fields); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	for _, name := range []string{"start", "requests", "failed", "ttft_p50_ms", "ttft_p90_ms", "throughput_p50", "latency_p50_ms", "latency_p90_ms",
		"throughput_p10", "failovers", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("series point JSON lacks %q: %v", name, fields)
		}
	}
}

// usage_range uses the performance buckets, and its sums match the
// per-credential totals.
func TestUsageRangeSumsMatchAccountTotals(t *testing.T) {
	london := mustZone(t, "Europe/London")
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, london)}
	store := newClockStore(clock)
	var samples []sample
	auths := []string{"claude-a", "claude-b", "codex-c"}
	for i := 0; i < 40*24; i += 5 {
		at := clock.now.Add(-time.Duration(i)*time.Hour - 7*time.Minute)
		auth := auths[i%len(auths)]
		counters := Counters{Requests: 1, Input: int64(i), Output: 2 * int64(i), CacheRead: 100, CacheWrite: int64(i % 7)}
		if i%11 == 0 {
			counters.Failed = 1
		}
		samples = append(samples, sample{auth, at, counters})
	}
	// An account whose only use is 20 days old.
	samples = append(samples, sample{"codex-old", clock.now.AddDate(0, 0, -20), Counters{Requests: 1, Output: 9}})
	for _, s := range samples {
		recordSample(store, s)
	}
	end := clock.now.Add(time.Nanosecond)
	for _, tc := range []struct {
		key     string
		buckets int
		seconds int64
		hasOld  bool
	}{
		{"24h", 24, 3600, false},
		{"7d", 28, 21600, false},
		{"14d", 28, 43200, false},
		{"30d", 30, 86400, true},
		{"180d", 180, 86400, true},
		{"all", 41, 86400, true},
	} {
		summary := store.SummaryFor(10, windows[tc.key])
		usage := summary.UsageRange
		if usage.Range != tc.key || usage.BucketSeconds != tc.seconds || len(usage.Starts) != tc.buckets {
			t.Fatalf("%s: range %q, bucket %d, %d starts", tc.key, usage.Range, usage.BucketSeconds, len(usage.Starts))
		}
		if !reflect.DeepEqual(usage.Starts, seriesStarts(summary.Performance.Scopes["all"].Series)) {
			t.Fatalf("%s: usage starts differ from the performance series", tc.key)
		}
		if _, ok := usage.Accounts["codex-old"]; ok != tc.hasOld {
			t.Fatalf("%s: account with no usage in range listed = %v, want %v", tc.key, ok, tc.hasOld)
		}
		var all Counters
		for _, auth := range append(auths, "codex-old") {
			got := usageTotals(usage.Accounts[auth])
			if want := sumSamples(samples, auth, usage.Starts[0], end); got != want {
				t.Fatalf("%s: %s sums to %+v, want %+v", tc.key, auth, got, want)
			}
			if len(usage.Accounts[auth]) != 0 && len(usage.Accounts[auth]) != tc.buckets {
				t.Fatalf("%s: %s has %d buckets, want %d", tc.key, auth, len(usage.Accounts[auth]), tc.buckets)
			}
			all.add(got)
			account := summary.Accounts[auth]
			switch tc.key {
			case "24h":
				if got != account.Last24h {
					t.Fatalf("24h: %s sums to %+v, last_24h is %+v", auth, got, account.Last24h)
				}
			case "30d":
				// The last 14 days line up with the account's daily view.
				for i, day := range account.Daily {
					if bucket := usage.Accounts[auth][16+i]; bucket != day.Counters || usage.Starts[16+i].Format(dayLayout) != day.Date {
						t.Fatalf("30d: %s on %s = %+v, daily view has %+v", auth, day.Date, bucket, day)
					}
				}
			}
		}
		if tc.key == "all" && all != summary.History.Lifetime {
			t.Fatalf("all: usage sums to %+v, lifetime is %+v", all, summary.History.Lifetime)
		}
	}

	// Failed tries count as usage, but only accounts that answered serve a
	// session.
	store.record(coreusage.Record{AuthID: "claude-z", SessionID: "s1", RequestedAt: clock.now, Failed: true})
	store.record(coreusage.Record{AuthID: "claude-a", SessionID: "s1", RequestedAt: clock.now})
	summary := store.Summary(10)
	if got := sessionsByID(summary)["s1"].AuthIDs; len(got) != 1 || got[0] != "claude-a" {
		t.Fatalf("session auth ids = %v, want only the account that answered", got)
	}
	if got := usageTotals(summary.UsageRange.Accounts["claude-z"]); got.Requests != 1 || got.Failed != 1 {
		t.Fatalf("failed account usage = %+v, want the failed try", got)
	}
}

// The all window switches to 7 day buckets after 120 days, so a year of data
// stays small.
func TestAllWindowStaysBounded(t *testing.T) {
	london := mustZone(t, "Europe/London")
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, london)}
	for _, tc := range []struct {
		days, buckets int
		seconds       int64
	}{
		{1, 1, 86400}, {120, 120, 86400}, {121, 18, 604800}, {366, 53, 604800}, {3 * 365, 157, 604800},
	} {
		t.Run(strconv.Itoa(tc.days)+" days", func(t *testing.T) {
			store := newClockStore(clock)
			for day := 0; day < tc.days; day++ {
				for _, auth := range []string{"claude-a", "claude-b", "codex-c"} {
					at := time.Date(2026, 10, 5-day, 9+day%6, 0, 0, 0, london)
					store.record(coreusage.Record{AuthID: auth, Provider: providerOf("", auth), RequestedAt: at, UpstreamStream: true,
						TTFT: time.Duration(300+day%900) * time.Millisecond, Latency: time.Duration(2+day%30) * time.Second,
						Detail: coreusage.Detail{InputTokens: 100, OutputTokens: int64(200 + day), CacheReadTokens: 5000}})
				}
			}
			summary := store.SummaryFor(100, windows["all"])
			perf := summary.Performance
			if perf.BucketSeconds != tc.seconds || summary.UsageRange.BucketSeconds != tc.seconds {
				t.Fatalf("bucket seconds = %d / %d, want %d", perf.BucketSeconds, summary.UsageRange.BucketSeconds, tc.seconds)
			}
			for scope, series := range perf.Scopes {
				if len(series.Series) != tc.buckets {
					t.Fatalf("scope %s has %d points, want %d", scope, len(series.Series), tc.buckets)
				}
				if scope == "all" && seriesTotals(series.Series).Requests != int64(3*tc.days) {
					t.Fatalf("all scope counts %d requests, want %d", seriesTotals(series.Series).Requests, 3*tc.days)
				}
			}
			if len(summary.UsageRange.Starts) != tc.buckets {
				t.Fatalf("usage range has %d starts, want %d", len(summary.UsageRange.Starts), tc.buckets)
			}
			if tc.days == 366 {
				// The window sets the size of the performance and usage range
				// parts; the rest of the payload is the same for every window.
				windowBytes := func(summary Summary) int {
					return len(mustJSON(t, summary.Performance)) + len(mustJSON(t, summary.UsageRange))
				}
				all := windowBytes(summary)
				sixMonths := windowBytes(store.SummaryFor(100, windows["180d"]))
				if all > 160<<10 || all*2 > sixMonths {
					t.Fatalf("a year in the all window takes %d bytes (180d takes %d), want at most 160 KiB and under half of 180d", all, sixMonths)
				}
				t.Logf("a year of data: all window %d bytes, 180d window %d bytes", all, sixMonths)
			}
		})
	}
}
