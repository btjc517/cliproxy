package management

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// UseReset uses one of an account's usage resets at the provider. The body
// names the account ("auth_id" or "auth_index") and the reset ("grant_id").
// A provider's refusal is a 200 with its result, since the request itself
// went through; "result" is "reset" only when the limit was cleared.
func (h *Handler) UseReset(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var req struct {
		AuthID    string `json:"auth_id"`
		AuthIndex string `json:"auth_index"`
		GrantID   string `json:"grant_id"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	grantID := strings.TrimSpace(req.GrantID)
	if grantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "grant_id is required"})
		return
	}
	var auth *coreauth.Auth
	if authID := strings.TrimSpace(req.AuthID); authID != "" {
		for _, candidate := range h.authManager.List() {
			if candidate != nil && candidate.ID == authID {
				auth = candidate
				break
			}
		}
	} else if authIndex := strings.TrimSpace(req.AuthIndex); authIndex != "" {
		auth = h.authByIndex(authIndex)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_id or auth_index is required"})
		return
	}
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	event, errUse := coreauth.DefaultRoutingState().UseReset(c.Request.Context(), auth, grantID, "you")
	switch {
	case errors.Is(errUse, coreauth.ErrResetUnknown):
		c.JSON(http.StatusNotFound, gin.H{"error": "reset_not_found", "message": "This account has no such reset. Reload and try again."})
	case errors.Is(errUse, coreauth.ErrResetBusy):
		c.JSON(http.StatusConflict, gin.H{"error": "reset_busy", "message": "A reset is already being used on this account."})
	case errUse != nil && event.At.IsZero():
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resets_unavailable", "message": errUse.Error()})
	case errUse != nil:
		c.JSON(http.StatusBadGateway, gin.H{"error": "provider_unreachable", "message": event.Text})
	default:
		c.JSON(http.StatusOK, gin.H{"status": "ok", "result": event.Result, "text": event.Text, "event": event})
	}
}
