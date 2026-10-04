package api

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnetname"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
)

// maxDashboardSessionsBody caps the POST /dashboard/sessions body.
const maxDashboardSessionsBody = 1 << 20

// dashboardFiles holds the dashboard app: index.html, app.css and the
// JavaScript modules it loads from /dashboard/static/.
//
//go:embed dashboard
var dashboardFiles embed.FS

// dashboardStatic serves everything under dashboard/ at /dashboard/static/.
var dashboardStatic = func() http.Handler {
	sub, err := fs.Sub(dashboardFiles, "dashboard")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/dashboard/static/", http.FileServer(http.FS(sub)))
}()

// dashboardAvailable reports whether the dashboard should be served at all.
func (s *Server) dashboardAvailable() bool {
	cfg := s.cfg
	return cfg != nil && !cfg.Home.Enabled && !cfg.RemoteManagement.DisableControlPanel && s.managementRoutesEnabled.Load()
}

// fromLoopbackOrTailnet checks the TCP peer address, not forwarded headers, so
// the read-only dashboard data is only visible on this host or over Tailscale.
func fromLoopbackOrTailnet(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || tailnetname.IsTailnet(ip)
}

// serveDashboard serves the dashboard app's page.
func (s *Server) serveDashboard(c *gin.Context) {
	if !s.dashboardAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	page, err := dashboardFiles.ReadFile("dashboard/index.html")
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", page)
}

// serveDashboardStatic serves the dashboard's stylesheet and scripts. They
// change with each build and are small, so the browser always revalidates.
func (s *Server) serveDashboardStatic(c *gin.Context) {
	if !s.dashboardAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	dashboardStatic.ServeHTTP(c.Writer, c.Request)
}

// serveDashboardData returns the read-only dashboard data without a management
// key, but only to callers on this host or on the tailnet. Changing an
// account still goes through the management API and needs the key.
func (s *Server) serveDashboardData(c *gin.Context) {
	if !s.dashboardAvailable() || s.mgmt == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !fromLoopbackOrTailnet(c.Request.RemoteAddr) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Header("Cache-Control", "no-store")
	s.mgmt.GetDashboardData(c)
}

// dashboardSessionsRequest is what a machine pushes about its client
// sessions: its own name and a title per session id.
type dashboardSessionsRequest struct {
	Machine  string                         `json:"machine"`
	Sessions []usagestats.SessionMetaUpdate `json:"sessions"`
}

// postDashboardSessions stores session titles and the sending machine's name
// for the dashboard. Like the data endpoint it needs no key but only answers
// callers on this host or on the tailnet.
func (s *Server) postDashboardSessions(c *gin.Context) {
	if !s.dashboardAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !fromLoopbackOrTailnet(c.Request.RemoteAddr) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	var body dashboardSessionsRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxDashboardSessionsBody))
	if errDecode := decoder.Decode(&body); errDecode != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(errDecode, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body is larger than 1 MB"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"updated": usagestats.Default().UpdateSessionMeta(body.Machine, body.Sessions)})
}
