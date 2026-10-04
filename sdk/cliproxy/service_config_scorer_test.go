package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestScorerModeBuildsSelector(t *testing.T) {
	scorer := internalconfig.RoutingScorerConfig{
		Headroom:              map[string]float64{" BTC@Example.com ": 0.7, "bad": 1.5, "zero": 0},
		MaxSessionsPerAccount: 4,
	}
	cases := map[string]func(coreauth.Selector) bool{
		"shadow": func(s coreauth.Selector) bool { _, ok := s.(*coreauth.ShadowSelector); return ok },
		"Live":   func(s coreauth.Selector) bool { _, ok := s.(*coreauth.ScoredSelector); return ok },
		"":       func(s coreauth.Selector) bool { _, ok := s.(*coreauth.SoonestResetSelector); return ok },
	}
	for mode, check := range cases {
		scorer.Mode = mode
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: "soonest-reset", Scorer: scorer},
		})
		if state.scorerHeadroom != "btc@example.com=0.7" || state.scorerMaxSessions != 4 {
			t.Fatalf("state = %+v, want only the valid headroom line", state)
		}
		selector := newRoutingSelector(state)
		if !check(selector) {
			t.Fatalf("mode %q built %T", mode, selector)
		}
	}
	if got := parseHeadroom("a@example.com=0.7,b@example.com=0.5"); got["a@example.com"] != 0.7 || got["b@example.com"] != 0.5 {
		t.Fatalf("parseHeadroom = %v", got)
	}
}

func TestScorerShadowModeKeepsSessionAffinityOutermost(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:        "soonest-reset",
			SessionAffinity: true,
			Scorer:          internalconfig.RoutingScorerConfig{Mode: "shadow"},
		},
	})
	selector, ok := newRoutingSelector(state).(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("selector type = %T, want session affinity outermost", newRoutingSelector(state))
	}
	defer selector.Stop()
	if mode, _ := coreauth.DefaultRoutingState().Scorer(); mode != "shadow" {
		t.Fatalf("routing state mode = %q, want shadow", mode)
	}
}
