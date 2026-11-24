package auth

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// PlanFacts is what a provider reports about an account's subscription.
type PlanFacts struct {
	// StartedAt is when the current subscription started. The provider bills
	// again at this day and time each month.
	StartedAt time.Time `json:"started_at,omitzero"`
	// Status is the provider's word for the subscription ("active").
	Status string `json:"status,omitempty"`
	// ObservedAt is when the facts were last read.
	ObservedAt time.Time `json:"observed_at"`
}

// Active reports whether the subscription is one that bills again.
func (p PlanFacts) Active() bool {
	return !p.StartedAt.IsZero() && (p.Status == "" || strings.EqualFold(p.Status, "active"))
}

// ParseClaudeProfilePlan reads the subscription facts in the body of
// GET /api/oauth/profile. It returns nil when the body names no organization,
// so a failed read leaves the last facts in place.
func ParseClaudeProfilePlan(body []byte, now time.Time) *PlanFacts {
	organization := gjson.GetBytes(body, "organization")
	if !organization.IsObject() {
		return nil
	}
	return &PlanFacts{
		StartedAt:  parseResetTime(organization.Get("subscription_created_at").String()),
		Status:     strings.TrimSpace(organization.Get("subscription_status").String()),
		ObservedAt: now,
	}
}

// ObservePlan records an account's subscription facts.
func (s *RoutingState) ObservePlan(authID string, facts *PlanFacts) {
	if s == nil || facts == nil || strings.TrimSpace(authID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accountLocked(authID)
	copyFacts := *facts
	account.Plan = &copyFacts
	s.dirty = true
}

// Plan returns the account's recorded subscription facts.
func (s *RoutingState) Plan(authID string) (PlanFacts, bool) {
	if s == nil {
		return PlanFacts{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[authID]
	if account == nil || account.Plan == nil {
		return PlanFacts{}, false
	}
	return *account.Plan, true
}

// NeedsPlanReading reports whether a probe should read an account's
// subscription: it has no facts newer than maxAge and was not asked within
// maxAge, so an account that cannot be read is asked once per maxAge.
func (s *RoutingState) NeedsPlanReading(authID string, now time.Time, maxAge time.Duration) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[authID]
	if account == nil {
		return true
	}
	if !account.planProbeAt.IsZero() && now.Sub(account.planProbeAt) < maxAge {
		return false
	}
	return account.Plan == nil || now.Sub(account.Plan.ObservedAt) >= maxAge
}

// MarkPlanProbe records that an account's subscription was asked for.
func (s *RoutingState) MarkPlanProbe(authID string, now time.Time) {
	if s == nil || strings.TrimSpace(authID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountLocked(authID).planProbeAt = now
}

// accountLocked returns the account's record, creating it when missing.
func (s *RoutingState) accountLocked(authID string) *accountRouting {
	account := s.accounts[authID]
	if account == nil {
		account = &accountRouting{Meters: make(map[string]*Meter), History: make(map[string][]MeterSample)}
		s.accounts[authID] = account
	}
	return account
}

// NextMonthlyBill returns the first time after now that falls on the anchor's
// day of the month and time of day. Subscriptions bill each month at the
// moment they started; a start on the 31st bills on the last day of a shorter
// month. An anchor still ahead of now is itself the next bill.
func NextMonthlyBill(anchor, now time.Time) time.Time {
	if anchor.IsZero() {
		return time.Time{}
	}
	anchor = anchor.UTC()
	now = now.UTC()
	months := (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	if months < 0 {
		months = 0
	}
	for step := months; ; step++ {
		first := time.Date(anchor.Year(), anchor.Month()+time.Month(step), 1, 0, 0, 0, 0, time.UTC)
		day := anchor.Day()
		if last := first.AddDate(0, 1, -1).Day(); day > last {
			day = last
		}
		bill := time.Date(first.Year(), first.Month(), day, anchor.Hour(), anchor.Minute(), anchor.Second(), 0, time.UTC)
		if bill.After(now) {
			return bill
		}
	}
}
