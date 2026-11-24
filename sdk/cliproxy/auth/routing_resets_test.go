package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// claudeResetBody is the shape of GET /api/oauth/usage?cedar_ember=1 as an
// account with one unused full reset and one used Fable reset reports it.
const claudeResetBody = `{
  "five_hour": {"utilization": 12.0, "resets_at": "2026-10-04T19:00:00+00:00"},
  "seven_day": {"utilization": 100.0, "resets_at": "2026-10-08T16:00:00+00:00"},
  "cedar_ember": {
    "eligible": true, "at_limit": true, "exhausted": ["seven_day"],
    "cooldown_until": null, "next_grant_id": "full-reset-2610",
    "grants": [
      {"id": "full-reset-2610", "label": "One full limit reset (October 2026)", "resets_total": 1, "resets_left": 1,
       "starts_at": "2026-10-01T00:00:00Z", "ends_at": "2026-11-10T22:00:00Z",
       "clears": ["seven_day", "seven_day_overage_included"], "paused": false, "usable_now": true,
       "use_requires_limit": true, "blocking": [], "type": "weekly", "gives_back_weekly": false, "unavailable_reason": null},
      {"id": "launch-2610-fable", "label": "New model launch: one Fable reset", "resets_total": 1, "resets_left": 0,
       "starts_at": "2026-10-01T00:00:00Z", "ends_at": "2027-03-09T00:00:00Z",
       "clears": ["seven_day_overage_included"], "paused": false, "usable_now": false,
       "use_requires_limit": true, "blocking": ["seven_day"], "type": "weekly", "gives_back_weekly": true, "unavailable_reason": "blocked_seven_day"}
    ]
  }
}`

func TestParseClaudeResetStatusReadsGrants(t *testing.T) {
	status := ParseClaudeResetStatus([]byte(claudeResetBody), routerTestNow)
	if status == nil || len(status.Grants) != 2 {
		t.Fatalf("status = %+v, want two grants", status)
	}
	full := status.Grants[0]
	if full.ID != "full-reset-2610" || full.Left != 1 || full.Total != 1 || !full.UsableNow || !full.RequiresLimit {
		t.Fatalf("full grant = %+v", full)
	}
	if strings.Join(full.Clears, ",") != "7d,7d_oi" {
		t.Fatalf("full grant clears %v, want the week and the Fable week", full.Clears)
	}
	if !full.EndsAt.Equal(time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("full grant ends %v", full.EndsAt)
	}
	fable := status.Grants[1]
	if fable.Left != 0 || fable.UsableNow || !fable.GivesBackWeekly || fable.Unavailable != "blocked_seven_day" || strings.Join(fable.Blocking, ",") != "7d" {
		t.Fatalf("fable grant = %+v", fable)
	}
}

func TestParseClaudeResetStatusWithoutBlockKeepsLastReading(t *testing.T) {
	if status := ParseClaudeResetStatus([]byte(`{"five_hour":{"utilization":1}}`), routerTestNow); status != nil {
		t.Fatalf("status = %+v, want nil for a report without resets", status)
	}
	status := ParseClaudeResetStatus([]byte(`{"cedar_ember":{"eligible":false,"grants":[{"id":"x","resets_left":1}]}}`), routerTestNow)
	if status == nil || len(status.Grants) != 0 {
		t.Fatalf("status = %+v, want an empty reading for an account that cannot use resets", status)
	}
}

func TestParseCodexResetStatusReadsCredits(t *testing.T) {
	body := `{"credits":[
	  {"id":"cred_1","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-10-01T00:00:00Z","expires_at":"2026-10-20T00:00:00Z","title":"Full reset (Weekly + 5 hr)"},
	  {"id":"cred_2","reset_type":"codex_rate_limits","status":"redeemed","granted_at":"2026-09-01T00:00:00Z","expires_at":"2026-10-01T00:00:00Z","title":"Full reset (Weekly + 5 hr)"}
	],"available_count":1}`
	status := ParseCodexResetStatus([]byte(body), routerTestNow)
	if status == nil || len(status.Grants) != 2 {
		t.Fatalf("status = %+v, want two credits", status)
	}
	if got := status.Grants[0]; got.Left != 1 || !got.UsableNow || strings.Join(got.Clears, ",") != "primary,secondary" || got.RequiresLimit {
		t.Fatalf("available credit = %+v", got)
	}
	if got := status.Grants[1]; got.Left != 0 || got.UsableNow || got.Unavailable != "redeemed" {
		t.Fatalf("redeemed credit = %+v", got)
	}
	if ParseCodexResetStatus([]byte(`{"error":"nope"}`), routerTestNow) != nil {
		t.Fatal("a body without credits must not replace the last reading")
	}
}

