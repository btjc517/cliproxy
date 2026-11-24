package cliproxy

import (
	"context"
	"errors"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// claudeProfileURL is a variable so tests can point it at a local server.
var claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"

// planProbeMaxAge is how old an account's subscription facts may get. A
// subscription's start changes only when it is cancelled and bought again.
const planProbeMaxAge = 24 * time.Hour

// probeClaudePlans reads when each signed-in Claude account's subscription
// started, once a day, so the dashboard can show when it bills next.
func (s *Service) probeClaudePlans(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	state := coreauth.DefaultRoutingState()
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "claude") || claudeOAuthToken(auth) == "" {
			continue
		}
		if !state.NeedsPlanReading(auth.ID, time.Now(), planProbeMaxAge) {
			continue
		}
		if errProbe := s.readClaudePlan(ctx, auth); errProbe != nil {
			log.WithField("auth", auth.ID).Debugf("claude plan probe: %v", errProbe)
		}
		state.MarkPlanProbe(auth.ID, time.Now())
	}
}

// readClaudePlan reads one Claude account's subscription facts and records
// them.
func (s *Service) readClaudePlan(ctx context.Context, auth *coreauth.Auth) error {
	token := claudeOAuthToken(auth)
	if token == "" {
		return errors.New("account has no subscription sign-in")
	}
	body, errProbe := s.fetchClaudeOAuth(ctx, auth, token, claudeProfileURL)
	if errProbe != nil {
		return errProbe
	}
	facts := coreauth.ParseClaudeProfilePlan(body, time.Now())
	if facts == nil {
		return errors.New("the profile names no organization")
	}
	coreauth.DefaultRoutingState().ObservePlan(auth.ID, facts)
	return nil
}
