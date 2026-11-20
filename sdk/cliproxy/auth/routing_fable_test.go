package auth

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The Fable allowance arrives as the "7d_oi" claim. It covers Fable (and
// Mythos, the same model) and nothing else, not even a model-less ranking.
func TestFableClaimCoversOnlyFableModels(t *testing.T) {
	fable := &Meter{Name: claudeFableClaim}
	for _, model := range []string{"claude-fable-5-2", "claude-fable-5-1", "claude-mythos-5-2"} {
		if !meterAppliesToModel("claude", fable, model) {
			t.Fatalf("the Fable allowance must cover %s", model)
		}
	}
	for _, model := range []string{"claude-opus-5-5", "claude-haiku-5-5", ""} {
		if meterAppliesToModel("claude", fable, model) {
			t.Fatalf("the Fable allowance must not cover %q", model)
		}
	}
	if got := meterTitle("claude", &Meter{Name: claudeFableClaim, Window: weeklyQuotaWindow}); got != "Fable week" {
		t.Fatalf("title = %q, want Fable week", got)
	}
}

func TestFableHeadersFillTheFableMeter(t *testing.T) {
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.3")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", unixText(routerTestNow.Add(48*time.Hour)))
	state := newTestRoutingState(routerTestNow)
	state.observeResult(Result{AuthID: "a", Provider: "claude", Model: "claude-fable-5-2"}, headers, routerTestNow)
	meter, spent := state.modelAllowanceSpent("a", "claude", "claude-fable-5-2", routerTestNow)
	if !spent || !meter.ResetAt.Equal(routerTestNow.Add(48*time.Hour)) {
		t.Fatalf("spent = %v, meter = %+v; want the Fable week spent until its reset", spent, meter)
	}
	if _, spentOpus := state.modelAllowanceSpent("a", "claude", "claude-opus-5-5", routerTestNow); spentOpus {
		t.Fatal("a spent Fable week must not count against Opus")
	}
	if _, spentLater := state.modelAllowanceSpent("a", "claude", "claude-fable-5-2", routerTestNow.Add(49*time.Hour)); spentLater {
		t.Fatal("the Fable week must be open again after its reset")
	}
}

func TestScoredSelectorSkipsAccountOutOfFableWeek(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	spent := routerTestAuth("a-spent", "claude", "spent@example.com")
	open := routerTestAuth("b-open", "claude", "open@example.com")
	reset := routerTestNow.Add(72 * time.Hour)
	// The spent account would otherwise win: it has more of its week at risk.
	setMeter(state, spent.ID, "claude", Meter{Name: "7d", Utilization: 0.1, ResetAt: reset})
	setMeter(state, spent.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 0.99, ResetAt: reset})
	setMeter(state, open.ID, "claude", Meter{Name: "7d", Utilization: 0.6, ResetAt: reset})
	setMeter(state, open.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 0.2, ResetAt: reset})
	scorer := NewScoredSelector(ScorerConfig{}, state)
	auths := []*Auth{spent, open}

	fable := scorer.rankAvailable("claude-fable-5-2", auths, routerTestNow)
	if got := bestCandidate(auths, fable); got.ID != open.ID {
		t.Fatalf("Fable went to %s, want the account with Fable allowance left", got.ID)
	}
	if last := fable[len(fable)-1]; last.AuthID != spent.ID || !last.Excluded || !strings.Contains(last.Summary, "Fable week used up") {
		t.Fatalf("spent account = %+v", last)
	}
	opus := scorer.rankAvailable("claude-opus-5-5", auths, routerTestNow)
	if got := bestCandidate(auths, opus); got.ID != spent.ID {
		t.Fatalf("Opus went to %s, want the account with more of its week at risk", got.ID)
	}
}

func TestSoonestResetSkipsAccountOutOfFableWeek(t *testing.T) {
	state := newTestRoutingState(routerTestNow)
	spent := routerTestAuth("a-spent", "claude", "spent@example.com")
	open := routerTestAuth("b-open", "claude", "open@example.com")
	setMeter(state, spent.ID, "claude", Meter{Name: claudeFableClaim, Status: "rejected", Utilization: 1, ResetAt: routerTestNow.Add(time.Hour)})
	live := &SoonestResetSelector{nowFunc: func() time.Time { return routerTestNow }, state: state}
	picked, err := live.Pick(context.Background(), "claude", "claude-fable-5-2", cliproxyexecutor.Options{}, []*Auth{spent, open})
	if err != nil || picked.ID != open.ID {
		t.Fatalf("picked %v, %v; want the account with Fable allowance left", picked, err)
	}
	picked, _ = live.Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, []*Auth{spent, open})
	if picked.ID != spent.ID {
		t.Fatalf("Opus picked %s, want the first account: its Fable week does not matter", picked.ID)
	}
}