func TestParseCodexUsageMetersReadsBothWindows(t *testing.T) {
	body := `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,
	  "primary_window":{"used_percent":40,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":1791136800},
	  "secondary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_after_seconds":86400,"reset_at":1791223200}}}`
	meters := ParseCodexUsageMeters([]byte(body), routerTestNow)
	if got := meters["primary"]; got == nil || got.Utilization != 0.4 || got.Window != fiveHourWindow || got.ResetAt.Unix() != 1791136800 {
		t.Fatalf("primary = %+v", got)
	}
	if got := meters["secondary"]; got == nil || got.Utilization != 1 || got.Window != weeklyQuotaWindow {
		t.Fatalf("secondary = %+v", got)
	}
}

// resetTestGrant is an unused reset of the shared week that the provider says
// can be used now.
func resetTestGrant(id string) ResetGrant {
	return ResetGrant{
		ID: id, Label: "One full limit reset", Total: 1, Left: 1, Clears: []string{"7d", "7d_oi"},
		UsableNow: true, RequiresLimit: true, EndsAt: routerTestNow.Add(30 * 24 * time.Hour),
	}
}

func observeTestResets(state *RoutingState, authID string, grants ...ResetGrant) {
	state.ObserveResets(authID, "claude", &ResetStatus{Grants: grants, ObservedAt: routerTestNow.Add(-time.Minute)})
}

// weekSpent puts an account at its weekly limit with the reset the given time
// away, and a 5-hour window with room.
func weekSpent(state *RoutingState, authID string, resetIn time.Duration) {
	setMeter(state, authID, "claude", Meter{Name: "5h", Utilization: 0.2, ResetAt: routerTestNow.Add(3 * time.Hour), Status: "allowed"})
	setMeter(state, authID, "claude", Meter{Name: "7d", Utilization: 1, ResetAt: routerTestNow.Add(resetIn), Status: "rejected"})
}

func weekOpen(state *RoutingState, authID string, used float64) {
	setMeter(state, authID, "claude", Meter{Name: "5h", Utilization: 0.2, ResetAt: routerTestNow.Add(3 * time.Hour), Status: "allowed"})
	setMeter(state, authID, "claude", Meter{Name: "7d", Utilization: used, ResetAt: routerTestNow.Add(5 * 24 * time.Hour), Status: "allowed"})
}

func adviceFor(t *testing.T, state *RoutingState, auths []*Auth, authID, grantID string) ResetAdvice {
	t.Helper()
	view := state.Dashboard(auths, routerTestNow)
	for _, grant := range view.Accounts[authID].Resets {
		if grant.ID == grantID {
			return grant.Advice
		}
	}
	t.Fatalf("no reset %s on %s in %+v", grantID, authID, view.Accounts[authID].Resets)
	return ResetAdvice{}
}

// A reset is held while another account can carry the work: the limit comes
// back by itself and the reset stays for when nothing else has room.
func TestResetAdviceHoldsWhileAnotherAccountHasRoom(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a, b := routerTestAuth("a.json", "claude", "a@example.com"), routerTestAuth("b.json", "claude", "b@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	weekOpen(state, b.ID, 0.3)
	observeTestResets(state, a.ID, resetTestGrant("full"))

	advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "full")
	if advice.Action != "hold" || !strings.Contains(advice.Reason, "1 other account has room") {
		t.Fatalf("advice = %+v, want hold because b has room", advice)
	}
	if got := state.ResetSuggestions([]*Auth{a, b}, routerTestNow); len(got) != 0 {
		t.Fatalf("suggestions = %+v, want none", got)
	}
}

// With every account at a limit, the reset is what gets work moving again.
func TestResetAdviceUsesWhenNoAccountHasRoom(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a, b := routerTestAuth("a.json", "claude", "a@example.com"), routerTestAuth("b.json", "claude", "b@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	weekSpent(state, b.ID, 2*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))

	advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "full")
	if advice.Action != "use" || !strings.Contains(advice.Reason, "Every account is at a limit") || advice.Gain != 1 || advice.EarlyHours != 96 {
		t.Fatalf("advice = %+v, want use: nobody has room", advice)
	}
	if !strings.Contains(advice.Reason, "Clears the weekly limit 4d early") {
		t.Fatalf("reason = %q, want what the reset gives back", advice.Reason)
	}
	suggestions := state.ResetSuggestions([]*Auth{a, b}, routerTestNow)
	if len(suggestions) != 1 || suggestions[0].AuthID != a.ID || suggestions[0].Grant.ID != "full" || suggestions[0].Provider != "claude" {
		t.Fatalf("suggestions = %+v", suggestions)
	}
}

