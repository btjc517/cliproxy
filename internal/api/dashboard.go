package api

import (
	_ "embed"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed dashboard.html
var dashboardHTML []byte

// tailnetPrefixes are the Tailscale address ranges (CGNAT IPv4 and the
// Tailscale ULA IPv6 block).
var tailnetPrefixes = []*net.IPNet{
	mustCIDR("100.64.0.0/10"),
	mustCIDR("fd7a:115c:a1e0::/48"),
}

func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}

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
	if ip.IsLoopback() {
		return true
	}
	for _, prefix := range tailnetPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// serveDashboard serves the embedded account-pool dashboard page.
func (s *Server) serveDashboard(c *gin.Context) {
	if !s.dashboardAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", dashboardHTML)
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
