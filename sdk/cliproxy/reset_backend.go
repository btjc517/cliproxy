package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// These are variables so tests can point them at a local server.
var (
	// claudeResetURLFormat takes the account's organization UUID.
	claudeResetURLFormat = "https://api.anthropic.com/api/organizations/%s/reset_rate_limits"
	codexBackendURL      = "https://chatgpt.com/backend-api"
)

const (
	// claudeUsageResetQuery asks the usage endpoint to include the account's
	// reset grants and to skip the spend report nothing here reads.
	claudeUsageResetQuery = "cedar_ember=1&skip_spend=1"
	claudeResetProgram    = "cedar_ember"
	// codexResetUserAgent and codexResetOriginator match what the Codex
	// executor sends on model requests.
	codexResetUserAgent  = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	codexResetOriginator = "codex-tui"
)

// resetBackend reads and uses usage resets at the providers for the routing
// state.
type resetBackend struct{ service *Service }

func (b resetBackend) ClaimReset(ctx context.Context, auth *coreauth.Auth, grant coreauth.ResetGrant, requestID string) (coreauth.ResetClaimResult, error) {
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return b.service.claimClaudeReset(ctx, auth, grant, requestID)
	case "codex":
		return b.service.claimCodexReset(ctx, auth, grant, requestID)
	}
	return coreauth.ResetClaimResult{}, errors.New("this provider has no usage resets")
}

func (b resetBackend) RefreshResets(ctx context.Context, auth *coreauth.Auth) error {
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return b.service.readClaudeUsage(ctx, auth)
	case "codex":
		return b.service.readCodexUsage(ctx, auth)
	}
	return nil
}

func (b resetBackend) AfterReset(ctx context.Context, auth *coreauth.Auth) {
	if b.service == nil || b.service.coreManager == nil {
		return
	}
	if _, _, errReset := b.service.coreManager.ResetQuota(ctx, auth.ID); errReset != nil {
		log.WithField("auth", auth.ID).Warnf("usage resets: lift cooldown: %v", errReset)
	}
}

func claudeOAuthToken(auth *coreauth.Auth) string {
	token := claudeauth.ReadMetadataString(&auth.Metadata, "access_token")
	if !strings.Contains(token, "sk-ant-oat") {
		return ""
	}
	return token
}

// readClaudeUsage reads one Claude account's usage and reset grants and
// records both.
func (s *Service) readClaudeUsage(ctx context.Context, auth *coreauth.Auth) error {
	token := claudeOAuthToken(auth)
	if token == "" {
		return errors.New("account has no subscription sign-in")
	}
	body, errProbe := s.fetchClaudeUsage(ctx, auth, token)
	if errProbe != nil {
		return errProbe
	}
	state := coreauth.DefaultRoutingState()
	now := time.Now()
	if meters := coreauth.ParseClaudeUsageMeters(body, now); len(meters) > 0 {
		s.releaseRefilledHold(ctx, auth, meters, now)
		state.ObserveMeters(auth.ID, "claude", meters, now)
	}
	state.ObserveResets(auth.ID, "claude", coreauth.ParseClaudeResetStatus(body, now))
	return nil
}

func (s *Service) claimClaudeReset(ctx context.Context, auth *coreauth.Auth, grant coreauth.ResetGrant, requestID string) (coreauth.ResetClaimResult, error) {
	token := claudeOAuthToken(auth)
	organization := strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, "organization_uuid"))
	if token == "" || organization == "" {
		return coreauth.ResetClaimResult{}, errors.New("account has no subscription sign-in")
	}
	payload, errMarshal := json.Marshal(map[string]string{"program": claudeResetProgram, "grant_id": grant.ID, "request_id": requestID})
	if errMarshal != nil {
		return coreauth.ResetClaimResult{}, errMarshal
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(claudeResetURLFormat, url.PathEscape(organization)), bytes.NewReader(payload))
	if errReq != nil {
		return coreauth.ResetClaimResult{}, errReq
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", helps.DefaultClaudeUserAgent(s.cfg))
	body, errDo := s.doResetRequest(ctx, auth, req)
	if errDo != nil {
		return coreauth.ResetClaimResult{}, errDo
	}
	root := gjson.ParseBytes(body)
	result := coreauth.ResetClaimResult{Result: strings.TrimSpace(root.Get("result").String())}
	if result.Result == "" {
		return coreauth.ResetClaimResult{}, errors.New("the provider's answer had no result")
	}
	if left := root.Get("resets_left"); left.Type == gjson.Number {
		value := int(left.Int())
		result.Left = &value
	}
	root.Get("cleared").ForEach(func(_, window gjson.Result) bool {
		result.Cleared = append(result.Cleared, coreauth.ClaudeResetWindow(window.String()))
		return true
	})
	return result, nil
}

// releaseRefilledHold releases the hold on an account whose usage report
// shows a window refilled before the time the proxy holds it until, as happens
// when a reset is used at the provider.
//
// A request that finishes on the account while the report is being fetched
// changes the account, and the release is refused for a snapshot that is out
// of date. It is tried again with the account as it stands now. If that is
// refused too, the report is still recorded by the caller: the meters keep
// the refill, so the next read releases the hold.
func (s *Service) releaseRefilledHold(ctx context.Context, auth *coreauth.Auth, meters map[string]*coreauth.Meter, now time.Time) {
	if s == nil || s.coreManager == nil {
		return
	}
	reopened := coreauth.DefaultRoutingState().ReopenedClaudeWindows(auth.ID, meters, now)
	if len(reopened) == 0 {
		return
	}
	current := auth
	var errReconcile error
	for attempt := 0; attempt < 2; attempt++ {
		if errReconcile = s.coreManager.ReconcileClaudeQuota(ctx, current, reopened, meters); errReconcile == nil {
			return
		}
		fresh, ok := s.coreManager.GetByID(auth.ID)
		if !ok || fresh == nil {
			break
		}
		current = fresh
	}
	log.WithField("auth", auth.ID).Debugf("usage read: hold not released, left for the next read: %v", errReconcile)
}