// An account that is switched off takes no work, so its reset is never
// suggested, however full the pool is.
func TestResetAdviceHoldsOnAccountThatIsOff(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	a.Disabled = true
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))

	advice := adviceFor(t, state, []*Auth{a}, a.ID, "full")
	if advice.Action != "hold" || !strings.Contains(advice.Reason, "This account is off") {
		t.Fatalf("advice = %+v, want hold: the account is off", advice)
	}
	state.SetResetMode("auto")
	if candidate := state.AutoResetCandidate([]*Auth{a}, routerTestNow); candidate != nil {
		t.Fatalf("automatic mode would use a reset on an account that is off: %+v", candidate)
	}
}

// An account out of both its week and its Fable week, with a reset that clears
// both: the shared week decides, since it stops every model. Seen live on
// 24 Nov 2026, where such a reset was judged on Fable alone.
func TestResetAdviceJudgesFullResetOnTheSharedWeek(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a, b := routerTestAuth("a.json", "claude", "a@example.com"), routerTestAuth("b.json", "claude", "b@example.com")
	weekSpent(state, a.ID, 2*24*time.Hour)
	setMeter(state, a.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 0.98, ResetAt: routerTestNow.Add(5 * 24 * time.Hour), Status: "allowed"})
	weekSpent(state, b.ID, 3*24*time.Hour)
	setMeter(state, b.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 0.4, ResetAt: routerTestNow.Add(5 * 24 * time.Hour), Status: "allowed"})
	grant := resetTestGrant("full")
	grant.RequiresLimit = false
	observeTestResets(state, a.ID, grant)

	advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "full")
	if advice.Meter != "7d" || advice.Action != "use" || !strings.Contains(advice.Reason, "Every account is at a limit. Clears the weekly limit 2d early") {
		t.Fatalf("advice = %+v, want use, judged on the shared week", advice)
	}
}

// A grant or reading without a time leaves the field out of the dashboard
// data, so the page never shows the year 1.
func TestResetViewLeavesOutUnsetTimes(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekOpen(state, a.ID, 0.2)
	grant := resetTestGrant("full")
	grant.EndsAt = time.Time{}
	observeTestResets(state, a.ID, grant)
	b := routerTestAuth("b.json", "claude", "b@example.com")
	weekOpen(state, b.ID, 0.2)
	raw, errMarshal := json.Marshal(state.Dashboard([]*Auth{a, b}, routerTestNow).Accounts)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, field := range []string{"ends_at", "starts_at", "cooldown_until"} {
		if strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("dashboard data carries an unset %s: %s", field, raw)
		}
	}
	// a has a reading of its resets, b has none.
	if strings.Count(string(raw), `"resets_observed_at"`) != 1 {
		t.Fatalf("resets_observed_at must be set for a only: %s", raw)
	}
}

// A reset does not get a refused account working again, so it is not
// suggested or used automatically while the provider turns the account away.
func TestResetAdviceHoldsOnAccountTheProviderRefuses(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))
	state.mu.Lock()
	state.accounts[a.ID].Health = AccountHealth{LastFailureAt: routerTestNow.Add(-time.Minute), LastFailureStatus: http.StatusPaymentRequired}
	state.mu.Unlock()

	advice := adviceFor(t, state, []*Auth{a}, a.ID, "full")
	if advice.Action != "hold" || !strings.Contains(advice.Reason, "refusing this account") {
		t.Fatalf("advice = %+v, want hold: the provider refuses the account", advice)
	}
	state.SetResetMode("auto")
	if candidate := state.AutoResetCandidate([]*Auth{a}, routerTestNow); candidate != nil {
		t.Fatalf("automatic mode would use a reset on a refused account: %+v", candidate)
	}
}

