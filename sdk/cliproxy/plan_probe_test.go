package cliproxy

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestClaudePlanIsReadOnceADay(t *testing.T) {
	recorder, server := newResetTestServer(t, map[string]string{
		"GET /api/oauth/profile": `{"organization":{"subscription_created_at":"2026-10-27T14:57:02.378505Z","subscription_status":"active"}}`,
	})
	previous := claudeProfileURL
	claudeProfileURL = server.URL + "/api/oauth/profile"
	defer func() { claudeProfileURL = previous }()

	signedIn := &coreauth.Auth{ID: "plan-probe-a.json", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat-test"}}
	apiKey := &coreauth.Auth{ID: "plan-probe-key.json", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-api-test"}}
	codex := &coreauth.Auth{ID: "plan-probe-codex.json", Provider: "codex", Metadata: map[string]any{"access_token": "x"}}
	service := resetTestService(t, signedIn, apiKey, codex)

	service.probeClaudePlans(context.Background())
	service.probeClaudePlans(context.Background())
	if got := recorder.count("GET /api/oauth/profile"); got != 1 {
		t.Fatalf("profile read %d times in two rounds, want 1", got)
	}
	recorder.mu.Lock()
	header := recorder.headers["GET /api/oauth/profile"]
	recorder.mu.Unlock()
	if header.Get("Authorization") != "Bearer sk-ant-oat-test" || header.Get("anthropic-beta") == "" {
		t.Fatalf("profile request headers = %v", header)
	}
	facts, ok := coreauth.DefaultRoutingState().Plan(signedIn.ID)
	if !ok || !facts.Active() || facts.StartedAt.Format(time.RFC3339) != "2026-10-27T14:57:02Z" {
		t.Fatalf("facts = %+v, %v", facts, ok)
	}
	for _, other := range []string{apiKey.ID, codex.ID} {
		if _, found := coreauth.DefaultRoutingState().Plan(other); found {
			t.Fatalf("%s has no subscription sign-in and must not be read", other)
		}
	}
}

func TestClaudePlanProbeKeepsLastFactsWhenTheReadFails(t *testing.T) {
	recorder, server := newResetTestServer(t, map[string]string{})
	previous := claudeProfileURL
	claudeProfileURL = server.URL + "/api/oauth/profile"
	defer func() { claudeProfileURL = previous }()

	auth := &coreauth.Auth{ID: "plan-probe-fails.json", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat-test"}}
	service := resetTestService(t, auth)
	state := coreauth.DefaultRoutingState()
	started := time.Date(2026, 8, 13, 14, 21, 4, 0, time.UTC)
	// Facts two days old are due a reading.
	state.ObservePlan(auth.ID, &coreauth.PlanFacts{StartedAt: started, Status: "active", ObservedAt: time.Now().Add(-48 * time.Hour)})

	service.probeClaudePlans(context.Background())
	service.probeClaudePlans(context.Background())
	if got := recorder.count("GET /api/oauth/profile"); got != 1 {
		t.Fatalf("a failing account was asked %d times in two rounds, want 1", got)
	}
	facts, ok := state.Plan(auth.ID)
	if !ok || !facts.StartedAt.Equal(started) {
		t.Fatalf("a failed read replaced the last facts: %+v, %v", facts, ok)
	}
}
