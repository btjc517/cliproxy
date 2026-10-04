package usagestats

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func newTestStore(now time.Time) *Store {
	store := newStore()
	store.nowFunc = func() time.Time { return now }
	return store
}

func TestStoreSummaryTotalsAndHourly(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	store := newTestStore(now)
	store.record(coreusage.Record{AuthID: "a", SessionID: "s1", RequestedAt: now.Add(-10 * time.Minute),
		Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 900, CacheCreationTokens: 50}})
	store.record(coreusage.Record{AuthID: "a", SessionID: "s1", RequestedAt: now.Add(-2 * time.Hour), Failed: true})
	store.record(coreusage.Record{AuthID: "a", RequestedAt: now.Add(-3 * 24 * time.Hour), Detail: coreusage.Detail{OutputTokens: 7}})

	summary := store.Summary(10)
	account := summary.Accounts["a"]
	if account.Today.Requests != 2 || account.Today.Failed != 1 {
		t.Fatalf("today = %+v, want 2 requests and 1 failure", account.Today)
	}
	if account.Today.CacheRead != 900 || account.Today.CacheWrite != 50 || account.Today.Output != 20 {
		t.Fatalf("today tokens = %+v", account.Today)
	}
	if account.Last7d.Requests != 3 || account.Last7d.Output != 27 {
		t.Fatalf("last 7d = %+v, want 3 requests and 27 output tokens", account.Last7d)
	}
	if len(account.Hourly) != 48 || account.Hourly[47].Requests != 1 || account.Hourly[45].Requests != 1 {
		t.Fatalf("hourly tail = %+v / %+v", account.Hourly[45], account.Hourly[47])
	}
	if summary.Totals["today"].Requests != 2 {
		t.Fatalf("totals today = %+v", summary.Totals["today"])
	}
}

func TestStoreSessionRecordsEveryCredentialUsed(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	store := newTestStore(now)
	store.record(coreusage.Record{AuthID: "a", SessionID: "s1", RequestedAt: now.Add(-time.Minute)})
	store.record(coreusage.Record{AuthID: "a", SessionID: "s1", RequestedAt: now})
	store.record(coreusage.Record{AuthID: "b", SessionID: "s2", RequestedAt: now})
	store.record(coreusage.Record{AuthID: "c", SessionID: "s2", RequestedAt: now})

	sessions := map[string]Session{}
	for _, session := range store.Summary(10).Sessions {
		sessions[session.ID] = session
	}
	if got := sessions["s1"].AuthIDs; len(got) != 1 || got[0] != "a" || sessions["s1"].Requests != 2 {
		t.Fatalf("s1 = %+v, want one credential and two requests", sessions["s1"])
	}
	if got := sessions["s2"].AuthIDs; len(got) != 2 {
		t.Fatalf("s2 auth ids = %v, want two credentials", got)
	}
}

func TestStoreSessionIgnoresFailedTries(t *testing.T) {
	now := time.Date(2026, 10, 4, 16, 0, 0, 0, time.UTC)
	store := newTestStore(now)
	store.record(coreusage.Record{AuthID: "refused", SessionID: "s1", RequestedAt: now, Failed: true})
	store.record(coreusage.Record{AuthID: "a", SessionID: "s1", RequestedAt: now})

	session := store.Summary(10).Sessions[0]
	if len(session.AuthIDs) != 1 || session.AuthIDs[0] != "a" {
		t.Fatalf("auth ids = %v, want only the account that answered", session.AuthIDs)
	}
	if session.Requests != 2 || session.Failed != 1 {
		t.Fatalf("session counters = %+v, want the failed try still counted", session.Counters)
	}
}

func TestStorePersistsAcrossRestart(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "usage-stats.json")

	first := newTestStore(now)
	first.path = path
	first.record(coreusage.Record{AuthID: "a", SessionID: "s1", RequestedAt: now, Detail: coreusage.Detail{OutputTokens: 5}})
	if errFlush := first.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}

	second := newTestStore(now)
	second.path = path
	second.loadLocked()
	summary := second.Summary(10)
	if summary.Accounts["a"].Today.Output != 5 || len(summary.Sessions) != 1 {
		t.Fatalf("reloaded summary = %+v", summary)
	}
}

