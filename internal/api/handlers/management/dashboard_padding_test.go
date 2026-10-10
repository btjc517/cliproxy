package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDashboardPaddingValidation(t *testing.T) {
	const usage = "usage_start=2026-10-01T00:00:00Z&usage_end=2026-10-02T00:00:00Z"
	const perf = "perf_start=2026-10-01T00:00:00Z&perf_end=2026-10-02T00:00:00Z"
	for _, query := range []string{
		"usage_pad_start=2026-09-30T00:00:00Z",
		usage + "&usage_pad_start=bad",
		usage + "&usage_pad_start=2026-10-01T06:00:00Z",
		usage + "&usage_pad_end=2026-10-01T18:00:00Z",
		"perf_pad_end=2026-10-03T00:00:00Z",
		perf + "&perf_pad_start=2026-10-01T01:00:00Z",
	} {
		t.Run(query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/dashboard/data?"+query, nil)
			(&Handler{}).GetDashboardData(ctx)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDashboardPaddingResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/dashboard/data?usage_start=2020-10-01T00:00:00Z&usage_end=2020-10-03T00:00:00Z&usage_pad_start=2020-09-30T00:00:00Z&usage_pad_end=2020-10-04T00:00:00Z&perf_start=2020-10-01T00:00:00Z&perf_end=2020-10-03T00:00:00Z&perf_pad_start=2020-09-30T00:00:00Z&perf_pad_end=2020-10-04T00:00:00Z", nil)
	(&Handler{}).GetDashboardData(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var data struct {
		Summary struct {
			UsageRange struct {
				Starts    []string `json:"starts"`
				ViewStart string   `json:"view_start"`
				ViewEnd   string   `json:"view_end"`
			} `json:"usage_range"`
			Performance struct {
				Range       string `json:"range"`
				FigureStart string `json:"figure_start"`
				FigureEnd   string `json:"figure_end"`
			} `json:"performance"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	u, p := data.Summary.UsageRange, data.Summary.Performance
	// Four UTC days of padded window, in local days of whatever zone runs the test.
	if u.ViewStart == "" || u.ViewEnd == "" || len(u.Starts) < 4 || len(u.Starts) > 5 {
		t.Fatalf("usage %+v, want the window echoed and the padding's four days", u)
	}
	if p.Range != "custom" || p.FigureStart == "" || p.FigureEnd == "" {
		t.Fatalf("performance %+v, want custom with figure edges", p)
	}
}
