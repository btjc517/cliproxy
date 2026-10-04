package usagestats

import (
	"path/filepath"
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