// A fresh list of resets beside an old reading of the limit is not acted on:
// the limit may have cleared since.
func TestResetAdviceHoldsOnOldReadingOfTheLimit(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("codex-a.json", "codex", "a@example.com")
	old := routerTestNow.Add(-2 * time.Hour)
	setMeter(state, a.ID, "codex", Meter{Name: "primary", Utilization: 0.1, ResetAt: routerTestNow.Add(2 * time.Hour), Window: fiveHourWindow, ObservedAt: old})
	setMeter(state, a.ID, "codex", Meter{Name: "secondary", Utilization: 1, Status: "rejected", ResetAt: routerTestNow.Add(3 * 24 * time.Hour), Window: weeklyQuotaWindow, ObservedAt: old})
	state.ObserveResets(a.ID, "codex", &ResetStatus{ObservedAt: routerTestNow, Grants: []ResetGrant{{
		ID: "cred", Label: "Full reset", Total: 1, Left: 1, Clears: []string{"primary", "secondary"}, UsableNow: true, EndsAt: routerTestNow.Add(20 * 24 * time.Hour),
	}}})

	advice := adviceFor(t, state, []*Auth{a}, a.ID, "cred")
	if advice.Action != "hold" || !strings.Contains(advice.Reason, "No recent reading of the weekly limit") {
		t.Fatalf("advice = %+v, want hold: the week was last read two hours ago", advice)
	}
	state.SetResetMode("auto")
	if candidate := state.AutoResetCandidate([]*Auth{a}, routerTestNow); candidate != nil {
		t.Fatalf("automatic mode would use a credit on an old reading: %+v", candidate)
	}
}

// A limit about to reset by itself is not worth a reset.
func TestResetAdviceWaitsWhenLimitResetsSoon(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 3*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))

	advice := adviceFor(t, state, []*Auth{a}, a.ID, "full")
	if advice.Action != "wait" || !strings.Contains(advice.Reason, "resets by itself") {
		t.Fatalf("advice = %+v, want wait: the week resets in 3h", advice)
	}
}

// A reset that only works at a limit is held until the account gets there.
func TestResetAdviceHoldsUntilAccountIsAtLimit(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekOpen(state, a.ID, 0.6)
	observeTestResets(state, a.ID, resetTestGrant("full"))

	advice := adviceFor(t, state, []*Auth{a}, a.ID, "full")
	if advice.Action != "hold" || !strings.Contains(advice.Reason, "Only works once this account is at its weekly limit") {
		t.Fatalf("advice = %+v, want hold until the limit", advice)
	}
}

// A reset close to expiring is used while it still can be, even though other
// accounts have room: otherwise it is lost.
func TestResetAdviceUsesExpiringResetAtLimit(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a, b := routerTestAuth("a.json", "claude", "a@example.com"), routerTestAuth("b.json", "claude", "b@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	weekOpen(state, b.ID, 0.1)
	grant := resetTestGrant("full")
	grant.EndsAt = routerTestNow.Add(10 * time.Hour)
	observeTestResets(state, a.ID, grant)

	advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "full")
	if advice.Action != "use" || !strings.HasPrefix(advice.Reason, "Expires ") {
		t.Fatalf("advice = %+v, want use before it expires", advice)
	}
}

// A credit that works without a limit is used when it is about to expire and
// would give back most of the week, and held when it would give back little.
func TestResetAdviceExpiringCreditNeedsEnoughToGiveBack(t *testing.T) {
	for _, tc := range []struct {
		used float64
		want string
	}{{0.8, "use"}, {0.2, "hold"}} {
		state := newTestRoutingState(routerTestNow)
		a := routerTestAuth("codex-a.json", "codex", "a@example.com")
		setMeter(state, a.ID, "codex", Meter{Name: "primary", Utilization: 0.1, ResetAt: routerTestNow.Add(2 * time.Hour), Window: fiveHourWindow})
		setMeter(state, a.ID, "codex", Meter{Name: "secondary", Utilization: tc.used, ResetAt: routerTestNow.Add(3 * 24 * time.Hour), Window: weeklyQuotaWindow})
		state.ObserveResets(a.ID, "codex", &ResetStatus{ObservedAt: routerTestNow, Grants: []ResetGrant{{
			ID: "cred", Label: "Full reset", Total: 1, Left: 1, Clears: []string{"primary", "secondary"}, UsableNow: true, EndsAt: routerTestNow.Add(6 * time.Hour),
		}}})
		if advice := adviceFor(t, state, []*Auth{a}, a.ID, "cred"); advice.Action != tc.want {
			t.Fatalf("week %.0f%% used: advice = %+v, want %s", tc.used*100, advice, tc.want)
		}
	}
}

