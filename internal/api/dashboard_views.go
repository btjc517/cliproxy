package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	log "github.com/sirupsen/logrus"
)

// dashboardViewsPath is where saved dashboard views live: next to the usage
// stats file, or the test override.
func (s *Server) dashboardViewsPath() string {
	if s.viewsPathOverride != "" {
		return s.viewsPathOverride
	}
	return usagestats.Default().ViewsPath()
}

// servesTLS reports whether Start put this server's listener behind TLS. It
// reads the listener's state, not the config, which can change on reload
// without a restart.
func (s *Server) servesTLS() bool {
	return s.server != nil && s.server.TLSConfig != nil
}

// sameOrigin reports whether a browser request comes from a page served by
// this server. A request without an Origin header (a script or curl) passes.
// Otherwise the Origin must be a bare http or https origin whose scheme,
// hostname and effective port (80 or 443 when left out) equal the request's.
// The request is https when it arrived over TLS or when serverTLS says the
// listener is TLS: the multiplexer hands a TLS connection without ALPN to
// net/http wrapped, which leaves r.TLS nil. X-Forwarded-Proto is not used,
// because nothing in this server trusts it. An Origin of "null" (sandboxed
// or file pages) fails. The server's CORS middleware allows every origin, so
// this is what stops a page on another site, open in a browser on the
// tailnet, from changing the views.
func sameOrigin(r *http.Request, serverTLS bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, errParse := url.Parse(origin)
	if errParse != nil || parsed.User != nil || parsed.Opaque != "" || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil || serverTLS {
		scheme = "https"
	}
	if !strings.EqualFold(parsed.Scheme, scheme) {
		return false
	}
	requestHost := &url.URL{Host: r.Host}
	if requestHost.Hostname() == "" {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), requestHost.Hostname()) &&
		effectivePort(parsed.Port(), scheme) == effectivePort(requestHost.Port(), scheme)
}

// effectivePort is port, or the scheme's default port when port is empty.
func effectivePort(port, scheme string) string {
	if port != "" {
		return port
	}
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// dashboardViewsAllowed applies the read-only dashboard's access rule to the
// views endpoints: the dashboard must be on and the caller on this host or on
// the tailnet. It writes the refusal and reports whether to go on.
func (s *Server) dashboardViewsAllowed(c *gin.Context) bool {
	if !s.dashboardAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if !fromLoopbackOrTailnet(c.Request.RemoteAddr) {
		c.AbortWithStatus(http.StatusForbidden)
		return false
	}
	c.Header("Cache-Control", "no-store")
	return true
}

// getDashboardViews returns the saved dashboard views:
// {"views": [...], "default": "<id or empty>"}.
func (s *Server) getDashboardViews(c *gin.Context) {
	if !s.dashboardViewsAllowed(c) {
		return
	}
	path := s.dashboardViewsPath()
	if path == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "saved views need a usage stats file"})
		return
	}
	views, errRead := usagestats.ReadViews(path)
	if errRead != nil {
		log.Warnf("dashboard: read saved views: %v", errRead)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "saved views file cannot be read"})
		return
	}
	c.JSON(http.StatusOK, views)
}

// putDashboardViews replaces the saved dashboard views with the request body,
// after usagestats.DecodeViews checks it. Besides the read-only dashboard's
// access rule it refuses a cross-origin browser request.
func (s *Server) putDashboardViews(c *gin.Context) {
	if !s.dashboardViewsAllowed(c) {
		return
	}
	if !sameOrigin(c.Request, s.servesTLS()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "cross-origin request refused"})
		return
	}
	path := s.dashboardViewsPath()
	if path == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "saved views need a usage stats file"})
		return
	}
	views, errDecode := usagestats.DecodeViews(c.Request.Body)
	if errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errDecode.Error()})
		return
	}
	stored, errWrite := usagestats.WriteViews(path, views)
	if errWrite != nil {
		log.Errorf("dashboard: save views: %v", errWrite)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "saved views could not be written"})
		return
	}
	c.JSON(http.StatusOK, stored)
}