func TestParseClaudeUsageMetersReadsTheFableRow(t *testing.T) {
	body := []byte(`{"five_hour":{"utilization":2.0,"resets_at":"2026-10-06T16:39:59+00:00"},"seven_day":{"utilization":40,"resets_at":"2026-10-11T08:00:00+00:00"},"limits":[` +
		`{"kind":"session","group":"session","percent":2,"resets_at":"2026-10-06T16:39:59+00:00"},` +
		`{"kind":"weekly_all","group":"weekly","percent":40,"resets_at":"2026-10-11T08:00:00+00:00"},` +
		`{"kind":"weekly_scoped","group":"weekly","percent":100,"resets_at":"2026-10-11T08:00:00+00:00","scope":{"model":{"display_name":"Fable"}}},` +
		`{"kind":"weekly_scoped","group":"weekly","percent":12,"resets_at":null,"scope":{"model":{"display_name":"Sonnet"}}},` +
		`{"kind":"weekly_scoped","group":"weekly","percent":50,"scope":{"model":{"display_name":"Something new"}}}]}`)
	meters := ParseClaudeUsageMeters(body, routerTestNow)
	if len(meters) != 4 {
		t.Fatalf("meters = %v, want 5h, 7d, the Fable week and 7d_sonnet", meters)
	}
	fable := meters[claudeFableClaim]
	if fable == nil || fable.Utilization != 1 || fable.Status != "rejected" || fable.Window != weeklyQuotaWindow ||
		!fable.ResetAt.Equal(time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("Fable week = %+v", fable)
	}
	if sonnet := meters["7d_sonnet"]; sonnet == nil || math.Abs(sonnet.Utilization-0.12) > 1e-9 {
		t.Fatalf("7d_sonnet = %+v", sonnet)
	}
	nested := ParseClaudeUsageMeters([]byte(`{"limits":{"limits":[{"kind":"weekly_scoped","percent":30,"scope":{"model":{"display_name":"Fable"}}}]}}`), routerTestNow)
	if got := nested[claudeFableClaim]; got == nil || math.Abs(got.Utilization-0.3) > 1e-9 {
		t.Fatalf("nested limits = %v", nested)
	}
}

// A busy account's 5h and weekly meters stay fresh from its responses, but
// only a usage report or a Fable response tells the proxy its Fable week.
func TestBusyAccountIsReadForItsFableWeek(t *testing.T) {
	now := routerTestNow
	maxAge := 20 * time.Minute
	state := newTestRoutingState(now)
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.1")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.3")
	state.observeResult(Result{AuthID: "busy", Provider: "claude", Model: "claude-opus-5-5", Success: true}, headers, now)
	if !state.NeedsMeterReading("busy", now.Add(time.Minute), maxAge) {
		t.Fatal("an account with no Fable reading must be read")
	}
	if state.NeedsMeterReading("busy", now.Add(2*time.Minute), maxAge) {
		t.Fatal("the Fable read must not repeat within maxAge, even when it found no Fable row")
	}
	state.observeResult(Result{AuthID: "busy", Provider: "claude", Model: "claude-opus-5-5", Success: true}, headers, now.Add(25*time.Minute))
	if !state.NeedsMeterReading("busy", now.Add(26*time.Minute), maxAge) {
		t.Fatal("the Fable read must repeat once maxAge has passed")
	}
	state.ObserveMeters("busy", "claude", map[string]*Meter{claudeFableClaim: {Name: claudeFableClaim, Utilization: 0.5}}, now.Add(26*time.Minute))
	state.observeResult(Result{AuthID: "busy", Provider: "claude", Model: "claude-opus-5-5", Success: true}, headers, now.Add(40*time.Minute))
	if state.NeedsMeterReading("busy", now.Add(40*time.Minute), maxAge) {
		t.Fatal("a fresh Fable reading must not need another")
	}
}