// A Fable-only reset looks at who else has Fable left, not at the shared week.
func TestResetAdviceFableResetLooksAtFableOnOtherAccounts(t *testing.T) {
	fableGrant := ResetGrant{
		ID: "fable", Label: "One Fable reset", Total: 1, Left: 1, Clears: []string{claudeFableClaim},
		UsableNow: true, RequiresLimit: true, GivesBackWeekly: true, EndsAt: routerTestNow.Add(60 * 24 * time.Hour),
	}
	fableSpent := func(state *RoutingState, authID string) {
		weekOpen(state, authID, 0.5)
		setMeter(state, authID, "claude", Meter{Name: claudeFableClaim, Utilization: 1, ResetAt: routerTestNow.Add(5 * 24 * time.Hour), Status: "rejected"})
	}
	a, b := routerTestAuth("a.json", "claude", "a@example.com"), routerTestAuth("b.json", "claude", "b@example.com")

	state := newTestRoutingState(routerTestNow)
	fableSpent(state, a.ID)
	weekOpen(state, b.ID, 0.3)
	setMeter(state, b.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 0.4, ResetAt: routerTestNow.Add(5 * 24 * time.Hour), Status: "allowed"})
	observeTestResets(state, a.ID, fableGrant)
	if advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "fable"); advice.Action != "hold" || !strings.Contains(advice.Reason, "1 other account still has Fable left") {
		t.Fatalf("advice = %+v, want hold: b has Fable left", advice)
	}

	state = newTestRoutingState(routerTestNow)
	fableSpent(state, a.ID)
	fableSpent(state, b.ID)
	observeTestResets(state, a.ID, fableGrant)
	if advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "fable"); advice.Action != "use" || !strings.Contains(advice.Reason, "No account has Fable left") {
		t.Fatalf("advice = %+v, want use: nobody has Fable left", advice)
	}
}

// When the other accounts will run out before any limit comes back, the reset
// is used now, before work stops.
func TestResetAdviceUsesWhenOthersRunOutBeforeRelief(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a, b := routerTestAuth("a.json", "claude", "a@example.com"), routerTestAuth("b.json", "claude", "b@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	weekOpen(state, b.ID, 0.9)
	// b burned 30% of its week in the last three hours: 10% left lasts one hour.
	state.mu.Lock()
	state.accounts[b.ID].History["7d"] = []MeterSample{
		{At: routerTestNow.Add(-3 * time.Hour), Utilization: 0.6},
		{At: routerTestNow.Add(-time.Minute), Utilization: 0.9},
	}
	state.mu.Unlock()
	observeTestResets(state, a.ID, resetTestGrant("full"))

	advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "full")
	if advice.Action != "use" || !strings.Contains(advice.Reason, "The other accounts run out") {
		t.Fatalf("advice = %+v, want use: b runs out long before a's week resets", advice)
	}
}

func TestResetAdviceForUsedStaleAndBlockedResets(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	used := resetTestGrant("used")
	used.Left = 0
	blocked := resetTestGrant("blocked")
	blocked.UsableNow, blocked.Unavailable = false, "cooldown"
	observeTestResets(state, a.ID, used, blocked)
	if advice := adviceFor(t, state, []*Auth{a}, a.ID, "used"); advice.Action != "none" || advice.Reason != "Used" {
		t.Fatalf("used reset advice = %+v", advice)
	}
	if advice := adviceFor(t, state, []*Auth{a}, a.ID, "blocked"); advice.Action != "hold" || !strings.Contains(advice.Reason, "wait before another reset") {
		t.Fatalf("blocked reset advice = %+v", advice)
	}

	state.ObserveResets(a.ID, "claude", &ResetStatus{Grants: []ResetGrant{resetTestGrant("old")}, ObservedAt: routerTestNow.Add(-2 * time.Hour)})
	if advice := adviceFor(t, state, []*Auth{a}, a.ID, "old"); advice.Action != "none" || !strings.Contains(advice.Reason, "No recent reading") {
		t.Fatalf("stale reading advice = %+v, want none", advice)
	}
}

