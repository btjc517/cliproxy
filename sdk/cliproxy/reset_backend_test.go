package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// resetTestServer answers the provider endpoints the reset code calls and
// records each request.
type resetTestServer struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]map[string]string
	headers  map[string]http.Header
	answers  map[string]string
}

func newResetTestServer(t *testing.T, answers map[string]string) (*resetTestServer, *httptest.Server) {
	t.Helper()
	recorder := &resetTestServer{bodies: map[string]map[string]string{}, headers: map[string]http.Header{}, answers: answers}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		payload := map[string]string{}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &payload)
		}
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, key+"?"+r.URL.RawQuery)
		recorder.bodies[key] = payload
		recorder.headers[key] = r.Header.Clone()
		answer, ok := recorder.answers[key]
		recorder.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return recorder, server
}

func (s *resetTestServer) count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, request := range s.requests {
		if strings.HasPrefix(request, key+"?") {
			n++
		}
	}
	return n
}

func resetTestService(t *testing.T, auths ...*coreauth.Auth) *Service {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	state := coreauth.DefaultRoutingState()
	state.SetResetBackend(resetBackend{service: service})
	t.Cleanup(func() {
		state.SetResetBackend(nil)
		state.SetResetMode("off")
	})
	return service
}

func resetView(t *testing.T, auth *coreauth.Auth) coreauth.RouterAccountView {
	t.Helper()
	return coreauth.DefaultRoutingState().Dashboard([]*coreauth.Auth{auth}, time.Now()).Accounts[auth.ID]
}