// A chat pinned to an account keeps it until that account runs out of its
// Fable week; then the chat moves to an account with Fable allowance left and
// stays there. Chats on other models do not move.
func TestSessionAffinityMovesFableChatOffSpentAccount(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour})
	defer selector.Stop()
	first := &Auth{ID: "fable-move-a", Provider: "claude"}
	second := &Auth{ID: "fable-move-b", Provider: "claude"}
	auths := []*Auth{first, second}
	t.Cleanup(func() {
		defaultRoutingState.mu.Lock()
		delete(defaultRoutingState.accounts, first.ID)
		delete(defaultRoutingState.accounts, second.ID)
		defaultRoutingState.mu.Unlock()
	})
	pick := func(session, model string, offered ...*Auth) string {
		t.Helper()
		if len(offered) == 0 {
			offered = auths
		}
		opts := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {session}},
			Metadata: make(map[string]any),
		}
		picked, errPick := selector.Pick(context.Background(), "claude", model, opts, offered)
		if errPick != nil || picked == nil {
			t.Fatalf("pick %s %s: %v, %v", session, model, picked, errPick)
		}
		return picked.ID
	}
	const fable, opus = "claude-fable-5-2", "claude-opus-5-5"
	const opusSession = "chat-opus"
	// Both chats start while only the first account is signed in.
	fableHome, other := first.ID, second.ID
	if pick("chat-fable", fable, first) != fableHome || pick(opusSession, opus, first) != fableHome {
		t.Fatal("test setup: the chats did not start on the first account")
	}
	if pick("chat-fable", fable) != fableHome {
		t.Fatal("a chat must stay on its account while it has Fable allowance")
	}
	reset := time.Now().Add(72 * time.Hour)
	setMeter(defaultRoutingState, fableHome, "claude", Meter{Name: claudeFableClaim, Utilization: 1, Status: "rejected", ResetAt: reset})

	if got := pick("chat-fable", fable); got != other {
		t.Fatalf("the Fable chat stayed on %s, want it moved to %s", got, other)
	}
	if got := pick("chat-fable", fable); got != other {
		t.Fatalf("the moved Fable chat went back to %s", got)
	}
	if got := pick(opusSession, opus); got != fableHome {
		t.Fatalf("the Opus chat moved to %s, want it kept on %s", got, fableHome)
	}
	if got := pick("chat-fable-new", fable); got != other {
		t.Fatalf("a new Fable chat started on %s, want %s", got, other)
	}

	// With every account out of its Fable week nothing is gained by moving.
	setMeter(defaultRoutingState, other, "claude", Meter{Name: claudeFableClaim, Utilization: 0.99, ResetAt: reset})
	if got := pick("chat-fable", fable); got != other {
		t.Fatalf("with every account spent the chat moved to %s, want it kept on %s", got, other)
	}
}

// Clients that send no session ID are pinned by matching their conversation.
// That path moves a Fable chat off a spent account the same way.
func TestConversationMatchMovesFableChatOffSpentAccount(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour})
	defer selector.Stop()
	first := &Auth{ID: "fable-lcp-a", Provider: "claude"}
	second := &Auth{ID: "fable-lcp-b", Provider: "claude"}
	t.Cleanup(func() {
		defaultRoutingState.mu.Lock()
		delete(defaultRoutingState.accounts, first.ID)
		delete(defaultRoutingState.accounts, second.ID)
		defaultRoutingState.mu.Unlock()
	})
	const fable = "claude-fable-5-2"
	pick := func(scope, body string, offered ...*Auth) string {
		t.Helper()
		opts := cliproxyexecutor.Options{
			OriginalRequest: []byte(body),
			Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope},
		}
		picked, errPick := selector.Pick(context.Background(), "claude", fable, opts, offered)
		if errPick != nil || picked == nil {
			t.Fatalf("pick %s: %v, %v", scope, picked, errPick)
		}
		if _, matched := opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string); !matched {
			t.Fatalf("pick %s did not go through conversation matching: %#v", scope, opts.Metadata)
		}
		return picked.ID
	}
	turn1 := `{"messages":[{"role":"system","content":"fable-lcp-sys"},{"role":"user","content":"first question"}]}`
	turn2 := `{"messages":[{"role":"system","content":"fable-lcp-sys"},{"role":"user","content":"first question"},{"role":"assistant","content":"an answer"},{"role":"user","content":"second question"}]}`
	turn3 := `{"messages":[{"role":"system","content":"fable-lcp-sys"},{"role":"user","content":"first question"},{"role":"assistant","content":"an answer"},{"role":"user","content":"second question"},{"role":"assistant","content":"another"},{"role":"user","content":"third question"}]}`
	if pick("scope-a", turn1, first) != first.ID {
		t.Fatal("test setup: the chat did not start on the first account")
	}
	if got := pick("scope-a", turn2, first, second); got != first.ID {
		t.Fatalf("the chat moved to %s while its account had Fable allowance", got)
	}
	reset := time.Now().Add(72 * time.Hour)
	setMeter(defaultRoutingState, first.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 1, Status: "rejected", ResetAt: reset})
	if got := pick("scope-a", turn3, first, second); got != second.ID {
		t.Fatalf("the Fable chat stayed on %s, want it moved to %s", got, second.ID)
	}
	if got := pick("scope-b", `{"messages":[{"role":"system","content":"fable-lcp-other"},{"role":"user","content":"a new chat"}]}`, first, second); got != second.ID {
		t.Fatalf("a new Fable chat started on %s, want %s", got, second.ID)
	}
}

