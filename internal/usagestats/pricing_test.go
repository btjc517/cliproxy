package usagestats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestLookupPriceMatchesLongestModelPrefix(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"claude-opus-5", "claude-opus-5"},
		{"claude-opus-5-20260301", "claude-opus-5"},
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"claude-opus-4-20250514", "claude-opus-4"},
		{"claude-opus-5-5[1m]", "claude-opus-5-5"},
		{"Claude-Sonnet-4-6[1M]", "claude-sonnet-4-6"},
		{"anthropic/claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"models/claude-haiku-4-5@20251001", "claude-haiku-4-5"},
		{"claude-3-5-haiku-20241022", "claude-3-5-haiku"},
		{"claude-mythos-5-1", "claude-mythos-5-1"},
		{"claude-mythos-5:thinking", "claude-mythos-5"},
		{"openai/gpt-6.1-sol", "gpt-6.1-sol"},
		{"gpt-6-sol", "gpt-6-sol"},
		{"gpt-6-astra(high)", "gpt-6-astra"},
		{"gpt-5.5-codex", "gpt-5.5"},
		{"gpt-5.6-terra_preview", "gpt-5.6-terra"},
		{"gpt-5.56", ""},
		{"gpt-6-astrax", ""},
		{"claude-opus", ""},
		{"codex-auto-review", ""},
		{"", ""},
	} {
		got, _, ok := lookupPrice(tc.model)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("lookupPrice(%q) = %q, %v; want %q", tc.model, got, ok, tc.want)
		}
	}

	tokens := Counters{Input: 1000, Output: 100}
	fallback := defaultFallback()
	if got, want := costFor("codex-auto-review", "codex", tokens, fallback, true), costFor("gpt-6-astra", "codex", tokens, fallback, true); got != want || got == 0 {
		t.Fatalf("unknown codex model costs %v, want the gpt-6-astra price %v", got, want)
	}
	if got, want := costFor("", "claude", tokens, fallback, true), costFor("claude-opus-5-5", "claude", tokens, fallback, true); got != want || got == 0 {
		t.Fatalf("claude request without a model costs %v, want the claude-opus-5-5 price %v", got, want)
	}
	if got := costFor("mystery-model", "gemini", tokens, fallback, true); got != 0 {
		t.Fatalf("unknown model of a provider without a fallback costs %v, want 0", got)
	}
}

