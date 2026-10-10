package api

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// testViewsBody is a views document as the server stores it after the first
// save. The revision a client sends is ignored, so a PUT with If-Match "0"
// stores and returns exactly this.
const testViewsBody = `{"views":[{"id":"spend","name":"Spend","panels":[{"type":"tokens","options":{"format":"lines"}}],"columns":["tokens","cost"],"window":"last24h","accounts":null,"builtin":false}],"default":"spend","deleted":[],"revision":1}`

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
	return viewsRequestIfMatch(engine, method, remoteAddr, host, origin, "", body)
}

// viewsRequestIfMatch is viewsRequest with an If-Match header, left out when
// ifMatch is empty.
func viewsRequestIfMatch(engine *gin.Engine, method, remoteAddr, host, origin, ifMatch, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/dashboard/views", strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	engine.ServeHTTP(rec, req)
	return rec
}

func TestDashboardViewsRoundTrip(t *testing.T) {
	_, engine, path := newViewsTestServer(t)
	host := "desktop-home.tail.ts.net:8317"

	empty := viewsRequest(engine, http.MethodGet, "127.0.0.1:5000", host, "", "")
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != `{"views":[],"default":"","deleted":[],"revision":0}` {
		t.Fatalf("empty GET: status %d body %s", empty.Code, empty.Body.String())
	}

	put := viewsRequestIfMatch(engine, http.MethodPut, "100.65.36.120:5000", host, "http://"+host, `"0"`, testViewsBody)
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
	replace := `{"views":[],"default":"","deleted":[],"revision":2}`
	if rec := viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", host, "", `"1"`, replace); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != replace {
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
			rec := viewsRequestIfMatch(engine, tc.method, tc.remote, host, tc.origin, `"0"`, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d body %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatal("a refused PUT wrote the views file")
	}

	if rec := viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", host, "HTTP://127.0.0.1:8317", `"0"`, testViewsBody); rec.Code != http.StatusOK {
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
	// revision tracks the stored revision, so every PUT that passes the
	// origin check also passes the revision check.
	revision := 0
	put := func(host, origin string, overTLS bool) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/dashboard/views", strings.NewReader(testViewsBody))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = host
		req.Header.Set("Origin", origin)
		req.Header.Set("If-Match", strconv.Itoa(revision))
		if overTLS {
			req.TLS = &tls.ConnectionState{}
		}
		engine.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			revision++
		}
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
	body := `{"views":[` + strings.Join(views, ",") + `],"default":"","deleted":[],"revision":1}`
	if len(body) < 60<<10 || len(body) > 64<<10 {
		t.Fatalf("body is %d bytes, want just under 64 KB", len(body))
	}
	if rec := viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", host, "", "0", body); rec.Code != http.StatusOK {
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
	// A damaged file has no revision to trust; it counts as the empty set at
	// revision 0, so a client can replace it.
	if rec := viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", "127.0.0.1:8317", "", `"0"`, testViewsBody); rec.Code != http.StatusOK {
		t.Fatalf("PUT over a damaged file: status %d, want 200", rec.Code)
	}
}

// decodeViewsResponse decodes a views response body, failing the test when
// it is not JSON.
func decodeViewsResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("response %q is not JSON: %v", rec.Body.String(), errDecode)
	}
	return body
}

