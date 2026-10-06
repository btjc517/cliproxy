package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// claudeUsageURL is a variable so tests can point it at a local server.
var claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	// meterProbeInterval is how often idle accounts are checked. Accounts that
	// carry traffic read their meters from responses and are skipped.
	meterProbeInterval = 10 * time.Minute
	meterProbeFirstRun = time.Minute
	meterProbeMaxAge   = 20 * time.Minute
)

// startClaudeMeterProbe reads the usage of signed-in Claude accounts that have
// no recent meter reading, so idle accounts show and rank by real allowance.
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
		token := claudeauth.ReadMetadataString(&auth.Metadata, "access_token")
		if !strings.Contains(token, "sk-ant-oat") || !state.NeedsMeterReading(auth.ID, time.Now(), meterProbeMaxAge) {
			continue
		}
		body, errProbe := s.fetchClaudeUsage(ctx, auth, token)
		if errProbe != nil {
			log.WithField("auth", auth.ID).Debugf("claude usage probe: %v", errProbe)
			continue
		}
		now := time.Now()
		if meters := coreauth.ParseClaudeUsageMeters(body, now); len(meters) > 0 {
			state.ObserveMeters(auth.ID, "claude", meters, now)
		}
	}
}

func (s *Service) fetchClaudeUsage(ctx context.Context, auth *coreauth.Auth, token string) ([]byte, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
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
			log.Debugf("claude usage probe: close body: %v", errClose)
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
	return "usage endpoint returned " + http.StatusText(e.status)
}
