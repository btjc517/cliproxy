package auth

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	weeklyQuotaWindow = 7 * 24 * time.Hour
	// minWeeklyWindowMinutes accepts Codex windows reported as roughly one week.
	minWeeklyWindowMinutes = 6 * 24 * 60
)

// SoonestResetSelector picks the available credential whose weekly quota window
// resets soonest, so allowance that is about to expire is spent first.
//
// Reset times come from the passive quota snapshot each upstream response leaves
// on the credential (QuotaState.Signals). Credentials with no snapshot yet are
// picked first so one request can learn their reset time. Ties keep the
// ID-sorted candidate order, which makes the choice deterministic.
type SoonestResetSelector struct {
	// nowFunc overrides the clock in tests.
	nowFunc func() time.Time
}

// Pick selects the credential with the earliest known weekly reset.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	if s != nil && s.nowFunc != nil {
		now = s.nowFunc()
	}
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	if len(available) == 1 {
		return available[0], nil
	}

	type ranked struct {
		auth    *Auth
		resetAt time.Time
		known   bool
	}
	candidates := make([]ranked, len(available))
	for i, candidate := range available {
		resetAt, known := WeeklyQuotaResetAt(candidate, now)
		candidates[i] = ranked{auth: candidate, resetAt: resetAt, known: known}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].known != candidates[j].known {
			return !candidates[i].known
		}
		return candidates[i].resetAt.Before(candidates[j].resetAt)
	})
	return candidates[0].auth, nil
}

// WeeklyQuotaResetAt reports when the credential's weekly quota window next
// resets, based on the last observed upstream quota headers. A reset time that
// has already passed is rolled forward by whole weeks.
func WeeklyQuotaResetAt(auth *Auth, now time.Time) (time.Time, bool) {
	if auth == nil || len(auth.Quota.Signals) == 0 {
		return time.Time{}, false
	}
	signals := auth.Quota.Signals
	var resetAt time.Time
	window := weeklyQuotaWindow
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		parsed, ok := parseQuotaResetTime(quotaSignal(signals, "Anthropic-Ratelimit-Unified-7d-Reset"))
		if !ok {
			return time.Time{}, false
		}
		resetAt = parsed
	case "codex":
		parsed, minutes, ok := codexWeeklyReset(signals, auth.Quota.ObservedAt)
		if !ok {
			return time.Time{}, false
		}
		resetAt = parsed
		window = time.Duration(minutes) * time.Minute
	default:
		return time.Time{}, false
	}
	if !resetAt.After(now) {
		periods := now.Sub(resetAt)/window + 1
		resetAt = resetAt.Add(periods * window)
	}
	return resetAt, true
}

// codexWeeklyReset finds the Codex rate-limit window of about one week
// (primary or secondary, whichever reports it) and returns its reset time.
func codexWeeklyReset(signals map[string]string, observedAt time.Time) (time.Time, int64, bool) {
	for _, prefix := range []string{"X-Codex-Secondary-", "X-Codex-Primary-"} {
		minutes, errParse := strconv.ParseInt(quotaSignal(signals, prefix+"Window-Minutes"), 10, 64)
		if errParse != nil || minutes < minWeeklyWindowMinutes {
			continue
		}
		if resetAt, ok := parseQuotaResetTime(quotaSignal(signals, prefix+"Reset-At")); ok {
			return resetAt, minutes, true
		}
		after, errAfter := strconv.ParseInt(quotaSignal(signals, prefix+"Reset-After-Seconds"), 10, 64)
		if errAfter == nil && after >= 0 && !observedAt.IsZero() {
			return observedAt.Add(time.Duration(after) * time.Second), minutes, true
		}
	}
	return time.Time{}, 0, false
}

func quotaSignal(signals map[string]string, name string) string {
	if value, ok := signals[http.CanonicalHeaderKey(name)]; ok {
		return strings.TrimSpace(value)
	}
	for key, value := range signals {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// parseQuotaResetTime accepts unix seconds or an RFC 3339 timestamp.
func parseQuotaResetTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if sec, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && sec > 0 {
		return time.Unix(sec, 0), true
	}
	if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
		return parsed, true
	}
	return time.Time{}, false
}
