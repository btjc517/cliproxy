package usagestats

import (
	"math"
	"strings"
	"time"
)

// The API cost prices usage at the public API list prices: standard tier,
// global endpoint, in USD per million tokens, as checked on pricesAsOf.
// Sources: https://platform.claude.com/docs/en/about-claude/pricing and
// https://developers.openai.com/api/docs/pricing.
const (
	pricesAsOf  = "2026-10-06"
	notModelled = "batch, fast mode, US-only inference, web search fees, 1-hour cache writes"
	// longContextTokens is the input size, uncached plus cache read plus
	// cache write, above which one OpenAI request is priced at the long
	// context rates.
	longContextTokens = 272_000
	// fallbackDays is how many days of the history's per-model usage, ending
	// at its newest day, pick each provider's fallback model.
	fallbackDays = 30
)

// tokenPrices are USD per million tokens.
type tokenPrices struct {
	input, cacheWrite, cacheRead, output float64
}

// modelPrice is one model's prices. long applies to a single request whose
// input is above longContextTokens; nil means the model has no surcharge.
type modelPrice struct {
	standard tokenPrices
	long     *tokenPrices
}

// claudePrice takes the input, 5-minute cache write, cache read and output
// prices. Claude 4.6 and later have no long context surcharge.
func claudePrice(input, cacheWrite, cacheRead, output float64) modelPrice {
	return modelPrice{standard: tokenPrices{input: input, cacheWrite: cacheWrite, cacheRead: cacheRead, output: output}}
}

// openAIPrice takes the input, cached input and output prices, then the same
// three for long context; zero long prices mean no surcharge. A cache write
// is priced as input.
func openAIPrice(input, cached, output, longInput, longCached, longOutput float64) modelPrice {
	price := modelPrice{standard: tokenPrices{input: input, cacheWrite: input, cacheRead: cached, output: output}}
	if longInput != 0 {
		price.long = &tokenPrices{input: longInput, cacheWrite: longInput, cacheRead: longCached, output: longOutput}
	}
	return price
}

// modelPrices maps a model id prefix to its prices. lookupPrice takes the
// longest prefix that ends at a word boundary of the id.
var modelPrices = func() map[string]modelPrice {
	prices := make(map[string]modelPrice)
	set := func(price modelPrice, ids ...string) {
		for _, id := range ids {
			prices[id] = price
		}
	}
	set(claudePrice(10, 12.50, 0.25, 50), "claude-fable-5-1", "claude-mythos-5-1")
	set(claudePrice(10, 12.50, 1, 50), "claude-fable-5", "claude-mythos-5")
	set(claudePrice(4, 5, 0.20, 20), "claude-opus-5-5")
	set(claudePrice(5, 6.25, 0.50, 25), "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5")
	set(claudePrice(15, 18.75, 1.50, 75), "claude-opus-4-1", "claude-opus-4")
	set(claudePrice(2, 2.50, 0.20, 10), "claude-sonnet-5-5", "claude-sonnet-5")
	set(claudePrice(3, 3.75, 0.30, 15), "claude-sonnet-4-6", "claude-sonnet-4-5", "claude-sonnet-4", "claude-3-7-sonnet")
	set(claudePrice(1, 1.25, 0.10, 5), "claude-haiku-4-5")
	set(claudePrice(0.80, 1, 0.08, 4), "claude-3-5-haiku", "claude-haiku-3-5")

	set(openAIPrice(2, 0.10, 10, 4, 0.20, 15), "gpt-6.1-sol")
	set(openAIPrice(10, 1, 50, 20, 2, 75), "gpt-6-astra")
	set(openAIPrice(2, 0.20, 10, 4, 0.40, 15), "gpt-6-sol")
	set(openAIPrice(0.10, 0.01, 0.50, 0.20, 0.02, 0.75), "gpt-6-luna")
	set(openAIPrice(4, 0.40, 20, 0, 0, 0), "gpt-5.6-sol")
	set(openAIPrice(2, 0.20, 12, 0, 0, 0), "gpt-5.6-terra")
	set(openAIPrice(0.20, 0.02, 1.20, 0, 0, 0), "gpt-5.6-luna")
	set(openAIPrice(5, 0.50, 30, 10, 1, 45), "gpt-5.5")
	return prices
}()

