package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// An idle Claude account has no reading until traffic reaches it. The probe
// reads its usage so the dashboard and router see its allowance at once.
func TestClaudeMeterProbeReadsIdleOAuthAccountsOnly(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Errorf("anthropic-beta = %q", r.Header.Get("anthropic-beta"))
		}
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":0.0,"resets_at":null},"seven_day":{"utilization":3.0,"resets_at":"2026-10-11T14:00:00+00:00"}}`))
	}))
	defer server.Close()
	previous := claudeUsageURL
	claudeUsageURL = server.URL
	defer func() { claudeUsageURL = previous }()

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	register := func(id string, disabled bool, token string) {
		auth := &coreauth.Auth{ID: id, Provider: "claude", Disabled: disabled, Metadata: map[string]any{"access_token": token}}
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
	register("probe-idle.json", false, "sk-ant-oat01-idle")
	register("probe-off.json", true, "sk-ant-oat01-off")
	register("probe-apikey.json", false, "sk-ant-api03-key")
	service := &Service{cfg: &config.Config{}, coreManager: manager}

	service.probeClaudeMeters(ctx)

	if len(seen) != 1 || seen[0] != "Bearer sk-ant-oat01-idle" {
		t.Fatalf("probed %v, want only the enabled OAuth account", seen)
	}
	state := coreauth.DefaultRoutingState()
	if state.NeedsMeterReading("probe-idle.json", time.Now(), meterProbeMaxAge) {
		t.Fatal("the probe did not record a reading")
	}
	// A fresh reading is not probed again.
	service.probeClaudeMeters(ctx)
	if len(seen) != 1 {
		t.Fatalf("probed again with a fresh reading: %v", seen)
	}
}

func TestClaudeProbeReleasesEarlyWeeklyReset(t *testing.T) {
	ctx := context.Background()
	m := coreauth.NewManager(nil, nil, nil)
	id := "early-weekly-reset.json"
	now := time.Now()
	deadline := now.Add(5 * 24 * time.Hour)
	_, err := m.Register(ctx, &coreauth.Auth{ID: id, Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat-test"}})
	if err != nil {
		t.Fatal(err)
	}
	retry := time.Until(deadline) + 8*time.Second
	m.MarkResult(ctx, coreauth.Result{AuthID: id, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: 429, Message: "weekly quota"}})
	held, _ := m.GetByID(id)
	if !held.Unavailable || held.Quota.Reason != "credential_quota" {
		t.Fatalf("setup failed: %+v", held.Quota)
	}
	// The proxy last saw a full week. Anthropic now reports 59% used before the scheduled reset.
	old := now.Add(-30 * time.Minute)
	coreauth.DefaultRoutingState().ObserveMeters(id, "claude", map[string]*coreauth.Meter{"7d": {Name: "7d", Status: "rejected", Utilization: 1, ResetAt: deadline, ObservedAt: old}}, old)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"seven_day":{"utilization":59,"resets_at":%q}}`, deadline.Format(time.RFC3339Nano))
	}))
	defer server.Close()
	previous := claudeUsageURL
	claudeUsageURL = server.URL
	defer func() { claudeUsageURL = previous }()
	s := &Service{cfg: &config.Config{}, coreManager: m}
	s.probeClaudeMeters(ctx)
	got, _ := m.GetByID(id)
	if got.Unavailable || got.Quota.Exceeded || got.ModelStates["claude-fable-5-2"].Unavailable {
		t.Fatal("fresh provider reading left the old weekly hold in place")
	}
}

// A model can stay in cooldown while its account serves other models. Its
// weekly usage reading is still fresh by the ordinary twenty-minute cadence.
func TestClaudeProbePicksUpResetDuringModelOnlyHold(t *testing.T) {
	ctx := context.Background()
	m := coreauth.NewManager(nil, nil, nil)
	id := "claude-model-only-refill.json"
	const model = "claude-fable-5-2"
	now := time.Now()
	deadline := now.Add(96 * time.Hour)
	held := &coreauth.ModelState{Unavailable: true, NextRetryAfter: deadline, UpdatedAt: now,
		Quota:     coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline},
		LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}}
	_, err := m.Register(ctx, &coreauth.Auth{ID: id, Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat-test"},
		ModelStates: map[string]*coreauth.ModelState{model: held, "claude-sonnet-5-5": {}}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := m.GetByID(id)
	if a.Quota.Exceeded {
		t.Fatal("fixture has an account-wide hold")
	}

	state := coreauth.DefaultRoutingState()
	old := now.Add(-5 * time.Minute)
	state.ObserveMeters(id, "claude", map[string]*coreauth.Meter{
		"7d":    {Name: "7d", Status: "rejected", Utilization: 1, ResetAt: deadline, ObservedAt: old},
		"7d_oi": {Name: "7d_oi", Status: "allowed", Utilization: .79, ResetAt: deadline, ObservedAt: old},
	}, old)
	state.MarkResetProbe(id, old)

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprintf(w, `{"seven_day":{"utilization":0,"resets_at":%q},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":0,"resets_at":%q}]}`, deadline.Format(time.RFC3339Nano), deadline.Format(time.RFC3339Nano))
	}))
	defer server.Close()
	previous := claudeUsageURL
	claudeUsageURL = server.URL
	defer func() { claudeUsageURL = previous }()
	s := &Service{cfg: &config.Config{}, coreManager: m}
	s.probeClaudeMeters(ctx)
	refreshed, _ := m.GetByID(id)
	if calls != 1 || refreshed.ModelStates[model].Quota.Exceeded {
		t.Fatalf("provider reads = %d, model held = %v", calls, refreshed.ModelStates[model].Quota.Exceeded)
	}
	s.probeClaudeMeters(ctx)
	if calls != 1 {
		t.Fatalf("duplicated usage probe after refill: %d", calls)
	}
}
