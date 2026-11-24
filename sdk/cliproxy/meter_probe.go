package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// claudeUsageURL is a variable so tests can point it at a local server.
var claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	// meterProbeInterval is how often accounts are checked, so a newly added
	// account shows its allowance within a minute. Only accounts without a
	// reading newer than meterProbeMaxAge are probed: those that carry traffic
	// read their meters from responses, and idle ones every 20 minutes.
	meterProbeInterval = time.Minute
	meterProbeFirstRun = time.Minute
	meterProbeMaxAge   = 20 * time.Minute
)

// startClaudeMeterProbe reads the usage of signed-in Claude accounts that have
// no recent meter reading, so idle accounts show and rank by real allowance.
// The same round reads each account's usage resets and, in automatic mode,
// uses one when it is worth it.
func (s *Service) startClaudeMeterProbe(ctx context.Context) {
	var running atomic.Bool
	run := func() {
		// A probe has no timeout, so a hung request must not stack rounds.
		if !running.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer running.Store(false)
			s.probeClaudeMeters(ctx)
			s.probeClaudePlans(ctx)
			s.probeCodexResets(ctx)
			s.runAutoReset(ctx)
		}()
	}
	go func() {
		timer := time.NewTimer(meterProbeFirstRun)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				run()
				timer.Reset(meterProbeInterval)
			}
		}
	}()
}

func (s *Service) probeClaudeMeters(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	state := coreauth.DefaultRoutingState()
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "claude") {
			continue
		}
		if claudeOAuthToken(auth) == "" {
			continue
		}
		// The same report carries the meters and the reset grants. A busy
		// account reads its meters from responses, so it is asked here once
		// per meterProbeMaxAge for its resets.
		now := time.Now()
		resetAge := meterProbeMaxAge
		if auth.Quota.Exceeded && auth.Quota.NextRecoverAt.After(now) {
			// A user can refill an account while the old retry deadline is still days away.
			resetAge = heldAccountProbeAge
		}
		if !state.NeedsMeterReading(auth.ID, now, meterProbeMaxAge) && !state.NeedsResetReading(auth.ID, now, resetAge) {
			continue
		}
		if errProbe := s.readClaudeUsage(ctx, auth); errProbe != nil {
			log.WithField("auth", auth.ID).Debugf("claude usage probe: %v", errProbe)
		}
		state.MarkResetProbe(auth.ID, time.Now())
	}
}

func (s *Service) fetchClaudeUsage(ctx context.Context, auth *coreauth.Auth, token string) ([]byte, error) {
	return s.fetchClaudeOAuth(ctx, auth, token, claudeUsageURL+"?"+claudeUsageResetQuery)
}

// fetchClaudeOAuth reads one of the endpoints a Claude subscription sign-in
// can call for its own account.
func (s *Service) fetchClaudeOAuth(ctx context.Context, auth *coreauth.Auth, token, endpoint string) ([]byte, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", helps.DefaultClaudeUserAgent(s.cfg))
	resp, errDo := helps.NewProxyAwareHTTPClient(ctx, s.cfg, auth, 0).Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("claude account probe: close body: %v", errClose)
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

type usageProbeStatusError struct{ status int }

func (e *usageProbeStatusError) Error() string {
	return "the provider answered " + http.StatusText(e.status)
}