// Two accounts at their weekly limit with a reset each: one use changes what
// the pool can take, so only the better one is suggested.
func TestResetSuggestionsOfferOneSharedResetAtATime(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a, b := routerTestAuth("a.json", "claude", "a@example.com"), routerTestAuth("b.json", "claude", "b@example.com")
	weekSpent(state, a.ID, 2*24*time.Hour)
	weekSpent(state, b.ID, 5*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full-a"))
	observeTestResets(state, b.ID, resetTestGrant("full-b"))

	suggestions := state.ResetSuggestions([]*Auth{a, b}, routerTestNow)
	if len(suggestions) != 1 || suggestions[0].AuthID != b.ID {
		t.Fatalf("suggestions = %+v, want only b, whose week is furthest from resetting", suggestions)
	}
	if advice := adviceFor(t, state, []*Auth{a, b}, a.ID, "full-a"); advice.Action != "hold" || !strings.Contains(advice.Reason, "b@example.com goes first") {
		t.Fatalf("a's advice = %+v, want hold behind b", advice)
	}
}

// Two resets on one account that clear the same limit: the narrower one is
// suggested and the broader one kept.
func TestResetAdvicePrefersNarrowestGrant(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	narrow := resetTestGrant("week-only")
	narrow.Clears = []string{"7d"}
	observeTestResets(state, a.ID, resetTestGrant("full"), narrow)

	if advice := adviceFor(t, state, []*Auth{a}, a.ID, "week-only"); advice.Action != "use" {
		t.Fatalf("narrow advice = %+v, want use", advice)
	}
	if advice := adviceFor(t, state, []*Auth{a}, a.ID, "full"); advice.Action != "hold" || !strings.Contains(advice.Reason, "Another reset on this account goes first") {
		t.Fatalf("broad advice = %+v, want hold", advice)
	}
}

// fakeResetBackend records what the routing state asks of the providers.
type fakeResetBackend struct {
	mu         sync.Mutex
	result     ResetClaimResult
	err        error
	requestIDs []string
	refreshes  int
	afters     int
	// onRefresh runs inside RefreshResets, to change the reading.
	onRefresh func()
	// block, when set, holds ClaimReset until it is closed.
	block   chan struct{}
	started chan struct{}
}

func (f *fakeResetBackend) ClaimReset(_ context.Context, _ *Auth, _ ResetGrant, requestID string) (ResetClaimResult, error) {
	f.mu.Lock()
	f.requestIDs = append(f.requestIDs, requestID)
	block, started := f.block, f.started
	result, err := f.result, f.err
	f.mu.Unlock()
	if block != nil {
		close(started)
		<-block
	}
	return result, err
}

func (f *fakeResetBackend) RefreshResets(context.Context, *Auth) error {
	f.mu.Lock()
	f.refreshes++
	onRefresh := f.onRefresh
	f.mu.Unlock()
	if onRefresh != nil {
		onRefresh()
	}
	return nil
}

func (f *fakeResetBackend) AfterReset(context.Context, *Auth) {
	f.mu.Lock()
	f.afters++
	f.mu.Unlock()
}

func TestUseResetClearsLimitAndRecordsIt(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))
	left := 0
	backend := &fakeResetBackend{result: ResetClaimResult{Result: "reset", Left: &left, Cleared: []string{"7d"}}}
	state.SetResetBackend(backend)

	event, errUse := state.UseReset(context.Background(), a, "full", "you")
	if errUse != nil {
		t.Fatalf("UseReset: %v", errUse)
	}
	if event.Result != "reset" || event.By != "you" || event.Account != "a@example.com" || event.Provider != "claude" || event.Label != "One full limit reset" {
		t.Fatalf("event = %+v", event)
	}
	account := state.account(a.ID)
	if got := account.Meters["7d"]; got.Utilization != 0 || got.Status != "allowed" {
		t.Fatalf("week after reset = %+v, want cleared", got)
	}
	if account.Resets.Grants[0].Left != 0 || account.Resets.Grants[0].UsableNow {
		t.Fatalf("grant after reset = %+v, want none left", account.Resets.Grants[0])
	}
	if backend.afters != 1 || backend.refreshes != 1 {
		t.Fatalf("afters = %d, refreshes = %d, want the cooldown lifted and one fresh reading", backend.afters, backend.refreshes)
	}
	if events := state.ResetEvents(); len(events) != 1 || events[0].GrantID != "full" {
		t.Fatalf("events = %+v", events)
	}
	if view := state.Dashboard([]*Auth{a}, routerTestNow); len(view.ResetEvents) != 1 || view.ResetMode != ResetModeOff {
		t.Fatalf("dashboard reset events = %+v mode %q", view.ResetEvents, view.ResetMode)
	}
}

// A use that got no answer keeps its request ID, so trying again cannot spend
// a second reset if the first one did go through.
func TestUseResetRetriesWithTheSameRequestID(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))
	backend := &fakeResetBackend{err: errors.New("connection reset")}
	state.SetResetBackend(backend)

	event, errUse := state.UseReset(context.Background(), a, "full", "you")
	if errUse == nil || event.Result != "error" || !strings.Contains(event.Text, "Could not reach the provider") {
		t.Fatalf("event = %+v, err = %v, want a recorded failure", event, errUse)
	}
	if account := state.account(a.ID); account.Resets.Grants[0].Left != 1 || account.Meters["7d"].Utilization != 1 {
		t.Fatal("a failed use must leave the grant and the limit as they were")
	}
	backend.mu.Lock()
	backend.err, backend.result = nil, ResetClaimResult{Result: "reset"}
	backend.mu.Unlock()
	if _, errUse = state.UseReset(context.Background(), a, "full", "you"); errUse != nil {
		t.Fatalf("retry: %v", errUse)
	}
	if len(backend.requestIDs) != 2 || backend.requestIDs[0] == "" || backend.requestIDs[0] != backend.requestIDs[1] {
		t.Fatalf("request IDs = %v, want the retry to reuse the first", backend.requestIDs)
	}
	if account := state.account(a.ID); account.Resets.Grants[0].Left != 0 || account.Meters["7d"].Utilization != 0 {
		t.Fatal("the retry that went through must clear the limit and use up the grant")
	}
}

