package management

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// StartOAuthV8 dispatches login using the provider query parameter.
func (h *Handler) StartOAuthV8(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if c.Request.URL.Query().Has("reauth_id") {
		id := c.Query("reauth_id")
		if h.reauthTarget(id, provider) == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Select an existing subscription for this provider."})
			return
		}
		c.Set("reauth_id", id)
	}
	switch provider {
	case "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
	case "claude":
		h.RequestAnthropicToken(c)
	case "codex":
		h.RequestCodexToken(c)
	case "antigravity":
		h.RequestAntigravityToken(c)
	case "kimi":
		h.RequestKimiToken(c)
	case "kimi-ai":
		h.RequestKimiAIToken(c)
	case "xai":
		h.RequestXAIToken(c)
	case "devin":
		h.RequestDevinToken(c)
	case "meta":
		h.RequestMetaToken(c)
	default:
		if !h.ServePluginAuthURL(c) {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider_not_found"})
		}
	}
}

func canReauthenticate(auth *coreauth.Auth) bool {
	if auth == nil || auth.FileName == "" || (auth.Provider != "claude" && auth.Provider != "codex") {
		return false
	}
	kind, email := auth.AccountInfo()
	return kind == coreauth.AuthKindOAuth && email != ""
}

// reauthTarget resolves a dashboard row against the current credentials.
func (h *Handler) reauthTarget(id, provider string) *coreauth.Auth {
	if h.authManager == nil {
		return nil
	}
	target, ok := h.authManager.GetByID(id)
	if !ok || !canReauthenticate(target) || target.Provider != provider {
		return nil
	}
	return target
}

// Bind the provider's reply to the selected row before saving any tokens.
func (h *Handler) bindReauthRecord(id string, record *coreauth.Auth) error {
	if id == "" {
		return nil
	}
	target := h.reauthTarget(id, record.Provider)
	if target == nil {
		return errors.New("The selected account is no longer available.")
	}
	_, email := target.AccountInfo()
	returned, _ := record.Metadata["email"].(string)
	if email == "" || !strings.EqualFold(email, strings.TrimSpace(returned)) {
		return errors.New("You signed in to a different account. Try again with the selected email address.")
	}
	sameWorkspace := false
	for _, key := range []string{"organization_uuid", "account_uuid", "account_id"} {
		previous, _ := target.Metadata[key].(string)
		if previous == "" {
			continue
		}
		actual, _ := record.Metadata[key].(string)
		if actual == "" || !strings.EqualFold(previous, actual) {
			return errors.New("Choose the original account and workspace, then try again.")
		}
		if key != "account_uuid" {
			sameWorkspace = true
		}
	}
	// Older files may lack the workspace field. Their filename must then carry
	// the hash of the workspace the provider just verified.
	if !sameWorkspace && !namedForVerifiedWorkspace(target.FileName, record) {
		return errors.New("The provider could not confirm the original workspace. Check the account and try again.")
	}
	if h.cfg == nil || h.cfg.AuthDir == "" {
		return errors.New("The selected credential has no local account file.")
	}
	path := target.Attributes["path"]
	if path == "" {
		if !exactName(target.FileName) {
			return errors.New("The selected credential has no local account file.")
		}
		path = filepath.Join(h.cfg.AuthDir, target.FileName)
	}
	relative, err := filepath.Rel(h.cfg.AuthDir, path)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("The selected credential is outside the accounts directory.")
	}
	// Stores trim and clean names. Refuse any name they would change, so the
	// save cannot land on a different file.
	if !exactName(path) || !exactName(relative) {
		return errors.New("The selected credential has no local account file.")
	}
	record.ID, record.FileName = target.ID, relative
	record.Attributes = map[string]string{"path": path}
	return nil
}

// exactName reports whether trimming or cleaning would leave name unchanged.
func exactName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && name == filepath.Clean(name)
}

// namedForVerifiedWorkspace reports whether fileName is the name the sign-in
// produced and carries the hash of the workspace the provider returned.
// Email-only names never match.
func namedForVerifiedWorkspace(fileName string, record *coreauth.Auth) bool {
	key, prefix := "organization_uuid", "claude-"
	if record.Provider == "codex" {
		key, prefix = "account_id", "codex-"
	}
	workspace, _ := record.Metadata[key].(string)
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return false
	}
	digest := sha256.Sum256([]byte(workspace))
	name := strings.ToLower(filepath.Base(fileName))
	return strings.EqualFold(name, record.FileName) && strings.HasPrefix(name, prefix+hex.EncodeToString(digest[:])[:8]+"-")
}

// ImportOAuthV8 dispatches credential import using the provider query parameter.
func (h *Handler) ImportOAuthV8(c *gin.Context) {
	switch strings.ToLower(strings.TrimSpace(c.Query("provider"))) {
	case "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
	case "vertex":
		h.ImportVertexCredential(c)
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": "provider_not_found"})
	}
}