func codexOAuth(auth *coreauth.Auth) (token, accountID string) {
	if auth == nil || auth.Metadata == nil {
		return "", ""
	}
	token, _ = auth.Metadata["access_token"].(string)
	accountID, _ = auth.Metadata["account_id"].(string)
	return strings.TrimSpace(token), strings.TrimSpace(accountID)
}

func (s *Service) codexResetRequest(ctx context.Context, auth *coreauth.Auth, method, path string, payload []byte) ([]byte, error) {
	token, accountID := codexOAuth(auth)
	if token == "" || accountID == "" {
		return nil, errors.New("account has no subscription sign-in")
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, errReq := http.NewRequestWithContext(ctx, method, codexBackendURL+path, reader)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Chatgpt-Account-Id", accountID)
	req.Header.Set("Originator", codexResetOriginator)
	req.Header.Set("User-Agent", codexResetUserAgent)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.doResetRequest(ctx, auth, req)
}

// readCodexUsage reads one Codex account's reset credits and its 5-hour and
// weekly windows and records both. A failure of either read is returned, so
// automatic mode never uses a credit on windows it could not read again; the
// credits that were read stay recorded.
func (s *Service) readCodexUsage(ctx context.Context, auth *coreauth.Auth) error {
	credits, errCredits := s.codexResetRequest(ctx, auth, http.MethodGet, "/wham/rate-limit-reset-credits", nil)
	if errCredits != nil {
		return errCredits
	}
	state := coreauth.DefaultRoutingState()
	now := time.Now()
	state.ObserveResets(auth.ID, "codex", coreauth.ParseCodexResetStatus(credits, now))
	usage, errUsage := s.codexResetRequest(ctx, auth, http.MethodGet, "/wham/usage", nil)
	if errUsage != nil {
		return fmt.Errorf("read usage windows: %w", errUsage)
	}
	if meters := coreauth.ParseCodexUsageMeters(usage, now); len(meters) > 0 {
		s.releaseRefilledHold(ctx, auth, meters, now)
		state.ObserveMeters(auth.ID, "codex", meters, now)
	}
	return nil
}

func (s *Service) claimCodexReset(ctx context.Context, auth *coreauth.Auth, grant coreauth.ResetGrant, requestID string) (coreauth.ResetClaimResult, error) {
	payload, errMarshal := json.Marshal(map[string]string{"redeem_request_id": requestID, "credit_id": grant.ID})
	if errMarshal != nil {
		return coreauth.ResetClaimResult{}, errMarshal
	}
	body, errDo := s.codexResetRequest(ctx, auth, http.MethodPost, "/wham/rate-limit-reset-credits/consume", payload)
	if errDo != nil {
		return coreauth.ResetClaimResult{}, errDo
	}
	code := strings.TrimSpace(gjson.GetBytes(body, "code").String())
	if code == "" {
		return coreauth.ResetClaimResult{}, errors.New("the provider's answer had no result")
	}
	result := coreauth.ResetClaimResult{Result: code}
	if code == "reset" {
		result.Cleared = grant.Clears
	}
	return result, nil
}

// doResetRequest sends a reset request and returns the body of a 200 answer.
func (s *Service) doResetRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) ([]byte, error) {
	resp, errDo := helps.NewProxyAwareHTTPClient(ctx, s.cfg, auth, 0).Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("usage resets: close body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &usageProbeStatusError{status: resp.StatusCode}
	}
	return body, nil
}

// probeCodexResets reads the reset credits of signed-in Codex accounts that
// have no recent reading.
func (s *Service) probeCodexResets(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	state := coreauth.DefaultRoutingState()
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "codex") {
			continue
		}
		if token, accountID := codexOAuth(auth); token == "" || accountID == "" {
			continue
		}
		now := time.Now()
		resetAge := meterProbeMaxAge
		if hasActiveQuotaHold(auth, now) {
			// A reset used at the provider refills an account days before the
			// time it is held until, so a held account is read more often.
			resetAge = heldAccountProbeAge
		}
		if !state.NeedsResetReading(auth.ID, now, resetAge) {
			continue
		}
		if errRead := s.readCodexUsage(ctx, auth); errRead != nil {
			log.WithField("auth", auth.ID).Debugf("codex reset probe: %v", errRead)
		}
		state.MarkResetProbe(auth.ID, time.Now())
	}
}

// heldAccountProbeAge is how often an account held for a used-up limit is
// read again, so a refill at the provider is noticed within a few minutes.
const heldAccountProbeAge = 2 * meterProbeInterval

// hasActiveQuotaHold reports whether the account, or any of its models, is held
// for a used-up limit.
func hasActiveQuotaHold(auth *coreauth.Auth, now time.Time) bool {
	if auth.Quota.Exceeded && auth.Quota.NextRecoverAt.After(now) {
		return true
	}
	for _, state := range auth.ModelStates {
		if state != nil && state.Quota.Exceeded && state.Quota.NextRecoverAt.After(now) {
			return true
		}
	}
	return false
}

// runAutoReset uses one reset when automatic mode is on and one is worth it.
func (s *Service) runAutoReset(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	if event := coreauth.DefaultRoutingState().RunAutoReset(ctx, s.coreManager.List()); event != nil {
		log.WithFields(log.Fields{"auth": event.AuthID, "grant": event.GrantID, "result": event.Result}).Info("usage resets: automatic use")
	}
}
