package auth

import (
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