// modelBoundaries are the characters that may follow a table prefix in a
// model id: dated ids, "[1m]" context suffixes, Vertex "@" versions and
// "(high)" thinking suffixes.
const modelBoundaries = "-[.@:_("

// defaultFallback is each provider's fallback model when the history has no
// per-model usage for it.
func defaultFallback() map[string]string {
	return map[string]string{"claude": "claude-opus-5-5", "codex": "gpt-6-astra"}
}

// lookupPrice finds the table entry for a model id. The id is lower-cased and
// loses any "models/" or provider prefix such as "anthropic/", and the
// longest table prefix followed by the end of the id or a boundary wins, so
// claude-opus-5-5 never matches claude-opus-5.
func lookupPrice(model string) (string, modelPrice, bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(id, "/"); slash >= 0 {
		id = id[slash+1:]
	}
	best := ""
	for prefix := range modelPrices {
		if len(prefix) <= len(best) || !strings.HasPrefix(id, prefix) {
			continue
		}
		if len(id) == len(prefix) || strings.IndexByte(modelBoundaries, id[len(prefix)]) >= 0 {
			best = prefix
		}
	}
	if best == "" {
		return "", modelPrice{}, false
	}
	return best, modelPrices[best], true
}

// roundCost keeps a cost to the nearest billionth of a dollar, so a total is
// the same whatever order its parts were added in.
func roundCost(usd float64) float64 { return math.Round(usd*1e9) / 1e9 }

// cost prices counters. perRequest says they are one request, the only case
// where the long context rates can apply.
func (p modelPrice) cost(counters Counters, perRequest bool) float64 {
	prices := p.standard
	if perRequest && p.long != nil && counters.Input+counters.CacheRead+counters.CacheWrite > longContextTokens {
		prices = *p.long
	}
	perMillion := float64(counters.Input)*prices.input +
		float64(counters.CacheWrite)*prices.cacheWrite +
		float64(counters.CacheRead)*prices.cacheRead +
		float64(counters.Output)*prices.output
	return roundCost(perMillion / 1e6)
}

// costFor prices counters at model, or at the provider's fallback model when
// the table does not know model. An unknown model of a provider without a
// fallback costs nothing.
func costFor(model, provider string, counters Counters, fallback map[string]string, perRequest bool) float64 {
	price, ok := modelPrice{}, false
	if model != "" {
		_, price, ok = lookupPrice(model)
	}
	if !ok {
		_, price, ok = lookupPrice(fallback[provider])
	}
	if !ok {
		return 0
	}
	return price.cost(counters, perRequest)
}

// fallbackModels picks each provider's fallback model: the priced model with
// the most tokens across the last fallbackDays days of the history's
// per-model usage, ending at its newest day, or the static default. Model
// ids that share a table entry, such as a "[1m]" variant, count together.
func fallbackModels(history *historyFile) map[string]string {
	fallback := defaultFallback()
	if history == nil {
		return fallback
	}
	var newest time.Time
	for day, providers := range history.Models {
		if parsed, errParse := time.Parse(dayLayout, day); errParse == nil && len(providers) > 0 && parsed.After(newest) {
			newest = parsed
		}
	}
	if newest.IsZero() {
		return fallback
	}
	first := newest.AddDate(0, 0, 1-fallbackDays)
	tokens := make(map[string]map[string]int64)
	for day, providers := range history.Models {
		parsed, errParse := time.Parse(dayLayout, day)
		if errParse != nil || parsed.Before(first) {
			continue
		}
		for provider, models := range providers {
			for model, counters := range models {
				name, _, ok := lookupPrice(model)
				if !ok {
					continue
				}
				if tokens[provider] == nil {
					tokens[provider] = make(map[string]int64)
				}
				tokens[provider][name] += counters.tokens()
			}
		}
	}
	for provider, byModel := range tokens {
		best, bestTokens := "", int64(0)
		for name, count := range byModel {
			if count > bestTokens || (count == bestTokens && count > 0 && name < best) {
				best, bestTokens = name, count
			}
		}
		if best != "" {
			fallback[provider] = best
		}
	}
	return fallback
}