// One request is priced at its own model, with OpenAI's long context rates
// only above 272K input tokens, and the cost reaches every tally.
func TestRequestCostByModel(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, time.UTC)}
	store := newClockStore(clock)
	var total float64
	for _, tc := range []struct {
		name, auth, provider, model string
		detail                      coreusage.Detail
		want                        float64
	}{
		{"claude", "claude-a", "claude", "claude-opus-5-5",
			coreusage.Detail{InputTokens: 1000, CacheCreationTokens: 2000, CacheReadTokens: 100_000, OutputTokens: 500}, 0.044},
		{"claude long context has no surcharge", "claude-b", "claude", "claude-opus-5-5[1m]",
			coreusage.Detail{InputTokens: 300_000, CacheReadTokens: 50_000, OutputTokens: 1000}, 1.23},
		{"openai short, cache write priced as input", "codex-c", "codex", "gpt-6-astra",
			coreusage.Detail{InputTokens: 10_000, CacheCreationTokens: 1000, CacheReadTokens: 200_000, OutputTokens: 2000}, 0.41},
		{"openai long", "codex-d", "codex", "gpt-6-astra",
			coreusage.Detail{InputTokens: 100_000, CacheReadTokens: 200_000, OutputTokens: 1000}, 2.475},
		{"openai at exactly 272K is short", "codex-e", "codex", "gpt-6-astra",
			coreusage.Detail{InputTokens: 72_000, CacheReadTokens: 200_000}, 0.92},
		{"openai model without long rates", "codex-f", "codex", "gpt-5.6-sol",
			coreusage.Detail{InputTokens: 300_000, OutputTokens: 1000}, 1.22},
	} {
		store.record(coreusage.Record{AuthID: tc.auth, Provider: tc.provider, Model: tc.model, SessionID: "s-" + tc.auth,
			RequestedAt: clock.now, Detail: tc.detail})
		summary := store.Summary(10)
		account := summary.Accounts[tc.auth]
		if account.Today.APICost != tc.want || account.Last24h.APICost != tc.want || account.Hourly[47].APICost != tc.want ||
			account.Daily[accountDays-1].APICost != tc.want {
			t.Fatalf("%s: account cost today %v, 24h %v, hour %v, day %v; want %v", tc.name,
				account.Today.APICost, account.Last24h.APICost, account.Hourly[47].APICost, account.Daily[accountDays-1].APICost, tc.want)
		}
		for _, session := range summary.Sessions {
			if session.ID == "s-"+tc.auth && session.APICost != tc.want {
				t.Fatalf("%s: session cost %v, want %v", tc.name, session.APICost, tc.want)
			}
		}
		if got := usageTotals(summary.UsageRange.Accounts[tc.auth]).APICost; got != tc.want {
			t.Fatalf("%s: usage range cost %v, want %v", tc.name, got, tc.want)
		}
		total = roundCost(total + tc.want)
	}
	summary := store.Summary(10)
	if got := summary.Totals["today"].APICost; got != total {
		t.Fatalf("today's total cost %v, want %v", got, total)
	}
	history := summary.History
	if history.Today.APICost != total || history.Lifetime.APICost != total || history.ThisWeek.APICost != total || history.ThisMonth.APICost != total {
		t.Fatalf("history cost today %v, week %v, month %v, lifetime %v; want %v",
			history.Today.APICost, history.ThisWeek.APICost, history.ThisMonth.APICost, history.Lifetime.APICost, total)
	}
	if got, want := history.Days[0].Providers["claude"].APICost, roundCost(0.044+1.23); got != want {
		t.Fatalf("claude day cost %v, want %v", got, want)
	}
}

