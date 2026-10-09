package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func getDashboardData(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/dashboard/data?"+query, nil)
	(&Handler{}).GetDashboardData(ctx)
	return rec
}

func TestDashboardPerfWindowValidation(t *testing.T) {
	for _, tc := range []struct{ query, message string }{
		{"perf_start=2026-10-08T00:00:00Z", "together"},
		{"perf_end=2026-10-08T00:00:00Z", "together"},
		{"perf_start=yesterday&perf_end=2026-10-08T00:00:00Z", "RFC3339"},
		{"perf_start=2026-10-08T00:00:00Z&perf_end=2026-10-08", "RFC3339"},
		{"perf_start=2026-10-08T00:00:00Z&perf_end=2026-10-07T00:00:00Z", "1 hour to 366 days"},
		{"perf_start=2026-10-08T00:00:00Z&perf_end=2026-10-08T00:00:00Z", "1 hour to 366 days"},
		{"perf_start=2026-10-08T00:00:00Z&perf_end=2026-10-08T00:59:59Z", "1 hour to 366 days"},
		{"perf_start=2025-10-01T00:00:00Z&perf_end=2026-10-08T00:00:00Z", "1 hour to 366 days"},
		{"perf_start=1960-01-01T00:00:00Z&perf_end=1960-01-02T00:00:00Z", "1970 and 2100"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := getDashboardData(t, tc.query)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("status %d body %s, want 400 naming %q", rec.Code, rec.Body.String(), tc.message)
			}
		})
	}
}

func TestDashboardPerfWindowResponse(t *testing.T) {
	end := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	start := end.Add(-7 * 24 * time.Hour)
	query := "perf_start=" + url.QueryEscape(start.Format(time.RFC3339)) + "&perf_end=" + url.QueryEscape(end.Format(time.RFC3339)) + "&scope=a,b"
	rec := getDashboardData(t, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var data struct {
		Summary struct {
			Range       string `json:"range"`
			Performance struct {
				Range         string   `json:"range"`
				BucketSeconds int64    `json:"bucket_seconds"`
				Ranges        []string `json:"ranges"`
				Scopes        map[string]struct {
					Series []struct {
						Start time.Time `json:"start"`
					} `json:"series"`
				} `json:"scopes"`
			} `json:"performance"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	perf := data.Summary.Performance
	if perf.Range != "custom" || perf.BucketSeconds != 3600 || len(perf.Ranges) == 0 {
		t.Fatalf("performance range %q bucket %d ranges %v, want custom, 3600 and the fixed ranges", perf.Range, perf.BucketSeconds, perf.Ranges)
	}
	if data.Summary.Range != "24h" {
		t.Fatalf("summary range %q, want the default fixed range untouched", data.Summary.Range)
	}
	series := perf.Scopes["all"].Series
	if len(series) == 0 || series[0].Start.After(start) || series[len(series)-1].Start.After(time.Now()) {
		t.Fatalf("series of %d points does not start at the window or stops after now", len(series))
	}
	// 5 days before now plus the hour that holds now, give or take the edges.
	if len(series) < 120 || len(series) > 122 {
		t.Fatalf("series has %d hourly points, want about 121", len(series))
	}

	plain := getDashboardData(t, "")
	if !strings.Contains(plain.Body.String(), `"range":"24h"`) || strings.Contains(plain.Body.String(), `"range":"custom"`) {
		t.Fatal("without perf params the performance range changed")
	}
}