// fableRoutingTestAuths registers cleanup for accounts a test writes into the
// process-wide routing state.
func fableRoutingTestAuths(t *testing.T, auths ...*Auth) {
	t.Helper()
	t.Cleanup(func() {
		defaultRoutingState.mu.Lock()
		for _, auth := range auths {
			delete(defaultRoutingState.accounts, auth.ID)
		}
		defaultRoutingState.mu.Unlock()
	})
}

func refuseForTest(authID string) {
	defaultRoutingState.mu.Lock()
	defer defaultRoutingState.mu.Unlock()
	account := defaultRoutingState.accounts[authID]
	if account == nil {
		account = &accountRouting{Provider: "claude", Meters: map[string]*Meter{}, History: map[string][]MeterSample{}}
		defaultRoutingState.accounts[authID] = account
	}
	account.Health = AccountHealth{LastFailureAt: time.Now().Add(-time.Minute), LastFailureStatus: http.StatusPaymentRequired}
}

// An account the provider has just refused cannot take over a Fable chat: with
// one account nearly out of its Fable week and the other refused, every way a
// request is routed stays on the account that still answers.
func TestFableChatDoesNotMoveToRefusedAccount(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
	defer selector.Stop()
	nearlyOut := &Auth{ID: "fable-refused-a", Provider: "claude"}
	refused := &Auth{ID: "fable-refused-b", Provider: "claude"}
	fableRoutingTestAuths(t, nearlyOut, refused)
	const fable = "claude-fable-5-2"
	pick := func(opts cliproxyexecutor.Options, offered ...*Auth) string {
		t.Helper()
		picked, errPick := selector.Pick(context.Background(), "claude", fable, opts, offered)
		if errPick != nil || picked == nil {
			t.Fatalf("pick: %v, %v", picked, errPick)
		}
		return picked.ID
	}
	session := func(id string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{Headers: map[string][]string{"X-Claude-Code-Session-Id": {id}}, Metadata: make(map[string]any)}
	}
	conversation := func(body string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{OriginalRequest: []byte(body), Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "refused-scope"}}
	}
	turn1 := `{"messages":[{"role":"system","content":"fable-refused-sys"},{"role":"user","content":"first question"}]}`
	turn2 := `{"messages":[{"role":"system","content":"fable-refused-sys"},{"role":"user","content":"first question"},{"role":"assistant","content":"an answer"},{"role":"user","content":"second question"}]}`
	if pick(session("refused-chat"), nearlyOut) != nearlyOut.ID || pick(conversation(turn1), nearlyOut) != nearlyOut.ID {
		t.Fatal("test setup: the chats did not start on the first account")
	}
	setMeter(defaultRoutingState, nearlyOut.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 0.985, ResetAt: time.Now().Add(72 * time.Hour)})
	refuseForTest(refused.ID)

	if got := pick(session("refused-chat"), nearlyOut, refused); got != nearlyOut.ID {
		t.Fatalf("the pinned chat moved to the refused account %s", got)
	}
	if got := pick(conversation(turn2), nearlyOut, refused); got != nearlyOut.ID {
		t.Fatalf("the conversation-matched chat moved to the refused account %s", got)
	}
	if got := pick(session("refused-new-chat"), nearlyOut, refused); got != nearlyOut.ID {
		t.Fatalf("a new chat started on the refused account %s", got)
	}
	if got := pick(cliproxyexecutor.Options{}, nearlyOut, refused); got != nearlyOut.ID {
		t.Fatalf("a request with no session went to the refused account %s", got)
	}
}