// A stats file from before API cost is estimated once at the fallback
// prices and saved with cost_since. Loading it again changes nothing, and
// new requests are priced exactly on top.
func TestCostEstimateRunsOnce(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, time.UTC)}
	start := clock.now
	path := filepath.Join(t.TempDir(), "usage-stats.json")
	hour := strconv.FormatInt(time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC).Unix(), 10)
	claude := `{"requests":2,"input_tokens":1000,"output_tokens":1000}`
	codex := `{"requests":1,"input_tokens":2000,"cache_read_tokens":10000}`
	older := `{"version":3,` +
		`"hourly":{"claude-a.json":{"` + hour + `":` + claude + `},"codex-b.json":{"` + hour + `":` + codex + `},"claude-idle.json":{"` + hour + `":{"requests":1,"failed":1}}},` +
		`"daily":{"2026-10-05":{"claude":` + claude + `,"codex":` + codex + `}},` +
		`"account_daily":{"claude-a.json":{"2026-10-05":` + claude + `},"codex-b.json":{"2026-10-05":` + codex + `}},` +
		`"auth_providers":{"claude-a.json":"claude","codex-b.json":"codex"},` +
		`"sessions":{"s1":{"provider":"claude","model":"claude-sonnet-4-6","auth_ids":["claude-a.json"],` +
		`"last_seen":"2026-10-05T15:00:00Z","requests":2,"input_tokens":1000,"output_tokens":1000}}}`
	writeTestFile(t, path, older)

	const claudeCost, codexCost, sessionCost = 0.024, 0.03, 0.018 // opus 5.5, astra, sonnet 4.6
	check := func(t *testing.T, store *Store, step string, claudeDay float64) {
		t.Helper()
		if got := store.hourly["claude-a.json"][time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC).Unix()].APICost; got != claudeCost {
			t.Fatalf("%s: claude hour cost %v, want %v", step, got, claudeCost)
		}
		if got := store.hourly["codex-b.json"][time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC).Unix()].APICost; got != codexCost {
			t.Fatalf("%s: codex hour cost %v, want %v", step, got, codexCost)
		}
		if got := store.hourly["claude-idle.json"][time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC).Unix()].APICost; got != 0 {
			t.Fatalf("%s: hour without tokens costs %v, want 0", step, got)
		}
		if got := store.daily["2026-10-05"]["claude"].APICost; got != claudeDay {
			t.Fatalf("%s: claude day cost %v, want %v", step, got, claudeDay)
		}
		if got := store.daily["2026-10-05"]["codex"].APICost; got != codexCost {
			t.Fatalf("%s: codex day cost %v, want %v", step, got, codexCost)
		}
		if got := store.accountDaily["codex-b.json"]["2026-10-05"].APICost; got != codexCost {
			t.Fatalf("%s: codex account day cost %v, want %v", step, got, codexCost)
		}
		if got := store.sessions["s1"].APICost; got != sessionCost {
			t.Fatalf("%s: session cost %v, want %v at its own model", step, got, sessionCost)
		}
		if !store.costSince.Equal(start) {
			t.Fatalf("%s: cost_since %v, want %v", step, store.costSince, start)
		}
	}

	first := newClockStore(clock)
	first.configure(path)
	defer close(first.stop)
	check(t, first, "first load", claudeCost)

	// The estimate is saved straight away with cost_since.
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved fileState
	if errParse := json.Unmarshal(data, &saved); errParse != nil {
		t.Fatalf("saved file does not parse: %v", errParse)
	}
	if !saved.CostSince.Equal(start) || saved.Daily["2026-10-05"]["claude"].APICost != claudeCost {
		t.Fatalf("saved cost_since %v, claude day cost %v", saved.CostSince, saved.Daily["2026-10-05"]["claude"].APICost)
	}
	// Code from before API cost ignores the new fields, so a rollback loads it.
	var rollback struct {
		Version int `json:"version"`
		Daily   map[string]map[string]*struct {
			Requests int64 `json:"requests"`
			Output   int64 `json:"output_tokens"`
		} `json:"daily"`
	}
	if errParse := json.Unmarshal(data, &rollback); errParse != nil || rollback.Daily["2026-10-05"]["claude"].Output != 1000 {
		t.Fatalf("older shape cannot read the new file: %v", errParse)
	}

	clock.now = clock.now.Add(time.Hour)
	second := newClockStore(clock)
	second.configure(path)
	defer close(second.stop)
	check(t, second, "second load", claudeCost)
	if second.changes != 0 {
		t.Fatalf("second load changed the tally %d times, want none", second.changes)
	}

	second.record(coreusage.Record{AuthID: "claude-a.json", Provider: "claude", Model: "claude-haiku-4-5", RequestedAt: clock.now,
		Detail: coreusage.Detail{InputTokens: 1000, OutputTokens: 1000}})
	const haikuCost = 0.006
	if got := second.hourly["claude-a.json"][time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC).Unix()].APICost; got != haikuCost {
		t.Fatalf("new request costs %v, want %v at its own model", got, haikuCost)
	}
	third := reloadStore(t, second, clock)
	check(t, third, "after a new request", roundCost(claudeCost+haikuCost))
}

// writeHistory writes a log backfill next to a stats file in dir.
func writeHistory(t *testing.T, dir, data string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, HistoryFileName), data)
}

