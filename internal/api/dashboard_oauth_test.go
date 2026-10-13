package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The sign-in helper on Ben's Macs polls this to know when to catch the
// localhost redirect. It must name the waiting provider, never its state, and
// answer only on this host or the tailnet.
func TestDashboardOAuthPendingListsProvidersWithoutStates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &Server{cfg: &config.Config{}}
	server.managementRoutesEnabled.Store(true)
	engine := gin.New()
	engine.GET("/dashboard/oauth/pending", server.getDashboardOAuthPending)
	get := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/oauth/pending", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}

	const state = "pendingstatefortest0123456789abcd"
	managementHandlers.RegisterOAuthSession(state, "anthropic")
	t.Cleanup(func() { managementHandlers.CompleteOAuthSession(state) })

	rec := get("100.101.102.103:5000")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"anthropic"`) {
		t.Fatalf("tailnet: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), state) {
		t.Fatalf("the reply leaked the sign-in state: %s", rec.Body.String())
	}
	if rec := get("203.0.113.9:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("public address got %d, want 403", rec.Code)
	}

	managementHandlers.CompleteOAuthSession(state)
	if rec := get("127.0.0.1:5000"); strings.Contains(rec.Body.String(), "anthropic") {
		t.Fatalf("a completed sign-in is still listed: %s", rec.Body.String())
	}
}
