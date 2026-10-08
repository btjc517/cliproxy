package management

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardUsageWindowValidation(t *testing.T) {
	for _, query := range []string{
		"usage_start=bad&usage_end=2026-10-08T00:00:00Z",
		"usage_start=2026-10-08T00:00:00Z",
		"usage_end=2026-10-08T00:00:00Z",
		"usage_start=2026-10-08T00:00:00Z&usage_end=2026-10-01T00:00:00Z",
		"usage_start=2025-10-01T00:00:00Z&usage_end=2026-10-08T00:00:00Z",
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
func TestDashboardUsageWindowResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/dashboard/data?usage_start=2026-10-01T12:30:00Z&usage_end=2026-10-02T12:30:00Z", nil)
	(&Handler{}).GetDashboardData(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var data struct {
		Summary struct {
			UsageRange struct {
				Range        string
				Starts, Ends []string
			} `json:"usage_range"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	r := data.Summary.UsageRange
	if r.Range != "viewport" || len(r.Starts) == 0 || len(r.Starts) != len(r.Ends) {
		t.Fatalf("invalid viewport %+v", r)
	}
}
