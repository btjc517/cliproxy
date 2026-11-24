package auth

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestParseClaudeUsageMetersReadsPercentAsFraction(t *testing.T) {
	now := routerTestNow
	body := []byte(`{"five_hour":{"utilization":2.0,"resets_at":"2026-10-06T16:39:59.980198+00:00"},"seven_day":{"utilization":99.0,"resets_at":"2026-10-11T07:59:59.980222+00:00"},"seven_day_opus":null,"seven_day_sonnet":{"utilization":100,"resets_at":null},"extra_usage":{"utilization":null}}`)
	meters := ParseClaudeUsageMeters(body, now)
	if len(meters) != 3 {
		t.Fatalf("meters = %v, want 5h, 7d and 7d_sonnet", meters)
	}
	week := meters["7d"]
	if math.Abs(week.Utilization-0.99) > 1e-9 || week.Status != "allowed" || week.Window != weeklyQuotaWindow {
		t.Fatalf("7d = %+v", week)
	}
	if want := time.Date(2026, 10, 11, 7, 59, 59, 980222000, time.UTC); !week.ResetAt.Equal(want) {
		t.Fatalf("7d resets %s, want %s", week.ResetAt, want)
	}
	if meters["5h"].Window != fiveHourWindow || math.Abs(meters["5h"].Utilization-0.02) > 1e-9 {
		t.Fatalf("5h = %+v", meters["5h"])
	}
	if sonnet := meters["7d_sonnet"]; sonnet.Status != "rejected" || !sonnet.ResetAt.IsZero() {
		t.Fatalf("7d_sonnet = %+v, want rejected with no reset", sonnet)
	}
	if ParseClaudeUsageMeters([]byte(`not json`), now) != nil {
		t.Fatal("a non-JSON body produced meters")
	}
}

// An idle account has no reading until a probe records one, and a probe keeps
// the facts that real traffic left.
func TestObserveMetersFillsAnIdleAccount(t *testing.T) {
	now := routerTestNow
	state := newTestRoutingState(now)
	if !state.NeedsMeterReading("idle", now, 20*time.Minute) {
		t.Fatal("an account with no reading must need one")
	}
	state.ObserveMeters("idle", "claude", ParseClaudeUsageMeters([]byte(`{"five_hour":{"utilization":0},"seven_day":{"utilization":12,"resets_at":"2026-10-11T14:00:00+00:00"}}`), now), now)
	if state.NeedsMeterReading("idle", now.Add(10*time.Minute), 20*time.Minute) {
		t.Fatal("a fresh reading must not need another")
	}
	if !state.NeedsMeterReading("idle", now.Add(30*time.Minute), 20*time.Minute) {
		t.Fatal("a stale reading must need another")
	}
	account := state.account("idle")
	if account == nil || account.Provider != "claude" || math.Abs(account.Meters["7d"].Utilization-0.12) > 1e-9 || len(account.History["7d"]) != 1 {
		t.Fatalf("account = %+v", account)
	}
	if !account.Facts.ObservedAt.IsZero() || !account.Health.LastSuccessAt.IsZero() {
		t.Fatalf("a probe changed facts or health: %+v %+v", account.Facts, account.Health)
	}
}

func TestClaudeRefillMatchesOnlyTheConfirmedWindow(t *testing.T) {
	now := time.Now()
	end := now.Add(72 * time.Hour)
	q := QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: end.Add(14 * time.Second)}
	cases := []struct {
		name     string
		refilled string
		model    string
		blocked  string
		offset   time.Duration
		want     bool
	}{
		{"weekly shared", "7d", "", "", 0, true},
		{"five hour", "5h", "claude-haiku-5-5", "", 0, true},
		{"Fable only", "7d_oi", "claude-fable-5-2", "", 0, true},
		{"another family", "7d_oi", "claude-opus-5-5", "", 0, false},
		{"Fable cannot clear shared hold", "7d_oi", "", "", 0, false},
		{"shared week still exhausted", "7d_oi", "claude-fable-5-2", "7d", 0, false},
		{"Fable still exhausted", "7d", "claude-fable-5-2", "7d_oi", 0, false},
		{"different deadline", "7d", "claude-fable-5-2", "", time.Hour, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := claudeQuotaRefill{reopened: map[string]ClaudeWindowRefill{c.refilled: {Deadline: end.Add(c.offset), ObservedAt: now}}, meters: map[string]*Meter{}}
			if c.blocked != "" {
				r.meters[c.blocked] = &Meter{Name: c.blocked, Status: "rejected", Utilization: 1}
			}
			if got := r.matches(c.model, q, q.NextRecoverAt, now.Add(-time.Minute), now); got != c.want {
				t.Fatalf("matches=%v, want %v", got, c.want)
			}
		})
	}
}

func TestClaudeWeeklyRefillSurvivesExhaustedFiveHourWindow(t *testing.T) {
	now := time.Now()
	state := newTestRoutingState(now)
	week := now.Add(72 * time.Hour)
	five := now.Add(time.Hour)
	report := func(at time.Time, weekly, short float64) map[string]*Meter {
		return map[string]*Meter{"7d": {Name: "7d", Utilization: weekly, Status: "allowed", ResetAt: week, ObservedAt: at},
			"5h": {Name: "5h", Utilization: short, Status: "allowed", ResetAt: five, ObservedAt: at}}
	}
	state.ObserveMeters("a", "claude", report(now, 1, 1), now)
	later := now.Add(time.Minute)
	meters := report(later, .59, 1)
	r := claudeQuotaRefill{reopened: state.ReopenedClaudeWindows("a", meters, later), meters: meters}
	q := QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: week}
	if r.matches("", q, week, now, later) {
		t.Fatal("5h window was still exhausted")
	}
	state.ObserveMeters("a", "claude", meters, later)
	after := five.Add(time.Minute)
	meters = report(after, .59, 0)
	r = claudeQuotaRefill{reopened: state.ReopenedClaudeWindows("a", meters, after), meters: meters}
	if !r.matches("", q, week, now, after) {
		t.Fatal("weekly recovery evidence lost")
	}
	if r.matches("", q, week, after, after) {
		t.Fatal("old refill cleared a newer hold")
	}
}

func TestClaudeRefillKeepsASeparateBillingHold(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	a, err := m.Register(ctx, &Auth{ID: "refill-with-billing-error", Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	retry := 48 * time.Hour
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: "week spent"}})
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "claude", Error: &Error{HTTPStatus: 402, Message: "payment required"}})
	before, _ := m.GetByID(a.ID)
	if before.LastError == nil || before.LastError.HTTPStatus != 402 || !before.Quota.Exceeded {
		t.Fatal("missing overlapping billing and quota holds")
	}
	windows := map[string]ClaudeWindowRefill{"7d": {Deadline: before.Quota.NextRecoverAt, ObservedAt: time.Now()}}
	if err = m.ReconcileClaudeQuota(ctx, before, windows, map[string]*Meter{"7d": {Name: "7d", Status: "allowed"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	if after.LastError == nil || after.LastError.HTTPStatus != 402 || !after.Unavailable {
		t.Fatal("refill cleared billing hold")
	}
}