// hasModels reports whether the history splits at least one day by model.
func (h *historyFile) hasModels() bool {
	for _, providers := range h.Models {
		for _, models := range providers {
			if len(models) > 0 {
				return true
			}
		}
	}
	return false
}

// priceHistory sets the API cost of each backfill day and provider that has
// none: each model's tokens at that model's price, and any tokens the models
// do not cover at the fallback. A day without models is priced at the
// fallback. It runs at load and changes only the copy in memory.
func priceHistory(history *historyFile, fallback map[string]string) {
	for day, providers := range history.Days {
		for provider, total := range providers {
			if total.APICost != 0 {
				continue
			}
			rest := total
			for model, counters := range history.Models[day][provider] {
				total.APICost = roundCost(total.APICost + costFor(model, provider, counters, fallback, false))
				rest.Input -= counters.Input
				rest.Output -= counters.Output
				rest.CacheRead -= counters.CacheRead
				rest.CacheWrite -= counters.CacheWrite
			}
			rest.Input = max(rest.Input, 0)
			rest.Output = max(rest.Output, 0)
			rest.CacheRead = max(rest.CacheRead, 0)
			rest.CacheWrite = max(rest.CacheWrite, 0)
			total.APICost = roundCost(total.APICost + costFor("", provider, rest, fallback, false))
			providers[provider] = total
		}
	}
}

// estimateCost prices the counters of a stats file written before
// per-request pricing at each provider's fallback model. Sessions use their
// own model when the table knows it. Counters that already have a cost or
// have no tokens are left alone.
func estimateCost(state *fileState, fallback map[string]string) {
	estimate := func(provider string, counters *Counters) {
		if counters != nil && counters.APICost == 0 && counters.tokens() != 0 {
			counters.APICost = costFor("", provider, *counters, fallback, false)
		}
	}
	authProvider := func(authID string) string {
		if provider := state.AuthProviders[authID]; provider != "" {
			return provider
		}
		return providerOf("", authID)
	}
	for authID, buckets := range state.Hourly {
		for _, bucket := range buckets {
			estimate(authProvider(authID), bucket)
		}
	}
	for authID, days := range state.AccountDaily {
		for _, bucket := range days {
			estimate(authProvider(authID), bucket)
		}
	}
	for _, providers := range state.Daily {
		for provider, bucket := range providers {
			estimate(provider, bucket)
		}
	}
	for _, session := range state.Sessions {
		if session == nil || session.APICost != 0 || session.tokens() == 0 {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(session.Provider))
		if provider == "" {
			authID := session.ServingAuthID
			if authID == "" && len(session.AuthIDs) > 0 {
				authID = session.AuthIDs[0]
			}
			provider = authProvider(authID)
		}
		session.APICost = costFor(session.Model, provider, session.Counters, fallback, false)
	}
}

// Pricing tells the dashboard how API cost was worked out.
type Pricing struct {
	// AsOf is the date the price table was checked.
	AsOf string `json:"as_of"`
	// ExactSince is when per-request pricing started; earlier usage is
	// estimated.
	ExactSince time.Time `json:"exact_since"`
	// HistoryByModel is true when the history file splits at least one day
	// by model.
	HistoryByModel bool `json:"history_by_model"`
	// Fallback names the model each provider's unknown and earlier usage is
	// priced at.
	Fallback    map[string]string `json:"fallback"`
	NotModelled string            `json:"not_modelled"`
}

func (s *Store) pricingLocked() Pricing {
	fallback := make(map[string]string, len(s.fallback))
	for provider, model := range s.fallback {
		fallback[provider] = model
	}
	return Pricing{
		AsOf:           pricesAsOf,
		ExactSince:     s.costSince,
		HistoryByModel: s.backfill != nil && s.backfill.hasModels(),
		Fallback:       fallback,
		NotModelled:    notModelled,
	}
}
