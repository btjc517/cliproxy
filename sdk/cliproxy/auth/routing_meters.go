package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Meter is one upstream usage meter on a credential: Claude's 5-hour or weekly
// window, a per-model weekly window, or a Codex rate-limit window.
type Meter struct {
	// Name identifies the meter: the Anthropic claim ("5h", "7d", "7d_opus") or
	// the Codex window ("primary", "secondary", "<limit>-primary").
	Name string `json:"name"`
	// Label is the upstream display name, when the provider sends one.
	Label string `json:"label,omitempty"`
	// Utilization is the used share of the window, from 0 to 1.
	Utilization float64 `json:"utilization"`
	// ResetAt is when the window resets, as last reported.
	ResetAt time.Time `json:"reset_at,omitempty"`
	// Window is the window length, when known.
	Window time.Duration `json:"window,omitempty"`
	// Status is the upstream verdict for the window ("allowed", "allowed_warning", "rejected").
	Status string `json:"status,omitempty"`
	// Models lists the models whose responses reported this meter.
	Models []string `json:"models,omitempty"`
	// ObservedAt is when the meter was last reported.
	ObservedAt time.Time `json:"observed_at"`
}

// MeterSample is one historical utilization reading of a meter.
type MeterSample struct {
	At          time.Time `json:"t"`
	Utilization float64   `json:"u"`
}