// A reserve account takes Fable chats once every rotation account is out of
// its Fable week, and a chat does not move between accounts that are all out.
func TestFableChatMovesAcrossPriorityTiersOnlyWhenItHelps(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour})
	defer selector.Stop()
	rotation := &Auth{ID: "fable-tier-rotation", Provider: "claude", Attributes: map[string]string{"priority": "0"}}
	reserve := &Auth{ID: "fable-tier-reserve", Provider: "claude", Attributes: map[string]string{"priority": "-1"}}
	fableRoutingTestAuths(t, rotation, reserve)
	const fable = "claude-fable-5-2"
	pick := func(id string, offered ...*Auth) string {
		t.Helper()
		opts := cliproxyexecutor.Options{Headers: map[string][]string{"X-Claude-Code-Session-Id": {id}}, Metadata: make(map[string]any)}
		picked, errPick := selector.Pick(context.Background(), "claude", fable, opts, offered)
		if errPick != nil || picked == nil {
			t.Fatalf("pick %s: %v, %v", id, picked, errPick)
		}
		return picked.ID
	}
	reset := time.Now().Add(72 * time.Hour)
	if pick("tier-chat", rotation, reserve) != rotation.ID {
		t.Fatal("test setup: a new chat must start on the rotation account")
	}
	if pick("tier-reserve-chat", reserve) != reserve.ID {
		t.Fatal("test setup: the second chat did not start on the reserve account")
	}
	setMeter(defaultRoutingState, rotation.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 1, Status: "rejected", ResetAt: reset})
	if got := pick("tier-chat", rotation, reserve); got != reserve.ID {
		t.Fatalf("the chat stayed on %s, want it moved to the reserve account with Fable left", got)
	}
	if got := pick("tier-new-chat", rotation, reserve); got != reserve.ID {
		t.Fatalf("a new chat started on %s, want the reserve account with Fable left", got)
	}
	setMeter(defaultRoutingState, reserve.ID, "claude", Meter{Name: claudeFableClaim, Utilization: 1, Status: "rejected", ResetAt: reset})
	if got := pick("tier-reserve-chat", rotation, reserve); got != reserve.ID {
		t.Fatalf("with every account out the chat moved to %s and lost its cache for nothing", got)
	}
}

// The same rule covers any week kept for one model family: an Opus chat moves
// off an account whose Opus week is used up, and a Fable chat there stays.
func TestOpusChatMovesOffAccountOutOfItsOpusWeek(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour})
	defer selector.Stop()
	first := &Auth{ID: "opus-week-a", Provider: "claude"}
	second := &Auth{ID: "opus-week-b", Provider: "claude"}
	fableRoutingTestAuths(t, first, second)
	pick := func(id, model string, offered ...*Auth) string {
		t.Helper()
		opts := cliproxyexecutor.Options{Headers: map[string][]string{"X-Claude-Code-Session-Id": {id}}, Metadata: make(map[string]any)}
		picked, errPick := selector.Pick(context.Background(), "claude", model, opts, offered)
		if errPick != nil || picked == nil {
			t.Fatalf("pick %s: %v, %v", id, picked, errPick)
		}
		return picked.ID
	}
	const fable, opus = "claude-fable-5-2", "claude-opus-5-5"
	if pick("opus-chat", opus, first) != first.ID || pick("fable-chat", fable, first) != first.ID {
		t.Fatal("test setup: the chats did not start on the first account")
	}
	setMeter(defaultRoutingState, first.ID, "claude", Meter{Name: "7d_opus", Utilization: 1, Status: "rejected", ResetAt: time.Now().Add(72 * time.Hour)})
	if got := pick("opus-chat", opus, first, second); got != second.ID {
		t.Fatalf("the Opus chat stayed on %s, want it moved to %s", got, second.ID)
	}
	if got := pick("fable-chat", fable, first, second); got != first.ID {
		t.Fatalf("the Fable chat moved to %s, want it kept on %s", got, first.ID)
	}
}
