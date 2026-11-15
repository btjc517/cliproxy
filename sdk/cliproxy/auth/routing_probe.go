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
	add := func(claim string, percent float64, resetsAt string) {
		meter := &Meter{Name: claim, Window: claudeClaimWindow(claim), Utilization: percent / 100, ObservedAt: now, Status: "allowed"}
		if meter.Utilization >= 1 {
			meter.Status = "rejected"
		}
		if resetAt, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(resetsAt)); errParse == nil {
			meter.ResetAt = resetAt
		}
		meters[claim] = meter
	}
	for field, claim := range claudeUsageClaims {
		window := root.Get(field)
		used := window.Get("utilization")
		if !window.IsObject() || used.Type != gjson.Number {
			continue
		}
		add(claim, used.Float(), window.Get("resets_at").String())
	}
	// Allowances kept for one model, such as the weekly Fable allowance, come
	// as rows of kind "weekly_scoped" that name the model.
	rows := root.Get("limits")
	if rows.IsObject() {
		rows = rows.Get("limits")
	}
	rows.ForEach(func(_, row gjson.Result) bool {
		used := row.Get("percent")
		if row.Get("kind").String() != "weekly_scoped" || used.Type != gjson.Number {
			return true
		}
		switch family := claudeModelFamily(row.Get("scope.model.display_name").String()); family {
		case "":
		case "fable":
			add(claudeFableClaim, used.Float(), row.Get("resets_at").String())
		default:
			add("7d_"+family, used.Float(), row.Get("resets_at").String())
		}
		return true
	})
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
	// A usage report carries the Fable allowance whenever the account has one.
	account.fableProbeAt = now
	s.dirty = true
}

// NeedsMeterReading reports whether a probe should read an account's usage:
// it has no 5h or weekly reading newer than maxAge, or no Fable reading that
// new. Responses keep the 5h and weekly meters of a busy account fresh, but
// only a Fable response carries the Fable allowance, so an account serving
// other models is read once per maxAge to keep its Fable meter known.
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
	fresh := false
	for _, name := range []string{"5h", "7d"} {
		if meter := account.Meters[name]; meter != nil && now.Sub(meter.ObservedAt) < maxAge {
			fresh = true
		}
	}
	if !fresh {
		return true
	}
	if meter := account.Meters[claudeFableClaim]; meter != nil && now.Sub(meter.ObservedAt) < maxAge {
		return false
	}
	if !account.fableProbeAt.IsZero() && now.Sub(account.fableProbeAt) < maxAge {
		return false
	}
	account.fableProbeAt = now
	return true
}
