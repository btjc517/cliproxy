package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed dashboard.html
var dashboardHTML []byte

// serveDashboard serves the embedded account-pool dashboard. The page reads the
// /v8/management API with the management key the viewer enters, so it is only
// useful where management routes are enabled.
func (s *Server) serveDashboard(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel || !s.managementRoutesEnabled.Load() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", dashboardHTML)
}
