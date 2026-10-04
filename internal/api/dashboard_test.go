package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestFromLoopbackOrTailnet(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5000":          true,
		"[::1]:5000":              true,
		"100.65.36.120:5000":      true,
		"[fd7a:115c:a1e0::1]:443": true,
		"172.28.96.1:5000":        false,
		"192.168.1.20:5000":       false,
		"100.128.0.1:5000":        false,
		"not-an-address":          false,
	}
	for addr, want := range cases {
		if got := fromLoopbackOrTailnet(addr); got != want {
			t.Errorf("fromLoopbackOrTailnet(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestPostDashboardSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &Server{cfg: &config.Config{}}
	server.managementRoutesEnabled.Store(true)
	post := func(remoteAddr, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/dashboard/sessions", strings.NewReader(body))
		ctx.Request.RemoteAddr = remoteAddr
		server.postDashboardSessions(ctx)
		return rec
	}

	body := `{"machine":"mbp-m3","sessions":[{"id":"post-test-1","title":"One"},{"id":"post-test-2","title":"Two"},{"id":""}]}`
	if rec := post("192.168.1.20:5000", body); rec.Code != http.StatusForbidden {
		t.Fatalf("LAN caller: status %d, want 403", rec.Code)
	}
	rec := post("100.65.36.120:5000", body)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"updated":2}` {
		t.Fatalf("tailnet caller: status %d body %s, want 200 and 2 updated", rec.Code, rec.Body.String())
	}
	if rec := post("127.0.0.1:5000", `{"sessions":`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: status %d, want 400", rec.Code)
	}
	huge := `{"machine":"m","sessions":[{"id":"x","title":"` + strings.Repeat("a", maxDashboardSessionsBody) + `"}]}`
	if rec := post("127.0.0.1:5000", huge); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 1 MB: status %d, want 413", rec.Code)
	}
}
