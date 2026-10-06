package auth

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// claudeUsageClaims maps the windows of Anthropic's OAuth usage report to the
// claim names the rate-limit headers use, so both sources fill the same meters.
var claudeUsageClaims = map[string]string{
	"five_hour":        "5h",
	"seven_day":        "7d",
	"seven_day_opus":   "7d_opus",
	"seven_day_sonnet": "7d_sonnet",
}

// ParseClaudeUsageMeters reads the body of GET /api/oauth/usage. The report
// gives utilization in percent; meters hold it as a fraction like the headers.
func ParseClaudeUsageMeters(body []byte, now time.Time) map[string]*Meter {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil
	}
	meters := make(map[string]*Meter)
	for field, claim := range claudeUsageClaims {
		window := root.Get(field)
		used := window.Get("utilization")
		if !window.IsObject() || used.Type != gjson.Number {
			continue
		}
		meter := &Meter{Name: claim, Window: claudeClaimWindow(claim), Utilization: used.Float() / 100, ObservedAt: now, Status: "allowed"}
		if meter.Utilization >= 1 {
			meter.Status = "rejected"
		}
		if resetAt, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(window.Get("resets_at").String())); errParse == nil {
			meter.ResetAt = resetAt
		}
		meters[claim] = meter
	}
	return meters
}

// ObserveMeters records meters read outside a proxied request, such as a usage
// probe of an idle account. Health and account facts stay as traffic left them.
func (s *RoutingState) ObserveMeters(authID, provider string, meters map[string]*Meter, now time.Time) {
	if s == nil || strings.TrimSpace(authID) == "" || len(meters) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[authID]
	if account == nil {
		account = &accountRouting{Meters: make(map[string]*Meter), History: make(map[string][]MeterSample)}
		s.accounts[authID] = account
	}
	if provider = strings.ToLower(strings.TrimSpace(provider)); provider != "" {
		account.Provider = provider
	}
	mergeMeters(account, meters, "", now)
	s.dirty = true
}

// NeedsMeterReading reports whether an account has no 5h or weekly reading
// newer than maxAge, so a probe should read its usage.
func (s *RoutingState) NeedsMeterReading(authID string, now time.Time, maxAge time.Duration) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[authID]
	if account == nil {
		return true
	}
	for _, name := range []string{"5h", "7d"} {
		if meter := account.Meters[name]; meter != nil && now.Sub(meter.ObservedAt) < maxAge {
			return false
		}
	}
	return true
}