func TestUseResetRefusesUnknownGrantAndSecondUseInFlight(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"), resetTestGrant("other"))
	backend := &fakeResetBackend{result: ResetClaimResult{Result: "reset"}, block: make(chan struct{}), started: make(chan struct{})}
	state.SetResetBackend(backend)

	if _, errUse := state.UseReset(context.Background(), a, "missing", "you"); !errors.Is(errUse, ErrResetUnknown) {
		t.Fatalf("unknown grant: err = %v", errUse)
	}
	done := make(chan error, 1)
	go func() {
		_, errUse := state.UseReset(context.Background(), a, "full", "you")
		done <- errUse
	}()
	<-backend.started
	if _, errUse := state.UseReset(context.Background(), a, "other", "you"); !errors.Is(errUse, ErrResetBusy) {
		t.Fatalf("second use while one is in flight: err = %v, want busy", errUse)
	}
	close(backend.block)
	if errUse := <-done; errUse != nil {
		t.Fatalf("first use: %v", errUse)
	}
	if len(backend.requestIDs) != 1 {
		t.Fatalf("provider saw %d uses, want one", len(backend.requestIDs))
	}
}

// A provider's "no" is recorded, uses nothing up locally beyond what it says,
// and clears no limit.
func TestUseResetRecordsRefusal(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))
	backend := &fakeResetBackend{result: ResetClaimResult{Result: "already_used"}}
	state.SetResetBackend(backend)

	event, errUse := state.UseReset(context.Background(), a, "full", "you")
	if errUse != nil || event.Result != "already_used" || event.Text != "This reset was already used" {
		t.Fatalf("event = %+v, err = %v", event, errUse)
	}
	account := state.account(a.ID)
	if account.Meters["7d"].Utilization != 1 || account.Resets.Grants[0].Left != 0 || backend.afters != 0 {
		t.Fatalf("after a refusal: week %+v, grant %+v, afters %d", account.Meters["7d"], account.Resets.Grants[0], backend.afters)
	}
}

func autoResetTestState(t *testing.T) (*RoutingState, *Auth, *fakeResetBackend, *time.Time) {
	t.Helper()
	now := routerTestNow
	state := NewRoutingState()
	state.nowFunc = func() time.Time { return now }
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))
	backend := &fakeResetBackend{result: ResetClaimResult{Result: "reset"}}
	state.SetResetBackend(backend)
	return state, a, backend, &now
}

func TestAutoResetOnlyRunsInAutoMode(t *testing.T) {
	state, a, backend, _ := autoResetTestState(t)
	if event := state.RunAutoReset(context.Background(), []*Auth{a}); event != nil || len(backend.requestIDs) != 0 {
		t.Fatalf("mode off used a reset: %+v", event)
	}
	state.SetResetMode("auto")
	event := state.RunAutoReset(context.Background(), []*Auth{a})
	if event == nil || event.Result != "reset" || event.By != "auto" {
		t.Fatalf("event = %+v, want an automatic use", event)
	}
	// One reading before the use, one after.
	if backend.refreshes != 2 || len(backend.requestIDs) != 1 {
		t.Fatalf("refreshes = %d, uses = %d", backend.refreshes, len(backend.requestIDs))
	}
}

// Automatic mode reads the account again first. When the fresh reading no
// longer says to use the reset, nothing is used.
func TestAutoResetStopsWhenFreshReadingDisagrees(t *testing.T) {
	state, a, backend, _ := autoResetTestState(t)
	state.SetResetMode("auto")
	backend.onRefresh = func() {
		// The week reset by itself in the meantime.
		weekOpen(state, a.ID, 0.05)
	}
	if event := state.RunAutoReset(context.Background(), []*Auth{a}); event != nil || len(backend.requestIDs) != 0 {
		t.Fatalf("used a reset the fresh reading no longer called for: %+v", event)
	}
}

