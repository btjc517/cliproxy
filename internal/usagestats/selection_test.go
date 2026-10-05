package usagestats

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// trueQuantile is the p-th quantile of values by rank, as histogram.percentile
// picks it.
func trueQuantile(values []float64, p float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}

// The selection merges the raw histograms of its accounts, so its percentiles
// are those of the merged requests, not a request-weighted mean of the
// per-account percentiles.
func TestSelectionPercentilesComeFromMergedHistograms(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 5, 15, 30, 0, 0, london)
	for _, tc := range []struct {
		name       string
		fast, slow int
	}{
		{"median among the fast account", 60, 40},
		{"median among the slow account", 30, 70},
		{"even split", 50, 50},
		{"one slow request", 99, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(now)
			store.machineName = func(string) string { return "" }
			var ttfts, latencies, throughputs []float64
			var tokens Counters
			var ttftHist, latencyHist, throughputHist histogram
			add := func(auth string, i int, ttft, latency time.Duration, output int64, counted bool) {
				store.record(coreusage.Record{AuthID: auth, Provider: "claude", RequestedAt: now.Add(-time.Duration(i) * time.Second),
					UpstreamStream: true, TTFT: ttft, Latency: latency,
					Detail: coreusage.Detail{InputTokens: 10, OutputTokens: output, CacheReadTokens: 100}})
				if !counted {
					return
				}
				ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
				ttfts = append(ttfts, ms(ttft))
				latencies = append(latencies, ms(latency))
				throughput := float64(output) / (latency - ttft).Seconds()
				throughputs = append(throughputs, throughput)
				ttftHist.observe(ms(ttft), ttftBase)
				latencyHist.observe(ms(latency), ttftBase)
				throughputHist.observe(throughput, throughputBase)
				tokens.add(Counters{Requests: 1, Input: 10, Output: output, CacheRead: 100})
			}
			// Fast: first token 100 ms to about 400 ms, 1 s to generate.
			for i := 0; i < tc.fast; i++ {
				ttft := time.Duration(100+5*i) * time.Millisecond
				add("claude-fast", i, ttft, ttft+time.Second, int64(400+3*i), true)
			}
			// Slow: first token 4 s to about 11 s, 20 s to generate.
			for i := 0; i < tc.slow; i++ {
				ttft := time.Duration(4000+100*i) * time.Millisecond
				add("claude-slow", 200+i, ttft, ttft+20*time.Second, int64(200+i), true)
			}
			// Not selected: many quick requests that would pull any blend down.
			for i := 0; i < 300; i++ {
				add("claude-other", 400+i, 50*time.Millisecond, 300*time.Millisecond, 100, false)
			}

			summary := store.SummaryForSelection(10, windows["24h"], []string{"claude-fast", "claude-slow"}, nil)
			scopes := summary.Performance.Scopes
			selection, ok := scopes[SelectionScope]
			if !ok {
				t.Fatal("no selection scope")
			}
			if selection.Requests != tokens.Requests {
				t.Fatalf("selection has %d requests, want %d", selection.Requests, tokens.Requests)
			}
			for _, check := range []struct {
				label  string
				got    float64
				merged float64
				truth  float64
				fast   float64
				slow   float64
			}{
				{"ttft p50", float64(selection.TTFT.P50), float64(roundMillis(ttftHist.percentile(0.5, ttftBase))), trueQuantile(ttfts, 0.5),
					float64(scopes["claude-fast"].TTFT.P50), float64(scopes["claude-slow"].TTFT.P50)},
				{"ttft p90", float64(selection.TTFT.P90), float64(roundMillis(ttftHist.percentile(0.9, ttftBase))), trueQuantile(ttfts, 0.9),
					float64(scopes["claude-fast"].TTFT.P90), float64(scopes["claude-slow"].TTFT.P90)},
				{"latency p50", float64(selection.Latency.P50), float64(roundMillis(latencyHist.percentile(0.5, ttftBase))), trueQuantile(latencies, 0.5),
					float64(scopes["claude-fast"].Latency.P50), float64(scopes["claude-slow"].Latency.P50)},
				{"throughput p50", selection.Throughput.P50, roundTenth(throughputHist.percentile(0.5, throughputBase)), trueQuantile(throughputs, 0.5),
					scopes["claude-fast"].Throughput.P50, scopes["claude-slow"].Throughput.P50},
				{"throughput p10", selection.Throughput.P10, roundTenth(throughputHist.percentile(0.1, throughputBase)), trueQuantile(throughputs, 0.1),
					scopes["claude-fast"].Throughput.P10, scopes["claude-slow"].Throughput.P10},
			} {
				if check.got != check.merged {
					t.Fatalf("%s = %v, want %v from the merged histogram", check.label, check.got, check.merged)
				}
				if math.Abs(check.got-check.truth) > check.truth*0.06 {
					t.Fatalf("%s = %v, want within 6%% of the true %v", check.label, check.got, check.truth)
				}
				fastShare := float64(tc.fast) / float64(tc.fast+tc.slow)
				blend := fastShare*check.fast + (1-fastShare)*check.slow
				if math.Abs(check.got-blend) < blend*0.2 && math.Abs(check.truth-blend) > blend*0.2 {
					t.Fatalf("%s = %v is the request-weighted mean %v, not the true %v", check.label, check.got, blend, check.truth)
				}
			}
			if tc.name == "median among the fast account" {
				if blend := 0.6*float64(scopes["claude-fast"].TTFT.P50) + 0.4*float64(scopes["claude-slow"].TTFT.P50); float64(selection.TTFT.P50) > blend/4 {
					t.Fatalf("ttft p50 = %d, want far below the request-weighted mean %.0f", selection.TTFT.P50, blend)
				}
			}

			wantHist := make([]TimeBin, 0)
			for _, index := range ttftHist.sortedIndexes() {
				wantHist = append(wantHist, TimeBin{LeMs: roundMillis(histUpper(index, ttftBase)), Count: ttftHist[index]})
			}
			if !reflect.DeepEqual(selection.TTFTHist, wantHist) {
				t.Fatalf("ttft_hist = %v, want the merged bins %v", selection.TTFTHist, wantHist)
			}
			point := selection.Series[len(selection.Series)-1]
			if point.Requests != tokens.Requests || point.LatencyP50 != roundMillis(latencyHist.percentile(0.5, ttftBase)) ||
				point.LatencyP90 != roundMillis(latencyHist.percentile(0.9, ttftBase)) ||
				point.ThroughputP10 != roundTenth(throughputHist.percentile(0.1, throughputBase)) ||
				point.Input != tokens.Input || point.Output != tokens.Output || point.CacheRead != tokens.CacheRead {
				t.Fatalf("newest selection point = %+v, want the merged requests %+v", point, tokens)
			}
		})
	}
}