func TestDashboardViewsRevisions(t *testing.T) {
	_, engine, path := newViewsTestServer(t)
	host := "127.0.0.1:8317"
	get := func() *httptest.ResponseRecorder {
		return viewsRequest(engine, http.MethodGet, "127.0.0.1:5000", host, "", "")
	}
	put := func(ifMatch, body string) *httptest.ResponseRecorder {
		return viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", host, "http://"+host, ifMatch, body)
	}

	// An empty store is revision 0.
	if rec := get(); rec.Code != http.StatusOK || decodeViewsResponse(t, rec)["revision"] != float64(0) || rec.Header().Get("ETag") != `"0"` {
		t.Fatalf("empty GET: status %d ETag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}

	// A file saved before revisions existed is revision 0.
	legacy := `{"views":[{"id":"spend","name":"Spend","panels":[],"columns":[],"window":"last24h","accounts":null,"builtin":false}],"default":"spend"}`
	if errWrite := os.WriteFile(path, []byte(legacy), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if rec := get(); rec.Code != http.StatusOK || decodeViewsResponse(t, rec)["revision"] != float64(0) || rec.Header().Get("ETag") != `"0"` {
		t.Fatalf("legacy GET: status %d ETag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}

	// The right If-Match moves to the next revision. The body's own revision
	// is ignored.
	first := put(`"0"`, `{"views":[],"default":"spend","revision":99}`)
	if first.Code != http.StatusOK || strings.TrimSpace(first.Body.String()) != `{"views":[],"default":"spend","deleted":[],"revision":1}` || first.Header().Get("ETag") != `"1"` {
		t.Fatalf("PUT If-Match 0: status %d ETag %q body %s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	// A bare number works too.
	second := put("1", `{"views":[],"default":"","revision":1}`)
	if second.Code != http.StatusOK || strings.TrimSpace(second.Body.String()) != `{"views":[],"default":"","deleted":[],"revision":2}` {
		t.Fatalf("PUT If-Match 1: status %d body %s", second.Code, second.Body.String())
	}
	stored, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}

	// A stale or malformed If-Match gets 409 with the current document and
	// writes nothing.
	current := strings.TrimSpace(get().Body.String())
	for _, ifMatch := range []string{`"1"`, `"0"`, `"3"`, "abc", `W/"2"`, "*", `"-1"`, `""`} {
		rec := put(ifMatch, `{"views":[],"default":"lost"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("If-Match %s: status %d body %s, want 409", ifMatch, rec.Code, rec.Body.String())
		}
		want := `{"current":` + current + `,"error":"views changed since you loaded them","revision":2}`
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Fatalf("If-Match %s: body %s, want %s", ifMatch, got, want)
		}
		if rec.Header().Get("ETag") != `"2"` {
			t.Fatalf("If-Match %s: ETag %q", ifMatch, rec.Header().Get("ETag"))
		}
	}

	// No If-Match gets 428.
	missing := put("", `{"views":[],"default":"lost"}`)
	if missing.Code != http.StatusPreconditionRequired || strings.TrimSpace(missing.Body.String()) != `{"error":"If-Match header required"}` {
		t.Fatalf("PUT without If-Match: status %d body %s, want 428", missing.Code, missing.Body.String())
	}

	if after, _ := os.ReadFile(path); string(after) != string(stored) {
		t.Fatalf("refused PUTs changed the file to %s", after)
	}
}

// Two tabs save from the same revision at once: exactly one wins.
func TestDashboardViewsConcurrentPutsFromOneRevision(t *testing.T) {
	_, engine, _ := newViewsTestServer(t)
	host := "127.0.0.1:8317"
	for round := 0; round < 20; round++ {
		ifMatch := strconv.Itoa(round)
		start := make(chan struct{})
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				body := `{"views":[],"default":"tab-` + strconv.Itoa(i) + `"}`
				codes[i] = viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", host, "", ifMatch, body).Code
			}(i)
		}
		close(start)
		wg.Wait()
		ok, conflict := 0, 0
		for _, code := range codes {
			switch code {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatalf("round %d: statuses %v, want one 200 and one 409", round, codes)
		}
	}
	if rec := viewsRequest(engine, http.MethodGet, "127.0.0.1:5000", host, "", ""); decodeViewsResponse(t, rec)["revision"] != float64(20) {
		t.Fatalf("after 20 rounds: %s, want revision 20", rec.Body.String())
	}
}

func TestDashboardViewsUpdatedAtAndDeletedRoundTrip(t *testing.T) {
	_, engine, _ := newViewsTestServer(t)
	host := "127.0.0.1:8317"
	body := `{"views":[{"id":"spend","name":"Spend","panels":[{"type":"cost","options":{}}],"columns":[],"window":"last24h","accounts":null,"builtin":false,"updated_at":1760090000123}],` +
		`"default":"spend","deleted":[{"id":"old","updated_at":1760080000000}],"revision":1}`
	if rec := viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", host, "", `"0"`, body); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("PUT: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := viewsRequest(engine, http.MethodGet, "127.0.0.1:5000", host, "", ""); strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("GET: %s, want %s", rec.Body.String(), body)
	}
	bad := `{"views":[],"default":"","deleted":[{"id":"Old","updated_at":1}]}`
	if rec := viewsRequestIfMatch(engine, http.MethodPut, "127.0.0.1:5000", host, "", `"1"`, bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad deleted id: status %d, want 400", rec.Code)
	}
}