// The probe reads a Claude account's reset grants with its usage, and a use
// from the dashboard reaches Anthropic with the grant and a request ID, then
// clears the limit locally.
func TestClaudeResetIsReadAndUsed(t *testing.T) {
	weekReset := time.Now().Add(4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	grantEnds := time.Now().Add(20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	usage := `{"five_hour":{"utilization":10.0,"resets_at":"` + time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339) + `"},
	  "seven_day":{"utilization":100.0,"resets_at":"` + weekReset + `"},
	  "cedar_ember":{"eligible":true,"at_limit":true,"grants":[{"id":"full-reset-test","label":"One full limit reset","resets_total":1,"resets_left":1,
	    "ends_at":"` + grantEnds + `","clears":["seven_day","seven_day_overage_included"],"usable_now":true,"use_requires_limit":true,"blocking":[]}]}}`
	recorder, server := newResetTestServer(t, map[string]string{
		"GET /usage": usage,
		"POST /organizations/org-1/reset_rate_limits": `{"result":"reset","resets_left":0,"cleared":["seven_day"]}`,
	})
	previousUsage, previousReset := claudeUsageURL, claudeResetURLFormat
	claudeUsageURL, claudeResetURLFormat = server.URL+"/usage", server.URL+"/organizations/%s/reset_rate_limits"
	defer func() { claudeUsageURL, claudeResetURLFormat = previousUsage, previousReset }()

	auth := &coreauth.Auth{ID: "reset-claude.json", Provider: "claude", Metadata: map[string]any{
		"access_token": "sk-ant-oat01-reset", "organization_uuid": "org-1", "email": "reset@example.com",
	}}
	service := resetTestService(t, auth)
	ctx := context.Background()

	service.probeClaudeMeters(ctx)
	if got := recorder.requests[0]; got != "GET /usage?"+claudeUsageResetQuery {
		t.Fatalf("usage request = %q, want the reset grants asked for", got)
	}
	view := resetView(t, auth)
	if len(view.Resets) != 1 || view.Resets[0].ID != "full-reset-test" || view.Resets[0].Left != 1 {
		t.Fatalf("resets after the probe = %+v", view.Resets)
	}
	if advice := view.Resets[0].Advice; advice.Action != "use" {
		t.Fatalf("advice = %+v, want use: the only account is at its weekly limit", advice)
	}
	// A second round does not ask again: the reading is fresh.
	service.probeClaudeMeters(ctx)
	if n := recorder.count("GET /usage"); n != 1 {
		t.Fatalf("usage was read %d times, want once", n)
	}

	event, errUse := coreauth.DefaultRoutingState().UseReset(ctx, auth, "full-reset-test", "you")
	if errUse != nil || event.Result != "reset" {
		t.Fatalf("UseReset: event = %+v, err = %v", event, errUse)
	}
	key := "POST /organizations/org-1/reset_rate_limits"
	body := recorder.bodies[key]
	if body["program"] != "cedar_ember" || body["grant_id"] != "full-reset-test" || len(body["request_id"]) < 16 {
		t.Fatalf("claim body = %v", body)
	}
	headers := recorder.headers[key]
	if headers.Get("Authorization") != "Bearer sk-ant-oat01-reset" || headers.Get("anthropic-beta") != "oauth-2025-04-20" || headers.Get("Content-Type") != "application/json" {
		t.Fatalf("claim headers = %v", headers)
	}
	// The use is followed by a fresh reading of the account.
	if n := recorder.count("GET /usage"); n != 2 {
		t.Fatalf("usage was read %d times, want a second read after the use", n)
	}
}

// A Codex reset credit is listed with the account's windows, and a use sends
// the credit and a request ID to the consume endpoint.
func TestCodexResetCreditIsReadAndUsed(t *testing.T) {
	expires := time.Now().Add(10 * time.Hour).UTC().Format(time.RFC3339)
	weekReset := time.Now().Add(3 * 24 * time.Hour).Unix()
	recorder, server := newResetTestServer(t, map[string]string{
		"GET /wham/rate-limit-reset-credits": `{"credits":[{"id":"cred_test","reset_type":"codex_rate_limits","status":"available","expires_at":"` + expires + `","title":"Full reset (Weekly + 5 hr)"}],"available_count":1}`,
		"GET /wham/usage": `{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_at":` + jsonInt(time.Now().Add(2*time.Hour).Unix()) + `},
		  "secondary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":` + jsonInt(weekReset) + `}}}`,
		"POST /wham/rate-limit-reset-credits/consume": `{"code":"reset","windows_reset":2}`,
	})
	previous := codexBackendURL
	codexBackendURL = server.URL
	defer func() { codexBackendURL = previous }()

	auth := &coreauth.Auth{ID: "reset-codex.json", Provider: "codex", Metadata: map[string]any{
		"access_token": "codex-token", "account_id": "acct-1", "email": "codex@example.com",
	}}
	apiKey := &coreauth.Auth{ID: "reset-codex-key.json", Provider: "codex", Metadata: map[string]any{"api_key": "sk-test"}}
	service := resetTestService(t, auth, apiKey)
	ctx := context.Background()

	service.probeCodexResets(ctx)
	if n := recorder.count("GET /wham/rate-limit-reset-credits"); n != 1 {
		t.Fatalf("credits were read %d times, want once: only the signed-in account has any", n)
	}
	headers := recorder.headers["GET /wham/rate-limit-reset-credits"]
	if headers.Get("Authorization") != "Bearer codex-token" || headers.Get("Chatgpt-Account-Id") != "acct-1" || headers.Get("Originator") != codexResetOriginator {
		t.Fatalf("credit headers = %v", headers)
	}
	view := resetView(t, auth)
	if len(view.Resets) != 1 || view.Resets[0].ID != "cred_test" || strings.Join(view.Resets[0].Clears, ",") != "primary,secondary" {
		t.Fatalf("resets after the probe = %+v", view.Resets)
	}
	if len(view.Meters) != 2 {
		t.Fatalf("meters after the probe = %+v, want the 5-hour window and the week", view.Meters)
	}
	service.probeCodexResets(ctx)
	if n := recorder.count("GET /wham/rate-limit-reset-credits"); n != 1 {
		t.Fatalf("credits were read again with a fresh reading: %d", n)
	}

	event, errUse := coreauth.DefaultRoutingState().UseReset(ctx, auth, "cred_test", "you")
	if errUse != nil || event.Result != "reset" {
		t.Fatalf("UseReset: event = %+v, err = %v", event, errUse)
	}
	body := recorder.bodies["POST /wham/rate-limit-reset-credits/consume"]
	if body["credit_id"] != "cred_test" || len(body["redeem_request_id"]) < 16 {
		t.Fatalf("consume body = %v", body)
	}
}

// When the credits can be read but the windows cannot, the read fails, so
// automatic mode does not go on to use a credit; the credits stay listed.
func TestCodexReadFailsWhenWindowsCannotBeRead(t *testing.T) {
	_, server := newResetTestServer(t, map[string]string{
		"GET /wham/rate-limit-reset-credits": `{"credits":[{"id":"cred_only","status":"available","title":"Full reset"}]}`,
	})
	previous := codexBackendURL
	codexBackendURL = server.URL
	defer func() { codexBackendURL = previous }()

	auth := &coreauth.Auth{ID: "reset-codex-no-usage.json", Provider: "codex", Metadata: map[string]any{"access_token": "codex-token", "account_id": "acct-3"}}
	service := resetTestService(t, auth)
	errRead := resetBackend{service: service}.RefreshResets(context.Background(), auth)
	if errRead == nil || !strings.Contains(errRead.Error(), "read usage windows") {
		t.Fatalf("RefreshResets err = %v, want the failed window read reported", errRead)
	}
	if view := resetView(t, auth); len(view.Resets) != 1 || view.Resets[0].ID != "cred_only" {
		t.Fatalf("resets = %+v, want the credit still listed", view.Resets)
	}
}

// An account the provider will not answer for is asked once per probe age,
// not every round.
func TestResetProbeDoesNotHammerAnAccountThatFails(t *testing.T) {
	recorder, server := newResetTestServer(t, map[string]string{})
	previous := codexBackendURL
	codexBackendURL = server.URL
	defer func() { codexBackendURL = previous }()

	auth := &coreauth.Auth{ID: "reset-codex-fails.json", Provider: "codex", Metadata: map[string]any{"access_token": "codex-token", "account_id": "acct-2"}}
	service := resetTestService(t, auth)
	service.probeCodexResets(context.Background())
	service.probeCodexResets(context.Background())
	if n := recorder.count("GET /wham/rate-limit-reset-credits"); n != 1 {
		t.Fatalf("a failing account was asked %d times in two rounds, want once", n)
	}
}

// Automatic mode off: the probe round never uses a reset.
func TestAutoResetDoesNothingWhenOff(t *testing.T) {
	recorder, server := newResetTestServer(t, map[string]string{})
	previous := claudeResetURLFormat
	claudeResetURLFormat = server.URL + "/organizations/%s/reset_rate_limits"
	defer func() { claudeResetURLFormat = previous }()

	auth := &coreauth.Auth{ID: "reset-auto-off.json", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-auto", "organization_uuid": "org-2"}}
	service := resetTestService(t, auth)
	state := coreauth.DefaultRoutingState()
	now := time.Now()
	state.ObserveMeters(auth.ID, "claude", map[string]*coreauth.Meter{
		"7d": {Name: "7d", Utilization: 1, Status: "rejected", ResetAt: now.Add(4 * 24 * time.Hour), Window: 7 * 24 * time.Hour, ObservedAt: now},
	}, now)
	state.ObserveResets(auth.ID, "claude", &coreauth.ResetStatus{ObservedAt: now, Grants: []coreauth.ResetGrant{{
		ID: "full", Total: 1, Left: 1, Clears: []string{"7d"}, UsableNow: true, RequiresLimit: true, EndsAt: now.Add(20 * 24 * time.Hour),
	}}})
	if got := state.ResetSuggestions([]*coreauth.Auth{auth}, now); len(got) != 1 {
		t.Fatalf("suggestions = %+v, want the reset suggested", got)
	}
	service.runAutoReset(context.Background())
	if len(recorder.requests) != 0 {
		t.Fatalf("a reset was used with automatic mode off: %v", recorder.requests)
	}
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// A Codex account held until its weekly reset is released when OpenAI reports
// the week refilled early, as happens when a reset credit is used at OpenAI
// and not through the proxy.
func TestCodexProbeReleasesHoldAfterEarlyRefill(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	deadline := now.Add(3 * 24 * time.Hour)
	_, server := newResetTestServer(t, map[string]string{
		"GET /wham/rate-limit-reset-credits": `{"credits":[]}`,
		"GET /wham/usage": `{"rate_limit":{"primary_window":{"used_percent":2,"limit_window_seconds":18000,"reset_at":` + jsonInt(now.Add(4*time.Hour).Unix()) + `},
		  "secondary_window":{"used_percent":5,"limit_window_seconds":604800,"reset_at":` + jsonInt(deadline.Unix()) + `}}}`,
	})
	previous := codexBackendURL
	codexBackendURL = server.URL
	defer func() { codexBackendURL = previous }()

	auth := &coreauth.Auth{ID: "codex-early-refill.json", Provider: "codex", Metadata: map[string]any{"access_token": "codex-token", "account_id": "acct-9"}}
	service := resetTestService(t, auth)
	manager := service.coreManager
	retry := time.Until(deadline)
	manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-test", RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: 429, Message: "usage limit reached"}})
	held, _ := manager.GetByID(auth.ID)
	if state := held.ModelStates["gpt-test"]; state == nil || !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("setup failed: %+v", held.ModelStates["gpt-test"])
	}
	// The proxy last saw a full week.
	old := now.Add(-30 * time.Minute)
	coreauth.DefaultRoutingState().ObserveMeters(auth.ID, "codex", map[string]*coreauth.Meter{
		"secondary": {Name: "secondary", Status: "rejected", Utilization: 1, ResetAt: deadline, Window: 7 * 24 * time.Hour, ObservedAt: old},
	}, old)

	service.probeCodexResets(ctx)

	got, _ := manager.GetByID(auth.ID)
	if state := got.ModelStates["gpt-test"]; state.Unavailable || state.Quota.Exceeded {
		t.Fatalf("the hold stayed after OpenAI reported the week refilled: %+v", state)
	}
}

// A Codex week that is still full keeps its hold.
func TestCodexProbeKeepsHoldWhileWeekIsFull(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	deadline := now.Add(3 * 24 * time.Hour)
	_, server := newResetTestServer(t, map[string]string{
		"GET /wham/rate-limit-reset-credits": `{"credits":[]}`,
		"GET /wham/usage":                    `{"rate_limit":{"secondary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":` + jsonInt(deadline.Unix()) + `}}}`,
	})
	previous := codexBackendURL
	codexBackendURL = server.URL
	defer func() { codexBackendURL = previous }()

	auth := &coreauth.Auth{ID: "codex-still-full.json", Provider: "codex", Metadata: map[string]any{"access_token": "codex-token", "account_id": "acct-10"}}
	service := resetTestService(t, auth)
	retry := time.Until(deadline)
	service.coreManager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-test", RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: 429, Message: "usage limit reached"}})
	old := now.Add(-30 * time.Minute)
	coreauth.DefaultRoutingState().ObserveMeters(auth.ID, "codex", map[string]*coreauth.Meter{
		"secondary": {Name: "secondary", Status: "rejected", Utilization: 1, ResetAt: deadline, Window: 7 * 24 * time.Hour, ObservedAt: old},
	}, old)

	service.probeCodexResets(ctx)

	got, _ := service.coreManager.GetByID(auth.ID)
	if state := got.ModelStates["gpt-test"]; !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("the hold was released while the week is still full: %+v", state)
	}
}

// A request that finishes on the account while its usage is being read makes
// the reader's copy of the account out of date. The read still records what
// the provider reported and still releases the hold, using the account as it
// stands now.
func TestUsageReadWithOutOfDateAccountStillRecordsAndReleases(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	deadline := now.Add(5 * 24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"seven_day":{"utilization":20,"resets_at":"` + deadline.UTC().Format(time.RFC3339) + `"},
		  "cedar_ember":{"eligible":true,"grants":[{"id":"full-after-race","label":"One full limit reset","resets_total":1,"resets_left":1,"clears":["seven_day"],"usable_now":true}]}}`))
	}))
	defer server.Close()
	previous := claudeUsageURL
	claudeUsageURL = server.URL
	defer func() { claudeUsageURL = previous }()

	auth := &coreauth.Auth{ID: "usage-read-race.json", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-race"}}
	service := resetTestService(t, auth)
	manager := service.coreManager
	retry := time.Until(deadline) + 5*time.Second
	manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: 429, Message: "weekly quota"}})
	old := now.Add(-30 * time.Minute)
	coreauth.DefaultRoutingState().ObserveMeters(auth.ID, "claude", map[string]*coreauth.Meter{
		"7d": {Name: "7d", Status: "rejected", Utilization: 1, ResetAt: deadline, ObservedAt: old},
	}, old)
	held, _ := manager.GetByID(auth.ID)
	if !held.Unavailable {
		t.Fatalf("setup failed: %+v", held.Quota)
	}
	// The reader holds a copy from before something else changed the account.
	outOfDate := held.Clone()
	outOfDate.Generation--
	outOfDate.UpdatedAt = outOfDate.UpdatedAt.Add(-time.Second)

	if errRead := service.readClaudeUsage(ctx, outOfDate); errRead != nil {
		t.Fatalf("readClaudeUsage: %v", errRead)
	}
	view := resetView(t, auth)
	if len(view.Resets) != 1 || view.Resets[0].ID != "full-after-race" {
		t.Fatalf("resets = %+v, want the provider's report recorded", view.Resets)
	}
	if len(view.Meters) != 1 || view.Meters[0].Utilization != 0.2 {
		t.Fatalf("meters = %+v, want the provider's reading recorded", view.Meters)
	}
	got, _ := manager.GetByID(auth.ID)
	if got.Unavailable || got.Quota.Exceeded {
		t.Fatalf("the hold stayed: %+v", got.Quota)
	}
}