// History days are priced by model where the backfill has models, with the
// rest at the fallback, and wholly at the fallback where it has none.
func TestHistoryDaysPricedByModel(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	writeHistory(t, dir, `{"cutoff":"2026-10-03T18:00:00Z",`+
		`"days":{"2026-10-01":{"claude":{"requests":3,"input_tokens":1200,"output_tokens":1000,"cache_read_tokens":10000},`+
		`"codex":{"requests":1,"input_tokens":2000,"output_tokens":100}},`+
		`"2026-10-02":{"claude":{"requests":1,"output_tokens":2000}}},`+
		`"models":{"2026-10-01":{"claude":{"claude-sonnet-4-6":{"input_tokens":600,"output_tokens":1000,"cache_read_tokens":10000},`+
		`"claude-haiku-4-5":{"input_tokens":400}},`+
		`"codex":{"codex-auto-review":{"input_tokens":2000,"output_tokens":100}}}}}`)
	store := newClockStore(clock)
	store.configure(filepath.Join(dir, "usage-stats.json"))
	defer close(store.stop)

	// Sonnet 4.6 has the most tokens, so it is the claude fallback; codex has
	// no priced model and keeps gpt-6-astra.
	const (
		claudeByModel = 0.0208 // sonnet 0.0198 + haiku 0.0004 + 200 uncovered input at sonnet 0.0006
		codexDay      = 0.025  // unknown model at gpt-6-astra
		claudeNoModel = 0.03   // 2000 output at sonnet
	)
	history := store.Summary(10).History
	if len(history.Days) != 2 {
		t.Fatalf("days = %+v", history.Days)
	}
	if got := history.Days[0].Providers["claude"].APICost; got != claudeByModel {
		t.Fatalf("1 Oct claude costs %v, want %v", got, claudeByModel)
	}
	if got := history.Days[0].Providers["codex"].APICost; got != codexDay {
		t.Fatalf("1 Oct codex costs %v, want %v", got, codexDay)
	}
	if got := history.Days[1].Providers["claude"].APICost; got != claudeNoModel {
		t.Fatalf("2 Oct claude costs %v, want %v at the fallback", got, claudeNoModel)
	}
	want := roundCost(claudeByModel + codexDay + claudeNoModel)
	if history.Lifetime.APICost != want || history.ThisMonth.APICost != want || history.ThisWeek.APICost != want || history.Today.APICost != 0 {
		t.Fatalf("history cost lifetime %v, month %v, week %v, today %v; want %v",
			history.Lifetime.APICost, history.ThisMonth.APICost, history.ThisWeek.APICost, history.Today.APICost, want)
	}
}

func TestFallbackModelIsTopModelOfLast30Days(t *testing.T) {
	models := func(day, provider string, tokens map[string]int64) map[string]map[string]map[string]Counters {
		byModel := make(map[string]Counters, len(tokens))
		for model, count := range tokens {
			byModel[model] = Counters{Input: count}
		}
		return map[string]map[string]map[string]Counters{day: {provider: byModel}}
	}
	merge := func(parts ...map[string]map[string]map[string]Counters) *historyFile {
		history := &historyFile{Models: make(map[string]map[string]map[string]Counters)}
		for _, part := range parts {
			for day, providers := range part {
				if history.Models[day] == nil {
					history.Models[day] = make(map[string]map[string]Counters)
				}
				for provider, byModel := range providers {
					history.Models[day][provider] = byModel
				}
			}
		}
		return history
	}
	for _, tc := range []struct {
		name    string
		history *historyFile
		claude  string
		codex   string
	}{
		{"no history", nil, "claude-opus-5-5", "gpt-6-astra"},
		{"history without models", &historyFile{Days: map[string]map[string]Counters{"2026-10-01": {"claude": {Output: 9}}}}, "claude-opus-5-5", "gpt-6-astra"},
		{"only the 30 days up to the newest day count", merge(
			models("2026-09-01", "claude", map[string]int64{"claude-haiku-4-5": 5000}),
			models("2026-09-02", "claude", map[string]int64{"claude-sonnet-5": 1000}),
			models("2026-10-01", "claude", map[string]int64{"claude-sonnet-5": 200, "claude-opus-5-5": 1100}),
		), "claude-sonnet-5", "gpt-6-astra"},
		{"variants of one model count together", merge(
			models("2026-10-01", "claude", map[string]int64{"claude-opus-5-5": 600, "claude-opus-5-5[1m]": 500, "claude-sonnet-5": 1000}),
		), "claude-opus-5-5", "gpt-6-astra"},
		{"unpriced models are skipped", merge(
			models("2026-10-01", "codex", map[string]int64{"codex-auto-review": 1_000_000, "gpt-6-sol": 50}),
		), "claude-opus-5-5", "gpt-6-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fallbackModels(tc.history)
			if got["claude"] != tc.claude || got["codex"] != tc.codex {
				t.Fatalf("fallback = %v, want claude %s and codex %s", got, tc.claude, tc.codex)
			}
		})
	}

	// A request for a model the table does not know is priced at the
	// history's fallback.
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, time.UTC)}
	dir := t.TempDir()
	writeHistory(t, dir, `{"days":{},"models":{"2026-10-01":{"codex":{"codex-auto-review":{"input_tokens":1000000},"gpt-6-sol":{"input_tokens":50}}}}}`)
	store := newClockStore(clock)
	store.configure(filepath.Join(dir, "usage-stats.json"))
	defer close(store.stop)
	store.record(coreusage.Record{AuthID: "codex-a", Provider: "codex", Model: "codex-auto-review", RequestedAt: clock.now,
		Detail: coreusage.Detail{InputTokens: 1000, OutputTokens: 100}})
	if got, want := store.Summary(10).Accounts["codex-a"].Today.APICost, 0.003; got != want {
		t.Fatalf("unknown model costs %v, want %v at gpt-6-sol", got, want)
	}
}