func TestStorePrunesOldBuckets(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	store := newTestStore(now)
	store.record(coreusage.Record{AuthID: "a", SessionID: "old", RequestedAt: now.Add(-20 * 24 * time.Hour)})

	summary := store.Summary(10)
	if _, ok := summary.Accounts["a"]; ok || len(summary.Sessions) != 0 {
		t.Fatalf("expected old data pruned, got %+v", summary)
	}
}

func TestStoreDailyTallyOutlivesHourlyPruning(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	store := newTestStore(now)
	old := now.Add(-20 * 24 * time.Hour)
	store.record(coreusage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: old, Detail: coreusage.Detail{OutputTokens: 40}})
	store.record(coreusage.Record{AuthID: "codex-b.json", Provider: "codex", RequestedAt: now, Detail: coreusage.Detail{OutputTokens: 2}})

	history := store.Summary(10).History
	if len(history.Days) != 2 || history.Days[0].Date != "2026-09-13" || history.Days[0].Providers["claude"].Output != 40 {
		t.Fatalf("days = %+v, want the pruned hour kept in the daily tally", history.Days)
	}
	if history.Lifetime.Output != 42 || history.Today.Output != 2 || history.ThisMonth.Output != 2 {
		t.Fatalf("history totals = %+v", history)
	}
}

func TestStoreSeedsDailyFromOlderStatsFile(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "usage-stats.json")
	hour := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC).Unix()
	older := `{"version":1,"hourly":{` +
		`"claude-x@example.com.json":{"` + strconv.FormatInt(hour, 10) + `":{"requests":3,"output_tokens":30}},` +
		`"codex-y@example.com-pro.json":{"` + strconv.FormatInt(hour, 10) + `":{"requests":1,"cache_read_tokens":9}}},"sessions":{}}`
	if errWrite := os.WriteFile(path, []byte(older), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	first := newTestStore(now)
	first.path = path
	first.loadLocked()
	days := first.Summary(10).History.Days
	if len(days) != 1 || days[0].Providers["claude"].Output != 30 || days[0].Providers["codex"].CacheRead != 9 {
		t.Fatalf("seeded days = %+v", days)
	}
	if errFlush := first.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}

	second := newTestStore(now)
	second.path = path
	second.loadLocked()
	if got := second.Summary(10).History.Lifetime; got.Requests != 4 || got.Output != 30 {
		t.Fatalf("reloaded lifetime = %+v, want the seed counted once", got)
	}
}

func TestStoreMergesLogBackfill(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) // a Sunday
	dir := t.TempDir()
	backfill := `{"cutoff":"2026-10-03T18:18:00+01:00",` +
		`"days":{"2026-09-28":{"claude":{"requests":5,"output_tokens":100}},` +
		`"2026-10-03":{"claude":{"requests":2,"output_tokens":10},"codex":{"requests":1,"input_tokens":7}},` +
		`"2026-08-01":{"codex":{"requests":1,"output_tokens":1000}}},` +
		`"machines":{"m1":{"codex":{"requests":1,"output_tokens":1000}}}}`
	if errWrite := os.WriteFile(filepath.Join(dir, HistoryFileName), []byte(backfill), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	store := newTestStore(now)
	store.configure(filepath.Join(dir, "usage-stats.json"))
	defer close(store.stop)
	store.record(coreusage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC), Detail: coreusage.Detail{OutputTokens: 5}})
	store.record(coreusage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: now, Detail: coreusage.Detail{OutputTokens: 1}})

	history := store.Summary(10).History
	if history.BackfillCutoff == nil || history.BackfillMachines["m1"]["codex"].Output != 1000 {
		t.Fatalf("backfill metadata = %+v", history)
	}
	dates := make([]string, 0, len(history.Days))
	for _, day := range history.Days {
		dates = append(dates, day.Date)
	}
	if strings.Join(dates, ",") != "2026-08-01,2026-09-28,2026-10-03,2026-10-04" {
		t.Fatalf("dates = %v, want oldest first", dates)
	}
	if got := history.Days[2].Providers["claude"]; got.Output != 15 || got.Requests != 3 {
		t.Fatalf("3 Oct claude = %+v, want backfill and proxy tally added", got)
	}
	if history.Lifetime.Output != 1116 || history.ThisWeek.Output != 116 || history.ThisMonth.Output != 16 || history.Today.Output != 1 {
		t.Fatalf("totals: lifetime %d, week %d, month %d, today %d",
			history.Lifetime.Output, history.ThisWeek.Output, history.ThisMonth.Output, history.Today.Output)
	}
}
