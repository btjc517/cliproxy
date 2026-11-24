package auth

import (
	"testing"
	"time"
)

func TestNextMonthlyBill(t *testing.T) {
	at := func(text string) time.Time {
		parsed, errParse := time.Parse(time.RFC3339, text)
		if errParse != nil {
			t.Fatal(errParse)
		}
		return parsed
	}
	for _, tc := range []struct{ name, anchor, now, want string }{
		{"later this month", "2026-10-27T14:57:02Z", "2026-11-24T12:00:00Z", "2026-11-27T14:57:02Z"},
		{"earlier this month, so next month", "2026-10-19T14:47:18Z", "2026-11-24T12:00:00Z", "2026-12-19T14:47:18Z"},
		{"today, time still ahead", "2026-08-24T18:00:00Z", "2026-11-24T12:00:00Z", "2026-11-24T18:00:00Z"},
		{"today, time passed", "2026-08-24T09:00:00Z", "2026-11-24T12:00:00Z", "2026-12-24T09:00:00Z"},
		{"over the year end", "2025-08-20T17:17:54Z", "2026-12-25T00:00:00Z", "2027-01-20T17:17:54Z"},
		{"the 31st in a 30-day month", "2026-08-31T10:00:00Z", "2026-11-05T00:00:00Z", "2026-11-30T10:00:00Z"},
		{"the 31st in February", "2026-12-31T10:00:00Z", "2027-02-01T00:00:00Z", "2027-02-28T10:00:00Z"},
		{"paid until a time still ahead", "2026-12-21T11:53:24Z", "2026-11-24T12:00:00Z", "2026-12-21T11:53:24Z"},
		{"another zone", "2026-10-27T16:57:02+02:00", "2026-11-24T12:00:00Z", "2026-11-27T14:57:02Z"},
	} {
		if got := NextMonthlyBill(at(tc.anchor), at(tc.now)); !got.Equal(at(tc.want)) {
			t.Errorf("%s: NextMonthlyBill(%s, %s) = %s, want %s", tc.name, tc.anchor, tc.now, got.Format(time.RFC3339), tc.want)
		}
	}
	if got := NextMonthlyBill(time.Time{}, routerTestNow); !got.IsZero() {
		t.Fatalf("no anchor: got %v, want none", got)
	}
}

func TestParseClaudeProfilePlan(t *testing.T) {
	body := `{"account":{"created_at":"2024-03-05T21:36:02.485928Z"},
	  "organization":{"organization_type":"claude_max","subscription_created_at":"2026-10-27T14:57:02.378505Z","subscription_status":"active"}}`
	facts := ParseClaudeProfilePlan([]byte(body), routerTestNow)
	if facts == nil || !facts.Active() || facts.StartedAt.Format(time.RFC3339) != "2026-10-27T14:57:02Z" || !facts.ObservedAt.Equal(routerTestNow) {
		t.Fatalf("facts = %+v", facts)
	}
	if ParseClaudeProfilePlan([]byte(`{"error":"nope"}`), routerTestNow) != nil {
		t.Fatal("a body without an organization must not replace the last facts")
	}
	ended := ParseClaudeProfilePlan([]byte(`{"organization":{"subscription_created_at":"2026-10-27T14:57:02Z","subscription_status":"canceled"}}`), routerTestNow)
	if ended == nil || ended.Active() {
		t.Fatalf("a cancelled plan must not count as billing again: %+v", ended)
	}
}

func TestPlanFactsAreKeptAndAskedOncePerAge(t *testing.T) {
	path := t.TempDir() + "/routing-state.json"
	state := newTestRoutingState(routerTestNow)
	state.Configure(path)
	if !state.NeedsPlanReading("a.json", routerTestNow, 24*time.Hour) {
		t.Fatal("an account never read needs a reading")
	}
	// A failed read is not repeated every round.
	state.MarkPlanProbe("a.json", routerTestNow)
	if state.NeedsPlanReading("a.json", routerTestNow.Add(time.Hour), 24*time.Hour) {
		t.Fatal("asked again an hour after a failed read")
	}
	started := routerTestNow.Add(-40 * 24 * time.Hour)
	state.ObservePlan("a.json", &PlanFacts{StartedAt: started, Status: "active", ObservedAt: routerTestNow})
	if errFlush := state.Flush(); errFlush != nil {
		t.Fatal(errFlush)
	}
	restored := newTestRoutingState(routerTestNow)
	restored.Configure(path)
	facts, ok := restored.Plan("a.json")
	if !ok || !facts.StartedAt.Equal(started) || !facts.Active() {
		t.Fatalf("restored facts = %+v, %v", facts, ok)
	}
	if restored.NeedsPlanReading("a.json", routerTestNow.Add(time.Hour), 24*time.Hour) {
		t.Fatal("fresh facts were asked for again after a restart")
	}
	if !restored.NeedsPlanReading("a.json", routerTestNow.Add(25*time.Hour), 24*time.Hour) {
		t.Fatal("facts older than the age must be read again")
	}
}