func TestSummaryPricing(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 45, 500, time.UTC)}
	notModelledJSON := `"not_modelled":"batch, fast mode, US-only inference, web search fees, 1-hour cache writes"`

	dir := t.TempDir()
	writeHistory(t, dir, `{"days":{"2026-10-01":{"claude":{"output_tokens":10}}},`+
		`"models":{"2026-10-01":{"claude":{"claude-sonnet-4-6":{"output_tokens":10}}}}}`)
	store := newClockStore(clock)
	store.configure(filepath.Join(dir, "usage-stats.json"))
	defer close(store.stop)
	want := `{"as_of":"2026-10-06","exact_since":"2026-10-05T15:30:45Z","history_by_model":true,` +
		`"fallback":{"claude":"claude-sonnet-4-6","codex":"gpt-6-astra"},` + notModelledJSON + `}`
	if got := mustJSON(t, store.Summary(10).Pricing); got != want {
		t.Fatalf("pricing = %s\nwant %s", got, want)
	}

	// A saved cost_since wins over the clock, and a history without models
	// keeps the static fallbacks.
	dir = t.TempDir()
	writeHistory(t, dir, `{"days":{"2026-10-01":{"claude":{"output_tokens":10}}}}`)
	writeTestFile(t, filepath.Join(dir, "usage-stats.json"), `{"version":3,"hourly":{},"sessions":{},"cost_since":"2026-09-30T08:00:00+01:00"}`)
	store = newClockStore(clock)
	store.configure(filepath.Join(dir, "usage-stats.json"))
	defer close(store.stop)
	want = `{"as_of":"2026-10-06","exact_since":"2026-09-30T08:00:00+01:00","history_by_model":false,` +
		`"fallback":{"claude":"claude-opus-5-5","codex":"gpt-6-astra"},` + notModelledJSON + `}`
	if got := mustJSON(t, store.Summary(10).Pricing); got != want {
		t.Fatalf("pricing = %s\nwant %s", got, want)
	}
	var summary map[string]json.RawMessage
	if errParse := json.Unmarshal([]byte(mustJSON(t, store.Summary(10))), &summary); errParse != nil || string(summary["pricing"]) != want {
		t.Fatalf("summary JSON pricing = %s, want %s", summary["pricing"], want)
	}
}
