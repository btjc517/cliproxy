package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeAuthResettingAt(id string, resetAt time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(resetAt.Unix(), 10),
		}},
	}
}

func TestSoonestResetSelectorPick_PrefersEarliestWeeklyReset(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(5*24*time.Hour)),
		claudeAuthResettingAt("b", now.Add(2*time.Hour)),
		claudeAuthResettingAt("c", now.Add(3*24*time.Hour)),
	}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "b")
	}
}

func TestSoonestResetSelectorPick_UnobservedCredentialFirst(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(time.Hour)),
		{ID: "b", Provider: "claude"},
	}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() auth.ID = %q, want unobserved %q", got.ID, "b")
	}
}

func TestSoonestResetSelectorPick_SkipsCoolingCredential(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	soonest := claudeAuthResettingAt("a", now.Add(time.Hour))
	soonest.Quota.Exceeded = true
	soonest.Quota.Reason = "credential_quota"
	soonest.Quota.NextRecoverAt = now.Add(time.Hour)
	auths := []*Auth{soonest, claudeAuthResettingAt("b", now.Add(4*24*time.Hour))}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "b")
	}
}

func TestSoonestResetSelectorPick_RespectsPriority(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	reserve := claudeAuthResettingAt("reserve", now.Add(time.Hour))
	reserve.Attributes = map[string]string{"priority": "-1"}
	auths := []*Auth{reserve, claudeAuthResettingAt("main", now.Add(6*24*time.Hour))}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "main" {
		t.Fatalf("Pick() auth.ID = %q, want higher-priority %q", got.ID, "main")
	}
}

func TestWeeklyQuotaResetAt_Codex(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	observed := now.Add(-time.Hour)
	tests := []struct {
		name    string
		signals map[string]string
		want    time.Time
		wantOK  bool
	}{
		{
			name: "secondary weekly window with reset-at",
			signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":   "300",
				"X-Codex-Primary-Reset-At":         strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
				"X-Codex-Secondary-Window-Minutes": "10080",
				"X-Codex-Secondary-Reset-At":       strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10),
			},
			want:   now.Add(48 * time.Hour),
			wantOK: true,
		},
		{
			name: "weekly window with reset-after-seconds only",
			signals: map[string]string{
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "7200",
			},
			want:   observed.Add(2 * time.Hour),
			wantOK: true,
		},
		{
			name: "no weekly window",
			signals: map[string]string{
				"X-Codex-Primary-Window-Minutes": "300",
				"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
			},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: observed, Signals: tt.signals}}
			got, ok := WeeklyQuotaResetAt(auth, now)
			if ok != tt.wantOK {
				t.Fatalf("WeeklyQuotaResetAt() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && !got.Equal(tt.want) {
				t.Fatalf("WeeklyQuotaResetAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWeeklyQuotaResetAt_RollsPastResetForward(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	auth := claudeAuthResettingAt("a", now.Add(-2*time.Hour))

	got, ok := WeeklyQuotaResetAt(auth, now)
	if !ok {
		t.Fatal("WeeklyQuotaResetAt() ok = false, want true")
	}
	want := now.Add(-2*time.Hour + weeklyQuotaWindow)
	if !got.Equal(want) {
		t.Fatalf("WeeklyQuotaResetAt() = %v, want %v", got, want)
	}
}

func TestSoonestResetSelectorPick_PrimesCredentialWhoseResetPassed(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(2*time.Hour)),
		claudeAuthResettingAt("b", now.Add(-30*time.Minute)),
	}

	plain := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	got, err := plain.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "a" {
		t.Fatalf("without priming Pick() = %q, want %q (b's reset rolls a week forward)", got.ID, "a")
	}

	priming := NewSoonestResetSelector([]string{" Claude "})
	priming.nowFunc = func() time.Time { return now }
	got, err = priming.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("with priming Pick() = %q, want just-reset %q", got.ID, "b")
	}
}

func TestSoonestResetSelectorPick_PrimingIsPerProvider(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	selector := NewSoonestResetSelector([]string{"codex"})
	selector.nowFunc = func() time.Time { return now }
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(2*time.Hour)),
		claudeAuthResettingAt("b", now.Add(-30*time.Minute)),
	}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "a" {
		t.Fatalf("Pick() = %q, want %q: Claude is not a priming provider here", got.ID, "a")
	}
}

func TestWeeklyQuotaResetPassed(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	if WeeklyQuotaResetPassed(claudeAuthResettingAt("a", now.Add(time.Minute)), now) {
		t.Fatal("future reset reported as passed")
	}
	if !WeeklyQuotaResetPassed(claudeAuthResettingAt("a", now.Add(-time.Minute)), now) {
		t.Fatal("past reset not reported as passed")
	}
	if WeeklyQuotaResetPassed(&Auth{ID: "a", Provider: "claude"}, now) {
		t.Fatal("credential with no snapshot reported as passed")
	}
}
