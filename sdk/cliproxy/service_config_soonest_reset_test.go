package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSoonestResetRoutingSelector(t *testing.T) {
	for _, input := range []string{"soonest-reset", "soonestreset", "sr"} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: input},
		})
		if state.strategy != "soonest-reset" {
			t.Fatalf("strategy for %q = %q, want soonest-reset", input, state.strategy)
		}
		if _, ok := newRoutingSelector(state).(*coreauth.SoonestResetSelector); !ok {
			t.Fatalf("selector type = %T, want *auth.SoonestResetSelector", newRoutingSelector(state))
		}
	}
}

func TestSoonestResetRoutingSelectorPrimeAfterReset(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "soonest-reset", PrimeAfterReset: []string{"Codex", "claude", "codex", " "}},
	})
	if state.primeAfterReset != "claude,codex" {
		t.Fatalf("primeAfterReset = %q, want %q", state.primeAfterReset, "claude,codex")
	}
	selector, ok := newRoutingSelector(state).(*coreauth.SoonestResetSelector)
	if !ok {
		t.Fatalf("selector type = %T, want *auth.SoonestResetSelector", newRoutingSelector(state))
	}
	if !selector.PrimeProviders["claude"] || !selector.PrimeProviders["codex"] || len(selector.PrimeProviders) != 2 {
		t.Fatalf("PrimeProviders = %v, want claude and codex", selector.PrimeProviders)
	}
}
