package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
)

// getDashboardOAuthPending tells the sign-in helper on Ben's Macs which
// providers are waiting for a browser callback, so it listens on their fixed
// localhost callback ports only while a dashboard sign-in is open. It returns
// provider names only: a sign-in completes with its state, which stays here.
func (s *Server) getDashboardOAuthPending(c *gin.Context) {
	if !s.dashboardAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !fromLoopbackOrTailnet(c.Request.RemoteAddr) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"providers": managementHandlers.PendingOAuthProviders()})
}
