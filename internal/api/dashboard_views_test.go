package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const testViewsBody = `{"views":[{"id":"spend","name":"Spend","panels":[{"type":"tokens","options":{"format":"lines"}}],"columns":["tokens","cost"],"window":"last24h","accounts":null,"builtin":false}],"default":"spend"}`

func newViewsTestServer(t *testing.T) (*Server, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "dashboard-views.json")
	server := &Server{cfg: &config.Config{}, viewsPathOverride: path}
	server.managementRoutesEnabled.Store(true)
	engine := gin.New()
	engine.GET("/dashboard/views", server.getDashboardViews)
	engine.PUT("/dashboard/views", server.putDashboardViews)
	return server, engine, path
}

func viewsRequest(engine *gin.Engine, method, remoteAddr, host, origin, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/dashboard/views", strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	engine.ServeHTTP(rec, req)
	return rec
}

func TestDashboardViewsRoundTrip(t *testing.T) {
	_, engine, path := newViewsTestServer(t)
	host := "desktop-home.tail.ts.net:8317"

	empty := viewsRequest(engine, http.MethodGet, "127.0.0.1:5000", host, "", "")
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != `{"views":[],"default":""}` {
		t.Fatalf("empty GET: status %d body %s", empty.Code, empty.Body.String())
	}

	put := viewsRequest(engine, http.MethodPut, "100.65.36.120:5000", host, "http://"+host, testViewsBody)
	if put.Code != http.StatusOK || strings.TrimSpace(put.Body.String()) != testViewsBody {
		t.Fatalf("same-origin PUT: status %d body %s, want 200 and the stored body", put.Code, put.Body.String())
	}
	got := viewsRequest(engine, http.MethodGet, "100.65.36.120:5000", host, "", "")
	if got.Code != http.StatusOK || strings.TrimSpace(got.Body.String()) != testViewsBody {
		t.Fatalf("GET after PUT: status %d body %s", got.Code, got.Body.String())
	}
	info, errStat := os.Stat(path)
	if errStat != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("views file %v mode %v, want 0600", errStat, info.Mode().Perm())
	}

	// A script without an Origin header may replace the set.
	replace := `{"views":[],"default":""}`
	if rec := viewsRequest(engine, http.MethodPut, "127.0.0.1:5000", host, "", replace); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != replace {
		t.Fatalf("PUT without Origin: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardViewsRefusals(t *testing.T) {
	server, engine, path := newViewsTestServer(t)
	host := "127.0.0.1:8317"

	for _, tc := range []struct {
		name, method, remote, origin, body string
		want                               int
	}{
		{"LAN GET", http.MethodGet, "192.168.1.20:5000", "", "", http.StatusForbidden},
		{"LAN PUT", http.MethodPut, "192.168.1.20:5000", "", testViewsBody, http.StatusForbidden},
		{"cross-origin PUT", http.MethodPut, "127.0.0.1:5000", "https://evil.example", testViewsBody, http.StatusForbidden},
		{"other port PUT", http.MethodPut, "127.0.0.1:5000", "http://127.0.0.1:9999", testViewsBody, http.StatusForbidden},
		{"null origin PUT", http.MethodPut, "127.0.0.1:5000", "null", testViewsBody, http.StatusForbidden},
		{"file origin PUT", http.MethodPut, "127.0.0.1:5000", "file://", testViewsBody, http.StatusForbidden},
		{"invalid body", http.MethodPut, "127.0.0.1:5000", "http://" + host, `{"views":[{"id":"Bad"}],"default":""}`, http.StatusBadRequest},
		{"unknown field", http.MethodPut, "127.0.0.1:5000", "http://" + host, `{"views":[],"default":"","x":1}`, http.StatusBadRequest},
		{"too large", http.MethodPut, "127.0.0.1:5000", "", `{"views":[],"default":"` + strings.Repeat("a", 70<<10) + `"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := viewsRequest(engine, tc.method, tc.remote, host, tc.origin, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d body %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatal("a refused PUT wrote the views file")
	}

	if rec := viewsRequest(engine, http.MethodPut, "127.0.0.1:5000", host, "HTTP://127.0.0.1:8317", testViewsBody); rec.Code != http.StatusOK {
		t.Fatalf("same origin in capitals: status %d", rec.Code)
	}

	server.managementRoutesEnabled.Store(false)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := viewsRequest(engine, method, "127.0.0.1:5000", host, "", testViewsBody); rec.Code != http.StatusNotFound {
			t.Fatalf("dashboard off %s: status %d, want 404", method, rec.Code)
		}
	}
}

func TestDashboardViewsDamagedFile(t *testing.T) {
	_, engine, path := newViewsTestServer(t)
	if errWrite := os.WriteFile(path, []byte("{"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if rec := viewsRequest(engine, http.MethodGet, "127.0.0.1:5000", "127.0.0.1:8317", "", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("damaged file GET: status %d, want 500", rec.Code)
	}
	if rec := viewsRequest(engine, http.MethodPut, "127.0.0.1:5000", "127.0.0.1:8317", "", testViewsBody); rec.Code != http.StatusOK {
		t.Fatalf("PUT over a damaged file: status %d, want 200", rec.Code)
	}
}