// The selection is built like the provider scopes: one account matches its own
// scope, every Claude account matches the claude scope, and every account
// matches all, in every window and across the daily rollup. Unknown ids are
// ignored, and no known id means no selection scope.
func TestSelectionScopeIDs(t *testing.T) {
	london := mustZone(t, "Europe/London")
	now := time.Date(2026, 10, 5, 15, 30, 0, 0, london)
	store := newTestStore(now)
	store.machineName = func(string) string { return "" }
	auths := []string{"claude-a", "claude-b", "claude-c", "codex-d"}
	// One request every 7 hours for 40 days: hourly buckets and rolled up days.
	for i := 0; i < 40*24/7; i++ {
		auth := auths[i%len(auths)]
		at := now.Add(-time.Duration(i) * 7 * time.Hour)
		store.record(coreusage.Record{AuthID: auth, Provider: providerOf("", auth), RequestedAt: at, UpstreamStream: true, Failed: i%9 == 0,
			TTFT: time.Duration(200+i*37%3000) * time.Millisecond, Latency: time.Duration(2+i%40) * time.Second,
			Detail: coreusage.Detail{InputTokens: int64(i), OutputTokens: int64(100 + i*13%900), CacheReadTokens: 50}})
	}
	store.Summary(10)
	if len(store.perfDaily["claude-a"]) == 0 {
		t.Fatal("test data has no rolled up days")
	}
	many := make([]string, 0, 70)
	for i := 0; i < 70; i++ {
		many = append(many, fmt.Sprintf("claude-unknown-%02d", i))
	}
	for _, tc := range []struct {
		name  string
		ids   []string
		known []string
		// same names the scope the selection must equal; "" means none, and
		// "empty" an all-zero scope.
		same string
	}{
		{"one account", []string{"claude-a"}, nil, "claude-a"},
		{"every claude account", []string{"claude-c", "claude-a", "claude-b"}, nil, "claude"},
		{"every account", auths, nil, "all"},
		{"unknown ids ignored", []string{"nope", "claude-a", "codex-zzz"}, nil, "claude-a"},
		{"repeated id counted once", []string{"claude-a", "claude-a"}, nil, "claude-a"},
		{"only unknown ids", []string{"nope", "codex-zzz"}, nil, ""},
		{"empty scope", nil, nil, ""},
		{"empty id", []string{""}, nil, ""},
		{"listed account without data", []string{"claude-new"}, []string{"claude-new", "claude-a"}, "empty"},
		{"unlisted account without data", []string{"claude-new"}, []string{"claude-a"}, ""},
		{"known id 64th", append(append([]string{}, many[:63]...), "claude-a"), nil, "claude-a"},
		{"known id 65th", append(append([]string{}, many[:64]...), "claude-a"), nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"24h", "7d", "14d", "30d", "180d", "all"} {
				perf := store.SummaryForSelection(10, windows[key], tc.ids, tc.known).Performance
				got, ok := perf.Scopes[SelectionScope]
				switch tc.same {
				case "":
					if ok {
						t.Fatalf("%s: selection scope present, want none", key)
					}
				case "empty":
					if !ok || got.Requests != 0 || len(got.Series) != len(perf.Scopes["all"].Series) || len(got.TTFTHist) != 0 {
						t.Fatalf("%s: selection = %+v, want an empty scope as long as all", key, got)
					}
				default:
					if !ok || !reflect.DeepEqual(got, perf.Scopes[tc.same]) {
						t.Fatalf("%s: selection differs from the %s scope", key, tc.same)
					}
				}
			}
		})
	}

	// Without a selection the payload has no selection key; with one, it has
	// the same fields as every other scope.
	if payload := mustJSON(t, store.SummaryFor(10, windows["24h"]).Performance); strings.Contains(payload, `"selection"`) {
		t.Fatal("selection scope sent without a selection")
	}
	var perf struct {
		Scopes map[string]map[string]json.RawMessage `json:"scopes"`
	}
	payload := mustJSON(t, store.SummaryForSelection(10, windows["24h"], []string{"claude-a", "codex-d"}, nil).Performance)
	if errUnmarshal := json.Unmarshal([]byte(payload), &perf); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if got, want := sortedKeys(perf.Scopes[SelectionScope]), sortedKeys(perf.Scopes["all"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("selection fields %v, want %v", got, want)
	}
}

func sortedKeys(fields map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestParseSelection(t *testing.T) {
	ids := make([]string, 70)
	for i := range ids {
		ids[i] = fmt.Sprintf("claude-%02d.json", i)
	}
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"only separators", " , ,,", nil},
		{"trimmed", " claude-a.json , codex-b.json ", []string{"claude-a.json", "codex-b.json"}},
		{"repeats dropped", "a,b,a,,b,c", []string{"a", "b", "c"}},
		{"email ids", "claude-x@example.com.json,codex-y@example.com-pro.json", []string{"claude-x@example.com.json", "codex-y@example.com-pro.json"}},
		{"64 kept", strings.Join(ids[:64], ","), ids[:64]},
		{"first 64 of 70", strings.Join(ids, ","), ids[:64]},
		{"repeats do not use up the cap", strings.Repeat("a,", 200) + "b", []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseSelection(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseSelection(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
