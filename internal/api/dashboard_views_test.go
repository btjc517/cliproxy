package api

import (
	"crypto/tls"
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

// A PUT goes through only when the Origin has the same scheme, hostname and
// effective port as the request. Over TLS the request scheme is https.
func TestDashboardViewsOriginMatchesSchemeHostAndPort(t *testing.T) {
	server, engine, _ := newViewsTestServer(t)
	put := func(host, origin string, overTLS bool) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/dashboard/views", strings.NewReader(testViewsBody))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = host
		req.Header.Set("Origin", origin)
		if overTLS {
			req.TLS = &tls.ConnectionState{}
		}
		engine.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, tc := range []struct {
		name, host, origin string
		overTLS            bool
		want               int
	}{
		{"same http origin", "desktop-home.ts.net:8317", "http://desktop-home.ts.net:8317", false, http.StatusOK},
		{"https page, http server", "desktop-home.ts.net:8317", "https://desktop-home.ts.net:8317", false, http.StatusForbidden},
		{"http page, https server", "dash.example", "http://dash.example", true, http.StatusForbidden},
		{"same https origin", "dash.example", "https://dash.example", true, http.StatusOK},
		{"https explicit 443 origin", "dash.example", "https://dash.example:443", true, http.StatusOK},
		{"https explicit 443 host", "dash.example:443", "https://dash.example", true, http.StatusOK},
		{"https other port", "dash.example", "https://dash.example:8443", true, http.StatusForbidden},
		{"http explicit 80 origin", "dash.example", "http://dash.example:80", false, http.StatusOK},
		{"http explicit 80 host", "dash.example:80", "http://dash.example", false, http.StatusOK},
		{"http 443 is not default", "dash.example", "http://dash.example:443", false, http.StatusForbidden},
		{"other port", "127.0.0.1:8317", "http://127.0.0.1:9999", false, http.StatusForbidden},
		{"other host", "127.0.0.1:8317", "http://localhost:8317", false, http.StatusForbidden},
		{"host case", "Dash.Example:8317", "http://dash.example:8317", false, http.StatusOK},
		{"ipv6 same", "[::1]:8317", "http://[::1]:8317", false, http.StatusOK},
		{"ipv6 default port", "[fd7a:115c:a1e0::1]", "http://[FD7A:115C:A1E0::1]:80", false, http.StatusOK},
		{"ipv6 other port", "[::1]:8317", "http://[::1]:9999", false, http.StatusForbidden},
		{"ipv6 other address", "[::1]:8317", "http://[::2]:8317", false, http.StatusForbidden},
		{"ipv6 without brackets", "[::1]:8317", "http://::1:8317", false, http.StatusForbidden},
		{"null origin", "127.0.0.1:8317", "null", false, http.StatusForbidden},
		{"file origin", "127.0.0.1:8317", "file://", false, http.StatusForbidden},
		{"origin with path", "127.0.0.1:8317", "http://127.0.0.1:8317/dashboard", false, http.StatusForbidden},
		{"origin with user", "127.0.0.1:8317", "http://me@127.0.0.1:8317", false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := put(tc.host, tc.origin, tc.overTLS); got != tc.want {
				t.Fatalf("Host %q Origin %q TLS %v: status %d, want %d", tc.host, tc.origin, tc.overTLS, got, tc.want)
			}
		})
	}

	// On a TLS listener, a connection the multiplexer hands over wrapped
	// (r.TLS nil) still counts as https.
	server.server = &http.Server{TLSConfig: &tls.Config{}}
	if got := put("dash.example", "https://dash.example", false); got != http.StatusOK {
		t.Fatalf("TLS server, wrapped connection, https origin: status %d, want 200", got)
	}
	if got := put("dash.example", "http://dash.example", false); got != http.StatusForbidden {
		t.Fatalf("TLS server, http origin: status %d, want 403", got)
	}
}

// The largest body the server accepts reads back after it is saved.
func TestDashboardViewsLargestBodyReadsBack(t *testing.T) {
	_, engine, _ := newViewsTestServer(t)
	host := "127.0.0.1:8317"
	var views []string
	for i := 0; i < 50; i++ {
		var panels []string
		for _, panelType := range []string{"allowance", "available", "tokens", "cost", "requests", "output", "cache", "ttft", "latency", "throughput", "failures", "activity"} {
			panels = append(panels, `{"type":"`+panelType+`","options":{"format":"bars","mode":"weekly"}}`)
		}
		id := strings.Repeat("v", 38) + string(rune('a'+i/26)) + string(rune('a'+i%26))
		views = append(views, `{"id":"`+id+`","name":"`+strings.Repeat("é", 60)+`","panels":[`+strings.Join(panels, ",")+
			`],"columns":["account","tokens","requests","input","cache_write","cache_read","output","cost","cache_reuse"],"window":"last7d","accounts":["`+
			strings.Repeat("a", 200)+`"],"builtin":false}`)
	}
	body := `{"views":[` + strings.Join(views, ",") + `],"default":""}`
	if len(body) < 60<<10 || len(body) > 64<<10 {
		t.Fatalf("body is %d bytes, want just under 64 KB", len(body))
	}
	if rec := viewsRequest(engine, http.MethodPut, "127.0.0.1:5000", host, "", body); rec.Code != http.StatusOK {
		t.Fatalf("PUT: status %d %s", rec.Code, rec.Body.String())
	}
	got := viewsRequest(engine, http.MethodGet, "127.0.0.1:5000", host, "", "")
	if got.Code != http.StatusOK || strings.TrimSpace(got.Body.String()) != body {
		t.Fatalf("GET after the largest PUT: status %d, %d bytes, want 200 and the %d bytes sent", got.Code, got.Body.Len(), len(body))
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
