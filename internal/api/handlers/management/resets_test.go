package management

import (
	"context"
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
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type stubResetBackend struct {
	result coreauth.ResetClaimResult
	grants []string
}

func (s *stubResetBackend) ClaimReset(_ context.Context, _ *coreauth.Auth, grant coreauth.ResetGrant, _ string) (coreauth.ResetClaimResult, error) {
	s.grants = append(s.grants, grant.ID)
	return s.result, nil
}
func (s *stubResetBackend) RefreshResets(context.Context, *coreauth.Auth) error { return nil }
func (s *stubResetBackend) AfterReset(context.Context, *coreauth.Auth)          {}

func TestUseResetEndpoint(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "mgmt-reset.json", Provider: "claude", Metadata: map[string]any{"email": "mgmt-reset@example.com"}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	state := coreauth.DefaultRoutingState()
	now := time.Now()
	state.ObserveResets(auth.ID, "claude", &coreauth.ResetStatus{ObservedAt: now, Grants: []coreauth.ResetGrant{{
		ID: "full", Label: "One full limit reset", Total: 1, Left: 1, Clears: []string{"7d"}, UsableNow: true,
	}}})
	backend := &stubResetBackend{result: coreauth.ResetClaimResult{Result: "not_limited"}}
	state.SetResetBackend(backend)
	t.Cleanup(func() { state.SetResetBackend(nil) })

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	router := gin.New()
	router.POST("/v8/management/routing/resets/use", h.UseReset)
	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v8/management/routing/resets/use", strings.NewReader(body)))
		var payload map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		return recorder, payload
	}

	if recorder, _ := post(`{"auth_id":"mgmt-reset.json"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing grant: status %d", recorder.Code)
	}
	if recorder, _ := post(`{"auth_id":"nobody.json","grant_id":"full"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown account: status %d", recorder.Code)
	}
	if recorder, payload := post(`{"auth_id":"mgmt-reset.json","grant_id":"gone"}`); recorder.Code != http.StatusNotFound || payload["error"] != "reset_not_found" {
		t.Fatalf("unknown reset: status %d body %v", recorder.Code, payload)
	}
	if len(backend.grants) != 0 {
		t.Fatalf("the provider was called for a bad request: %v", backend.grants)
	}

	// The provider's refusal is passed on with its reason, as a 200.
	recorder, payload := post(`{"auth_id":"mgmt-reset.json","grant_id":"full"}`)
	if recorder.Code != http.StatusOK || payload["result"] != "not_limited" || !strings.Contains(payload["text"].(string), "Nothing to reset") {
		t.Fatalf("refused use: status %d body %v", recorder.Code, payload)
	}
	backend.result = coreauth.ResetClaimResult{Result: "reset"}
	recorder, payload = post(`{"auth_id":"mgmt-reset.json","grant_id":"full"}`)
	if recorder.Code != http.StatusOK || payload["result"] != "reset" {
		t.Fatalf("use: status %d body %v", recorder.Code, payload)
	}
	if strings.Join(backend.grants, ",") != "full,full" {
		t.Fatalf("provider calls = %v", backend.grants)
	}
}

// The dashboard's switch writes routing.resets.mode through the config API.
func TestResetModeIsWrittenThroughConfigAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\nrouting:\n  strategy: soonest-reset\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config/routing/resets/mode", strings.NewReader(`"auto"`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	written, errReload := config.LoadConfig(path)
	if errReload != nil {
		t.Fatal(errReload)
	}
	if written.Routing.Resets.Mode != "auto" || written.Routing.Strategy != "soonest-reset" {
		t.Fatalf("routing after the write = %+v", written.Routing)
	}
}