// AccountFacts are credential-level quota facts that are not meters.
type AccountFacts struct {
	// BindingClaim is the meter Anthropic reports as currently limiting the account.
	BindingClaim string `json:"binding_claim,omitempty"`
	// Status is the overall upstream verdict ("allowed", "rejected").
	Status string `json:"status,omitempty"`
	// OverageStatus reports whether paid extra usage would be used.
	OverageStatus string `json:"overage_status,omitempty"`
	// ActiveLimit is the Codex limit that applied to the last response.
	ActiveLimit string `json:"active_limit,omitempty"`
	// LastModel is the model of the last observed response.
	LastModel  string    `json:"last_model,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

const (
	maxMeterModels = 8
	fiveHourWindow = 5 * time.Hour
	// longWindowMin separates long (weekly) windows from short (5-hour) ones.
	longWindowMin = 24 * time.Hour
)

// parseMeters reads the meters and account facts carried by one response.
func parseMeters(provider string, headers http.Header, now time.Time) (map[string]*Meter, AccountFacts) {
	signals := collectQuotaSignals(provider, headers)
	if len(signals) == 0 {
		return nil, AccountFacts{}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return parseClaudeMeters(signals, now)
	case "codex":
		return parseCodexMeters(signals, now)
	default:
		return nil, AccountFacts{}
	}
}

func parseClaudeMeters(signals map[string]string, now time.Time) (map[string]*Meter, AccountFacts) {
	meters := make(map[string]*Meter)
	facts := AccountFacts{}
	meter := func(claim string) *Meter {
		if existing := meters[claim]; existing != nil {
			return existing
		}
		created := &Meter{Name: claim, Window: claudeClaimWindow(claim), ObservedAt: now}
		meters[claim] = created
		return created
	}
	for name, value := range signals {
		rest, ok := strings.CutPrefix(strings.ToLower(name), "anthropic-ratelimit-unified-")
		if !ok {
			continue
		}
		switch rest {
		case "representative-claim":
			facts.BindingClaim = value
			continue
		case "status":
			facts.Status = value
			continue
		case "overage-status":
			facts.OverageStatus = value
			continue
		}
		if claim, found := strings.CutSuffix(rest, "-utilization"); found && claim != "" {
			if parsed, errParse := strconv.ParseFloat(value, 64); errParse == nil {
				meter(claim).Utilization = parsed
			}
		} else if claim, found = strings.CutSuffix(rest, "-reset"); found && claim != "" {
			if parsed, okParse := parseQuotaResetTime(value); okParse {
				meter(claim).ResetAt = parsed
			}
		} else if claim, found = strings.CutSuffix(rest, "-status"); found && claim != "" && claim != "overage" {
			meter(claim).Status = value
		}
	}
	return meters, facts
}

// claudeClaimWindow infers the window length from a claim name such as
// "5h", "7d" or "7d_opus".
func claudeClaimWindow(claim string) time.Duration {
	switch {
	case strings.HasPrefix(claim, "5h"), strings.HasPrefix(claim, "five_hour"):
		return fiveHourWindow
	case strings.HasPrefix(claim, "7d"), strings.HasPrefix(claim, "seven_day"):
		return weeklyQuotaWindow
	default:
		return 0
	}
}

func parseCodexMeters(signals map[string]string, now time.Time) (map[string]*Meter, AccountFacts) {
	meters := make(map[string]*Meter)
	facts := AccountFacts{}
	limitNames := make(map[string]string)
	resetAfter := make(map[string]time.Duration)
	for name, value := range signals {
		rest, ok := strings.CutPrefix(strings.ToLower(name), "x-codex-")
		if !ok {
			continue
		}
		switch rest {
		case "active-limit":
			facts.ActiveLimit = value
			continue
		case "limit-reached":
			if strings.EqualFold(value, "true") {
				facts.Status = "rejected"
			}
			continue
		}
		if namespace, found := strings.CutSuffix(rest, "-limit-name"); found {
			limitNames[strings.TrimPrefix(namespace, "additional-")] = value
			continue
		}
		meterName, field, found := splitCodexMeterHeader(rest)
		if !found || strings.HasPrefix(meterName, "code-review") {
			continue
		}
		meter := meters[meterName]
		if meter == nil {
			meter = &Meter{Name: meterName, ObservedAt: now}
			meters[meterName] = meter
		}
		switch field {
		case "used-percent":
			if parsed, errParse := strconv.ParseFloat(value, 64); errParse == nil {
				meter.Utilization = parsed / 100
			}
		case "window-minutes":
			if parsed, errParse := strconv.ParseInt(value, 10, 64); errParse == nil && parsed > 0 {
				meter.Window = time.Duration(parsed) * time.Minute
			}
		case "reset-at":
			if parsed, okParse := parseQuotaResetTime(value); okParse {
				meter.ResetAt = parsed
			}
		case "reset-after-seconds":
			if parsed, errParse := strconv.ParseInt(value, 10, 64); errParse == nil && parsed >= 0 {
				resetAfter[meterName] = time.Duration(parsed) * time.Second
			}
		}
	}
	for name, after := range resetAfter {
		if meter := meters[name]; meter != nil && meter.ResetAt.IsZero() {
			meter.ResetAt = now.Add(after)
		}
	}
	for name, meter := range meters {
		namespace := strings.TrimSuffix(strings.TrimSuffix(name, "-primary"), "-secondary")
		meter.Label = limitNames[namespace]
	}
	return meters, facts
}

// splitCodexMeterHeader splits a Codex rate-limit header (without the x-codex-
// prefix) into a meter name and a field. "primary-used-percent" gives
// ("primary", "used-percent"); "bengalfox-secondary-reset-at" gives
// ("bengalfox-secondary", "reset-at").
func splitCodexMeterHeader(rest string) (string, string, bool) {
	for _, kind := range []string{"primary", "secondary"} {
		if field, ok := strings.CutPrefix(rest, kind+"-"); ok {
			return kind, field, true
		}
		if index := strings.Index(rest, "-"+kind+"-"); index > 0 {
			namespace := strings.TrimPrefix(rest[:index], "additional-")
			return namespace + "-" + kind, rest[index+len(kind)+2:], true
		}
	}
	return "", "", false
}

// meterIsLong reports whether the meter is a weekly-class window.
func meterIsLong(meter *Meter) bool {
	if meter == nil {
		return false
	}
	if meter.Window > 0 {
		return meter.Window >= longWindowMin
	}
	return strings.HasPrefix(meter.Name, "7d") || strings.HasSuffix(meter.Name, "secondary")
}

// meterIsShort reports whether the meter is a 5-hour-class window.
func meterIsShort(meter *Meter) bool {
	if meter == nil {
		return false
	}
	if meter.Window > 0 {
		return meter.Window < longWindowMin
	}
	return strings.HasPrefix(meter.Name, "5h") || strings.HasSuffix(meter.Name, "primary")
}

// claudeModelFamilies are the model families Anthropic may meter separately.
var claudeModelFamilies = []string{"fable", "opus", "sonnet", "haiku"}

// claudeFableClaim is the claim Anthropic's rate-limit headers use for the
// weekly Fable allowance ("seven_day_overage_included" in its usage report,
// "Fable limit" in Claude Code). Only Fable responses carry it.
const claudeFableClaim = "7d_oi"

func claudeModelFamily(model string) string {
	lower := strings.ToLower(model)
	// Mythos is the same model as Fable and draws on the same allowance.
	if strings.Contains(lower, "mythos") {
		return "fable"
	}
	for _, family := range claudeModelFamilies {
		if strings.Contains(lower, family) {
			return family
		}
	}
	return ""
}

// claudeClaimFamily returns the model family a claim meters on its own, or ""
// for a claim that is not tied to one family by name.
func claudeClaimFamily(claim string) string {
	if strings.EqualFold(claim, claudeFableClaim) {
		return "fable"
	}
	return claudeModelFamily(claim)
}

// meterAppliesToModel reports whether a request for model draws on meter.
//
// Claude's plain "5h" and "7d" claims cover every model. A claim that names a
// model family ("7d_opus") covers that family only. The Fable claim covers
// Fable requests only, so a spent Fable week never holds back other models or
// a ranking that names no model. Any other claim covers the models whose
// responses reported it, or every model before one has.
// Codex's plain primary and secondary windows cover every model; its named
// additional limits are shown but not applied, because the headers do not say
// which models they cover.
func meterAppliesToModel(provider string, meter *Meter, model string) bool {
	if meter == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		if meter.Name == "5h" || meter.Name == "7d" {
			return true
		}
		if strings.EqualFold(meter.Name, claudeFableClaim) {
			return claudeModelFamily(model) == "fable"
		}
		if family := claudeClaimFamily(meter.Name); family != "" {
			return model == "" || claudeModelFamily(model) == family
		}
		if len(meter.Models) == 0 || model == "" {
			return true
		}
		family := claudeModelFamily(model)
		for _, seen := range meter.Models {
			if strings.EqualFold(seen, model) || (family != "" && claudeModelFamily(seen) == family) {
				return true
			}
		}
		return false
	case "codex":
		return meter.Name == "primary" || meter.Name == "secondary"
	default:
		return false
	}
}

// meterSpent reports whether the meter, as it stands at now, has no allowance
// left: the provider rejected the window or it is at fullMeterUtilization, and
// its reset is still ahead.
func meterSpent(meter Meter, now time.Time) (Meter, bool) {
	effective, wasReset := effectiveMeter(meter, now)
	if wasReset || !effective.ResetAt.After(now) {
		return effective, false
	}
	return effective, strings.EqualFold(effective.Status, "rejected") || effective.Utilization >= fullMeterUtilization
}

// modelAllowanceSpent reports whether the credential has used up an allowance
// that only the requested model's family draws on, such as the weekly Fable
// allowance or a "7d_opus" week, and when that allowance resets. The shared
// 5-hour and weekly windows are not looked at: the provider's own refusal and
// the cooldown it starts already move work off a credential that is out of
// those.
func (s *RoutingState) modelAllowanceSpent(authID, provider, model string, now time.Time) (Meter, bool) {
	family := claudeModelFamily(model)
	if s == nil || family == "" || !strings.EqualFold(strings.TrimSpace(provider), "claude") {
		return Meter{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return familyAllowanceSpent(s.accounts[authID], family, now)
}

func familyAllowanceSpent(account *accountRouting, family string, now time.Time) (Meter, bool) {
	if account == nil {
		return Meter{}, false
	}
	for _, meter := range account.Meters {
		if meter == nil || claudeClaimFamily(meter.Name) != family {
			continue
		}
		if effective, spent := meterSpent(*meter, now); spent {
			return effective, true
		}
	}
	return Meter{}, false
}

// modelAllowanceOpen returns the candidates that can take a session of model
// from a credential that has used up the model family's own allowance: those
// that still have that allowance and that the provider has not recently
// refused. anySpent reports whether any candidate is out of the allowance.
func (s *RoutingState) modelAllowanceOpen(auths []*Auth, model string, now time.Time) (open []*Auth, anySpent bool) {
	family := claudeModelFamily(model)
	if s == nil || family == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range auths {
		if candidate == nil {
			continue
		}
		account := s.accounts[candidate.ID]
		if strings.EqualFold(strings.TrimSpace(candidate.Provider), "claude") {
			if _, spent := familyAllowanceSpent(account, family, now); spent {
				anySpent = true
				continue
			}
		}
		if account != nil {
			if _, refused := account.Health.refusedReason(now); refused {
				continue
			}
		}
		open = append(open, candidate)
	}
	return open, anySpent
}

// withModelAllowance narrows candidates to the ones modelAllowanceOpen returns
// when some candidate is out of the model family's own allowance. When none
// is, or when no candidate could take the work, the list is returned
// unchanged, so routing never fails closed on the router's own data.
func (s *RoutingState) withModelAllowance(auths []*Auth, model string, now time.Time) []*Auth {
	if len(auths) < 2 {
		return auths
	}
	open, anySpent := s.modelAllowanceOpen(auths, model, now)
	if !anySpent || len(open) == 0 {
		return auths
	}
	return open
}

// effectiveMeter returns the meter as it stands at now: a window whose reset
// has passed starts again at zero, with its next reset rolled forward.
func effectiveMeter(meter Meter, now time.Time) (Meter, bool) {
	if meter.ResetAt.IsZero() || meter.ResetAt.After(now) {
		return meter, false
	}
	meter.Utilization = 0
	meter.Status = ""
	if meter.Window > 0 {
		periods := now.Sub(meter.ResetAt)/meter.Window + 1
		meter.ResetAt = meter.ResetAt.Add(periods * meter.Window)
	} else {
		meter.ResetAt = time.Time{}
	}
	return meter, true
}

func addMeterModel(models []string, model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return models
	}
	for _, existing := range models {
		if strings.EqualFold(existing, model) {
			return models
		}
	}
	models = append(models, model)
	if len(models) > maxMeterModels {
		models = models[len(models)-maxMeterModels:]
	}
	return models
}