// One automatic use per provider per half hour, and a reset the provider
// turned down is left alone for hours, so a wrong reading cannot burn through
// every reset.
func TestAutoResetIsRateLimited(t *testing.T) {
	state, a, backend, now := autoResetTestState(t)
	b := routerTestAuth("b.json", "claude", "b@example.com")
	weekSpent(state, b.ID, 5*24*time.Hour)
	observeTestResets(state, b.ID, resetTestGrant("full-b"))
	state.SetResetMode("auto")
	auths := []*Auth{a, b}
	keepFresh := func() {
		for _, id := range []string{a.ID, b.ID} {
			state.mu.Lock()
			state.accounts[id].Resets.ObservedAt = *now
			for _, meter := range state.accounts[id].Meters {
				meter.ObservedAt = *now
			}
			state.mu.Unlock()
		}
	}

	first := state.RunAutoReset(context.Background(), auths)
	if first == nil || first.AuthID != b.ID {
		t.Fatalf("first automatic use = %+v, want b", first)
	}
	*now = now.Add(10 * time.Minute)
	weekSpent(state, b.ID, 5*24*time.Hour)
	keepFresh()
	if got := state.ResetSuggestions(auths, *now); len(got) != 1 || got[0].AuthID != a.ID {
		t.Fatalf("suggestions = %+v, want a's reset now that b is at its limit again", got)
	}
	if event := state.RunAutoReset(context.Background(), auths); event != nil {
		t.Fatalf("second automatic use ten minutes later: %+v", event)
	}
	// b's reset went through, so b has room and a's reset is held. Once b is
	// at its limit again, a's reset is the next one worth using.
	*now = now.Add(25 * time.Minute)
	weekSpent(state, b.ID, 5*24*time.Hour)
	keepFresh()
	backend.mu.Lock()
	backend.result = ResetClaimResult{Result: "cooldown"}
	backend.mu.Unlock()
	second := state.RunAutoReset(context.Background(), auths)
	if second == nil || second.AuthID != a.ID || second.Result != "cooldown" {
		t.Fatalf("after the gap: %+v, want a try on a that the provider turns down", second)
	}
	// The grant still shows one left, and the gap has passed again, but the
	// provider said no: it is not tried again for hours.
	state.mu.Lock()
	state.accounts[a.ID].Resets.Grants[0].Left, state.accounts[a.ID].Resets.Grants[0].UsableNow = 1, true
	state.mu.Unlock()
	*now = now.Add(45 * time.Minute)
	keepFresh()
	if event := state.RunAutoReset(context.Background(), auths); event != nil {
		t.Fatalf("retried a reset the provider turned down: %+v", event)
	}
	*now = now.Add(7 * time.Hour)
	keepFresh()
	weekSpent(state, a.ID, 3*24*time.Hour)
	keepFresh()
	if event := state.RunAutoReset(context.Background(), auths); event == nil || event.AuthID != a.ID {
		t.Fatalf("after the retry gap: %+v, want another try on a", event)
	}
}

func TestResetReadingIsAskedOncePerProbeAge(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	if !state.NeedsResetReading("a.json", routerTestNow, 20*time.Minute) {
		t.Fatal("an account never read needs a reading")
	}
	// The provider gave no answer: the account is not asked again every round.
	state.MarkResetProbe("a.json", routerTestNow)
	if state.NeedsResetReading("a.json", routerTestNow.Add(5*time.Minute), 20*time.Minute) {
		t.Fatal("asked again five minutes after a failed read")
	}
	if !state.NeedsResetReading("a.json", routerTestNow.Add(21*time.Minute), 20*time.Minute) {
		t.Fatal("not asked again after the probe age")
	}
}

func TestResetStateSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/routing-state.json"
	state := newTestRoutingState(routerTestNow)
	state.Configure(path)
	a := routerTestAuth("a.json", "claude", "a@example.com")
	weekSpent(state, a.ID, 4*24*time.Hour)
	observeTestResets(state, a.ID, resetTestGrant("full"))
	state.SetResetBackend(&fakeResetBackend{result: ResetClaimResult{Result: "not_limited"}})
	if _, errUse := state.UseReset(context.Background(), a, "full", "you"); errUse != nil {
		t.Fatalf("UseReset: %v", errUse)
	}
	if errFlush := state.Flush(); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}

	restored := newTestRoutingState(routerTestNow)
	restored.Configure(path)
	if account := restored.account(a.ID); account == nil || account.Resets == nil || len(account.Resets.Grants) != 1 {
		t.Fatalf("restored account = %+v, want its resets", account)
	}
	if events := restored.ResetEvents(); len(events) != 1 || events[0].Result != "not_limited" {
		t.Fatalf("restored events = %+v", events)
	}
}
