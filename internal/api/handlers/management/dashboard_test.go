package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDashboardAccountMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry gin.H
		want  string
	}{
		{"disabled wins over priority", gin.H{"disabled": true, "priority": -1}, "off"},
		{"negative priority is reserve", gin.H{"disabled": false, "priority": -5}, "reserve"},
		{"zero priority rotates", gin.H{"disabled": false, "priority": 0}, "rotation"},
		{"positive priority rotates", gin.H{"priority": 3}, "rotation"},
		{"no priority rotates", gin.H{}, "rotation"},
		{"json number priority", gin.H{"priority": json.Number("-1")}, "reserve"},
	} {
		if got := dashboardAccountMode(tc.entry); got != tc.want {
			t.Errorf("%s: mode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRoutingStrategiesAreCanonical(t *testing.T) {
	if len(routingStrategies) != 4 {
		t.Fatalf("strategies = %v, want 4", routingStrategies)
	}
	for _, strategy := range routingStrategies {
		if got, ok := normalizeRoutingStrategy(strategy); !ok || got != strategy {
			t.Errorf("normalizeRoutingStrategy(%q) = %q, %v; want the same canonical value", strategy, got, ok)
		}
	}
}

// unsignedCodexIDToken builds an id_token with the given auth claims. The
// parser only decodes the payload, so the signature is a placeholder.
func unsignedCodexIDToken(t *testing.T, authClaims map[string]any) string {
	t.Helper()
	payload, errMarshal := json.Marshal(map[string]any{"https://api.openai.com/auth": authClaims})
	if errMarshal != nil {
		t.Fatalf("marshal claims: %v", errMarshal)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"none"}`)) + "." + encode(payload) + ".sig"
}

func TestDashboardAccountPlan(t *testing.T) {
	london, errZone := time.LoadLocation("Europe/London")
	if errZone != nil {
		t.Skipf("no tz data: %v", errZone)
	}
	// 23:30 UTC on 17 Oct is 00:30 on 18 Oct in London.
	until := time.Date(2026, 10, 17, 23, 30, 0, 0, time.UTC)
	codexAuth := &coreauth.Auth{Provider: "codex", Metadata: map[string]any{
		"id_token": unsignedCodexIDToken(t, map[string]any{
			"chatgpt_plan_type":                 "pro",
			"chatgpt_subscription_active_until": until.Format(time.RFC3339),
		}),
	}}

	before := time.Date(2026, 10, 1, 9, 0, 0, 0, london)

	plan := dashboardAccountPlan(codexAuth, nil, before)
	if plan["type"] != "pro" || plan["renews_on"] != "2026-10-18" || plan["source"] != "token" || plan["ends_on"] != "" || plan["next_bill_at"] != "2026-10-17T23:30:00Z" {
		t.Fatalf("token plan = %v", plan)
	}

	// A sign-in token is not rewritten each month. Once its paid-until time
	// has passed, the next bill is a month on at the same time.
	after := time.Date(2026, 11, 24, 12, 0, 0, 0, london)
	plan = dashboardAccountPlan(codexAuth, nil, after)
	if plan["renews_on"] != "2026-12-17" || plan["next_bill_at"] != "2026-12-17T23:30:00Z" || plan["source"] != "token" {
		t.Fatalf("token plan after its paid-until time = %v", plan)
	}

	codexAuth.Metadata["plan_ends_on"] = "2026-12-01"
	plan = dashboardAccountPlan(codexAuth, nil, before)
	if plan["ends_on"] != "2026-12-01" || plan["next_bill_at"] != "" {
		t.Fatalf("a plan that ends must not show a next bill: %v", plan)
	}

	codexAuth.Metadata["plan_renews_on"] = "2026-11-01"
	plan = dashboardAccountPlan(codexAuth, nil, before)
	if plan["renews_on"] != "2026-11-01" || plan["source"] != "manual" || plan["ends_on"] != "2026-12-01" || plan["next_bill_at"] != "" {
		t.Fatalf("manual plan = %v", plan)
	}

	unixAuth := &coreauth.Auth{Provider: "codex", Metadata: map[string]any{
		"id_token": unsignedCodexIDToken(t, map[string]any{"chatgpt_subscription_active_until": until.Unix()}),
	}}
	if plan = dashboardAccountPlan(unixAuth, nil, before); plan["renews_on"] != "2026-10-18" || plan["type"] != "" || plan["next_bill_at"] != "2026-10-17T23:30:00Z" {
		t.Fatalf("unix seconds plan = %v", plan)
	}

	dateAuth := &coreauth.Auth{Provider: "codex", Metadata: map[string]any{
		"id_token": unsignedCodexIDToken(t, map[string]any{"chatgpt_subscription_active_until": "2026-10-18"}),
	}}
	if plan = dashboardAccountPlan(dateAuth, nil, before); plan["renews_on"] != "2026-10-18" || plan["source"] != "token" || plan["next_bill_at"] != "" {
		t.Fatalf("date-only claim = %v", plan)
	}

	freeAuth := &coreauth.Auth{Provider: "codex", Metadata: map[string]any{
		"id_token": unsignedCodexIDToken(t, map[string]any{
			"chatgpt_plan_type":                 "free",
			"chatgpt_subscription_active_until": until.Format(time.RFC3339),
		}),
	}}
	if plan = dashboardAccountPlan(freeAuth, nil, before); plan["renews_on"] != "" || plan["next_bill_at"] != "" || plan["source"] != "" {
		t.Fatalf("a free plan has no bill: %v", plan)
	}

	claudeAuth := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"email": "x@example.com"}}
	plan = dashboardAccountPlan(claudeAuth, nil, before)
	if plan["type"] != "" || plan["renews_on"] != "" || plan["source"] != "" || plan["ends_on"] != "" || plan["next_bill_at"] != "" {
		t.Fatalf("claude plan = %v, want all empty", plan)
	}

	// A Claude subscription bills each month at the time it started.
	started := time.Date(2026, 10, 27, 14, 57, 2, 0, time.UTC)
	facts := &coreauth.PlanFacts{StartedAt: started, Status: "active", ObservedAt: after}
	plan = dashboardAccountPlan(claudeAuth, facts, after)
	if plan["renews_on"] != "2026-11-27" || plan["next_bill_at"] != "2026-11-27T14:57:02Z" || plan["source"] != "plan_start" {
		t.Fatalf("claude plan from its start = %v", plan)
	}
	facts.Status = "canceled"
	if plan = dashboardAccountPlan(claudeAuth, facts, after); plan["next_bill_at"] != "" || plan["renews_on"] != "" || plan["source"] != "" {
		t.Fatalf("a cancelled claude plan has no next bill: %v", plan)
	}
	facts.Status = "active"
	claudeAuth.Metadata["plan_renews_on"] = "2026-12-05"
	if plan = dashboardAccountPlan(claudeAuth, facts, after); plan["renews_on"] != "2026-12-05" || plan["source"] != "manual" || plan["next_bill_at"] != "" {
		t.Fatalf("a manual date wins over the plan start: %v", plan)
	}
}

func TestPatchAuthFileFields_PlanDates(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "claude-plan.json"
	filePath := filepath.Join(authDir, fileName)
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:         fileName,
		FileName:   fileName,
		Provider:   "claude",
		Attributes: map[string]string{"path": filePath},
		Metadata:   map[string]any{"type": "claude"},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.PatchAuthFileFields(ctx)
		return rec
	}
	persisted := func() map[string]any {
		t.Helper()
		raw, errRead := os.ReadFile(filePath)
		if errRead != nil {
			t.Fatalf("ReadFile() error = %v", errRead)
		}
		var data map[string]any
		if errUnmarshal := json.Unmarshal(raw, &data); errUnmarshal != nil {
			t.Fatalf("Unmarshal() error = %v", errUnmarshal)
		}
		return data
	}

	if rec := patch(`{"name":"claude-plan.json","plan_renews_on":"2026-10-18","plan_ends_on":"2026-10-08"}`); rec.Code != http.StatusOK {
		t.Fatalf("good dates: status %d body %s", rec.Code, rec.Body.String())
	}
	if data := persisted(); data["plan_renews_on"] != "2026-10-18" || data["plan_ends_on"] != "2026-10-08" {
		t.Fatalf("persisted = %v, want both plan dates", data)
	}

	for _, body := range []string{
		`{"name":"claude-plan.json","plan_renews_on":"2026-13-01"}`,
		`{"name":"claude-plan.json","plan_renews_on":"18/10/2026"}`,
		`{"name":"claude-plan.json","plan_ends_on":"2026-10-8"}`,
		`{"name":"claude-plan.json","plan_ends_on":20261008}`,
		`{"name":"claude-plan.json","plan_renews_on.day":"18"}`,
		`{"name":"claude-plan.json","prefix":"changed","plan_renews_on":"tomorrow"}`,
	} {
		if rec := patch(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", body, rec.Code)
		}
	}
	if data := persisted(); data["plan_renews_on"] != "2026-10-18" || data["prefix"] != nil {
		t.Fatalf("a rejected patch changed the file: %v", data)
	}

	if rec := patch(`{"name":"claude-plan.json","plan_renews_on":null}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d body %s", rec.Code, rec.Body.String())
	}
	data := persisted()
	if _, exists := data["plan_renews_on"]; exists {
		t.Fatalf("plan_renews_on still persisted after null: %v", data)
	}
	if data["plan_ends_on"] != "2026-10-08" {
		t.Fatalf("clearing one date dropped the other: %v", data)
	}
	updated, _ := manager.GetByID(fileName)
	if _, exists := updated.Metadata["plan_renews_on"]; exists {
		t.Fatal("plan_renews_on still in runtime metadata after null")
	}
}
