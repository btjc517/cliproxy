package auth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// ResetGrant is one usage reset a provider has granted an account: a one-off
// that sets some of its limits back to zero before they reset by themselves.
type ResetGrant struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	// Total and Left count the uses the grant came with and has left.
	Total    int       `json:"total"`
	Left     int       `json:"left"`
	StartsAt time.Time `json:"starts_at,omitzero"`
	EndsAt   time.Time `json:"ends_at,omitzero"`
	// Clears names the meters a use sets back to zero ("5h", "7d", "7d_oi",
	// "primary", "secondary").
	Clears []string `json:"clears,omitempty"`
	// Blocking names limits the account is at that stop the grant being used.
	Blocking []string `json:"blocking,omitempty"`
	// UsableNow is the provider's own verdict that a use would go through.
	UsableNow bool `json:"usable_now"`
	// RequiresLimit means the grant only works while the account is at a
	// limit it clears.
	RequiresLimit bool `json:"requires_limit,omitempty"`
	// GivesBackWeekly means a use also returns to the shared week what the
	// cleared limit had taken from it.
	GivesBackWeekly bool `json:"gives_back_weekly,omitempty"`
	Paused          bool `json:"paused,omitempty"`
	// Unavailable is the provider's reason code when UsableNow is false.
	Unavailable string `json:"unavailable,omitempty"`
}

// ResetStatus is what a provider last reported about an account's resets.
type ResetStatus struct {
	Grants        []ResetGrant `json:"grants,omitempty"`
	CooldownUntil time.Time    `json:"cooldown_until,omitzero"`
	ObservedAt    time.Time    `json:"observed_at"`
}

// ResetEvent records one attempt to use a reset.
type ResetEvent struct {
	At       time.Time `json:"at"`
	AuthID   string    `json:"auth_id"`
	Provider string    `json:"provider,omitempty"`
	Account  string    `json:"account"`
	GrantID  string    `json:"grant_id"`
	Label    string    `json:"label,omitempty"`
	// By is "you" for the dashboard button and "auto" for automatic mode.
	By string `json:"by"`
	// Result is the provider's answer: "reset" when it worked, else why not.
	Result string `json:"result"`
	// Text says the outcome in plain words.
	Text string `json:"text"`
}

// ResetClaimResult is a provider's answer to a use of a reset.
type ResetClaimResult struct {
	// Result is "reset" on success. Other values: "already_used",
	// "not_limited", "cooldown", "ineligible", "unavailable", "no_credit".
	Result string
	// Left is the uses the grant has left, when the provider says.
	Left *int
	// Cleared names the meters the provider says it set back to zero.
	Cleared []string
}

// ResetBackend talks to the providers. The service registers one at start.
type ResetBackend interface {
	// ClaimReset uses one reset. requestID identifies the attempt, so a retry
	// with the same ID cannot use a second reset.
	ClaimReset(ctx context.Context, auth *Auth, grant ResetGrant, requestID string) (ResetClaimResult, error)
	// RefreshResets reads the account's usage and resets from the provider
	// again and records them.
	RefreshResets(ctx context.Context, auth *Auth) error
	// AfterReset lifts the account's cooldown after a reset went through.
	AfterReset(ctx context.Context, auth *Auth)
}

const (
	// ResetModeOff shows resets and advice and leaves every use to a person.
	ResetModeOff = "off"
	// ResetModeAuto also uses a reset by itself when the advice is to use it.
	ResetModeAuto = "auto"

	// resetReadingMaxAge is how old a reset reading may be and still be
	// advised on or used automatically.
	resetReadingMaxAge = 45 * time.Minute
	// minShortResetGain and minLongResetGain are how long before its own reset
	// a 5-hour-class or weekly-class limit must be for a reset to be worth it.
	minShortResetGain = 45 * time.Minute
	minLongResetGain  = 6 * time.Hour
	// resetExpirySoon is when an unused grant is close enough to expiring to
	// use on a limit that is only partly used.
	resetExpirySoon = 24 * time.Hour
	// resetExpiryNote is when advice starts to mention the expiry.
	resetExpiryNote = 72 * time.Hour
	// minExpiringGain is the used share that makes an expiring grant worth
	// using on an account that is not at its limit.
	minExpiringGain = 0.5
	maxResetEvents  = 50
	// resetRetryWindow is how long a failed attempt keeps its request ID, so a
	// retry is the same attempt to the provider.
	resetRetryWindow = 10 * time.Minute
	// autoResetGap is the least time between two automatic uses on accounts of
	// one provider, so a wrong reading cannot spend several resets in a row.
	autoResetGap = 30 * time.Minute
	// autoResetRetryGap is how long automatic mode leaves a reset alone after
	// the provider turned a use of it down.
	autoResetRetryGap = 6 * time.Hour
)

// claudeResetWindows maps the window names of Anthropic's reset grants to the
// claim names the meters use.
var claudeResetWindows = map[string]string{
	"five_hour":                  "5h",
	"seven_day":                  "7d",
	"seven_day_overage_included": claudeFableClaim,
	"seven_day_opus":             "7d_opus",
	"seven_day_sonnet":           "7d_sonnet",
}

// ClaudeResetWindow maps a window name of Anthropic's reset API to the claim
// name the meters use.
func ClaudeResetWindow(name string) string {
	name = strings.TrimSpace(name)
	if claim, ok := claudeResetWindows[name]; ok {
		return claim
	}
	if rest, ok := strings.CutPrefix(name, "seven_day_"); ok && rest != "" {
		return "7d_" + rest
	}
	return name
}

func parseResetTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if parsed, errParse := time.Parse(time.RFC3339Nano, raw); errParse == nil {
		return parsed
	}
	return time.Time{}
}

// ParseClaudeResetStatus reads the reset grants in the body of
// GET /api/oauth/usage?cedar_ember=1. It returns nil when the body carries no
// reset block, so a reading without one leaves the last status in place.
func ParseClaudeResetStatus(body []byte, now time.Time) *ResetStatus {
	block := gjson.GetBytes(body, "cedar_ember")
	if !block.IsObject() {
		return nil
	}
	status := &ResetStatus{ObservedAt: now, CooldownUntil: parseResetTime(block.Get("cooldown_until").String())}
	if !block.Get("eligible").Bool() {
		return status
	}
	block.Get("grants").ForEach(func(_, row gjson.Result) bool {
		id := strings.TrimSpace(row.Get("id").String())
		if id == "" || row.Get("resets_left").Type != gjson.Number {
			return true
		}
		grant := ResetGrant{
			ID:              id,
			Label:           strings.TrimSpace(row.Get("label").String()),
			Total:           int(row.Get("resets_total").Int()),
			Left:            int(row.Get("resets_left").Int()),
			StartsAt:        parseResetTime(row.Get("starts_at").String()),
			EndsAt:          parseResetTime(row.Get("ends_at").String()),
			UsableNow:       row.Get("usable_now").Bool(),
			RequiresLimit:   !row.Get("use_requires_limit").Exists() || row.Get("use_requires_limit").Bool(),
			GivesBackWeekly: row.Get("gives_back_weekly").Bool(),
			Paused:          row.Get("paused").Bool(),
			Unavailable:     strings.TrimSpace(row.Get("unavailable_reason").String()),
		}
		row.Get("clears").ForEach(func(_, window gjson.Result) bool {
			grant.Clears = append(grant.Clears, ClaudeResetWindow(window.String()))
			return true
		})
		row.Get("blocking").ForEach(func(_, window gjson.Result) bool {
			grant.Blocking = append(grant.Blocking, ClaudeResetWindow(window.String()))
			return true
		})
		if grant.Left < 0 {
			grant.Left = 0
		}
		status.Grants = append(status.Grants, grant)
		return true
	})
	return status
}

// ParseCodexResetStatus reads the body of GET /wham/rate-limit-reset-credits.
// Each credit is one use that clears the 5-hour and weekly windows.
func ParseCodexResetStatus(body []byte, now time.Time) *ResetStatus {
	root := gjson.ParseBytes(body)
	if !root.IsObject() || !root.Get("credits").IsArray() {
		return nil
	}
	status := &ResetStatus{ObservedAt: now}
	root.Get("credits").ForEach(func(_, row gjson.Result) bool {
		id := strings.TrimSpace(row.Get("id").String())
		if id == "" {
			return true
		}
		state := strings.ToLower(strings.TrimSpace(row.Get("status").String()))
		grant := ResetGrant{
			ID:        id,
			Label:     strings.TrimSpace(row.Get("title").String()),
			Total:     1,
			StartsAt:  parseResetTime(row.Get("granted_at").String()),
			EndsAt:    parseResetTime(row.Get("expires_at").String()),
			Clears:    []string{"primary", "secondary"},
			UsableNow: state == "available",
		}
		if grant.Label == "" {
			grant.Label = "Full reset"
		}
		if state == "available" {
			grant.Left = 1
		} else {
			grant.Unavailable = state
		}
		status.Grants = append(status.Grants, grant)
		return true
	})
	return status
}

// ObserveResets records an account's reset grants as a provider reported them.
func (s *RoutingState) ObserveResets(authID, provider string, status *ResetStatus) {
	if s == nil || status == nil || strings.TrimSpace(authID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[authID]
	if account == nil {
		account = &accountRouting{Meters: make(map[string]*Meter), History: make(map[string][]MeterSample)}
		s.accounts[authID] = account
	}
	if provider = strings.ToLower(strings.TrimSpace(provider)); provider != "" && account.Provider == "" {
		account.Provider = provider
	}
	copyStatus := *status
	copyStatus.Grants = append([]ResetGrant(nil), status.Grants...)
	account.Resets = &copyStatus
	s.dirty = true
}

// NeedsResetReading reports whether a probe should read an account's resets:
// it has no reading newer than maxAge and was not asked within maxAge.
func (s *RoutingState) NeedsResetReading(authID string, now time.Time, maxAge time.Duration) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[authID]
	if account == nil {
		return true
	}
	if now.Sub(account.resetProbeAt) < maxAge {
		return false
	}
	return account.Resets == nil || now.Sub(account.Resets.ObservedAt) >= maxAge
}

// MarkResetProbe records that an account's resets were asked for, whether or
// not an answer came, so an account that cannot be read is asked once per
// probe age and not every round.
func (s *RoutingState) MarkResetProbe(authID string, now time.Time) {
	if s == nil || strings.TrimSpace(authID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account := s.accounts[authID]
	if account == nil {
		account = &accountRouting{Meters: make(map[string]*Meter), History: make(map[string][]MeterSample)}
		s.accounts[authID] = account
	}
	account.resetProbeAt = now
}

// SetResetBackend registers the code that talks to the providers.
func (s *RoutingState) SetResetBackend(backend ResetBackend) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.resetBackend = backend
	s.mu.Unlock()
}

// SetResetMode records whether resets are used automatically.
func (s *RoutingState) SetResetMode(mode string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.resetMode = NormalizeResetMode(mode)
	s.mu.Unlock()
}

// ResetMode returns ResetModeOff or ResetModeAuto.
func (s *RoutingState) ResetMode() string {
	if s == nil {
		return ResetModeOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return NormalizeResetMode(s.resetMode)
}

// NormalizeResetMode maps anything but "auto" to "off".
func NormalizeResetMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), ResetModeAuto) {
		return ResetModeAuto
	}
	return ResetModeOff
}

// ErrResetBusy means another use of a reset is in flight for the account.
var ErrResetBusy = errors.New("a reset is already being used on this account")

// ErrResetUnknown means the account has no such reset in its last reading.
var ErrResetUnknown = errors.New("this account has no such reset")

// UseReset uses one reset on an account and records the outcome. by is "you"
// or "auto". The provider decides whether the reset goes through; this only
// refuses a grant the last reading does not list.
func (s *RoutingState) UseReset(ctx context.Context, auth *Auth, grantID, by string) (ResetEvent, error) {
	if s == nil || auth == nil {
		return ResetEvent{}, ErrResetUnknown
	}
	grantID = strings.TrimSpace(grantID)
	now := s.now()
	s.mu.Lock()
	backend := s.resetBackend
	account := s.accounts[auth.ID]
	var grant *ResetGrant
	if account != nil && account.Resets != nil {
		for i := range account.Resets.Grants {
			if account.Resets.Grants[i].ID == grantID {
				copyGrant := account.Resets.Grants[i]
				grant = &copyGrant
				break
			}
		}
	}
	if grant == nil || backend == nil {
		s.mu.Unlock()
		if backend == nil {
			return ResetEvent{}, errors.New("resets are not available on this server")
		}
		return ResetEvent{}, ErrResetUnknown
	}
	key := auth.ID + "\x00" + grantID
	if s.resetClaims == nil {
		s.resetClaims = make(map[string]*resetClaim)
	}
	claim := s.resetClaims[key]
	if claim != nil && claim.inFlight {
		s.mu.Unlock()
		return ResetEvent{}, ErrResetBusy
	}
	for other, pending := range s.resetClaims {
		if pending.inFlight && strings.HasPrefix(other, auth.ID+"\x00") {
			s.mu.Unlock()
			return ResetEvent{}, ErrResetBusy
		}
	}
	// A failed attempt keeps its request ID for a while, so trying again is
	// the same attempt to the provider and cannot use a second reset.
	if claim == nil || now.Sub(claim.at) > resetRetryWindow {
		claim = &resetClaim{requestID: uuid.NewString(), at: now}
		s.resetClaims[key] = claim
	}
	claim.inFlight = true
	requestID := claim.requestID
	s.mu.Unlock()

	result, errClaim := backend.ClaimReset(ctx, auth, *grant, requestID)

	event := ResetEvent{
		At: s.now(), AuthID: auth.ID, Provider: strings.ToLower(strings.TrimSpace(auth.Provider)),
		Account: authAccountLabel(auth), GrantID: grantID, Label: grant.Label, By: by,
	}
	s.mu.Lock()
	claim.inFlight = false
	if errClaim != nil {
		event.Result = "error"
		event.Text = "Could not reach the provider: " + truncateString(errClaim.Error(), 160)
	} else {
		delete(s.resetClaims, key)
		event.Result = result.Result
		event.Text = resetResultText(result.Result)
		if account = s.accounts[auth.ID]; account != nil && account.Resets != nil {
			applyResetResult(account, grantID, *grant, result, event.At)
		}
	}
	s.resetEvents = append(s.resetEvents, event)
	if extra := len(s.resetEvents) - maxResetEvents; extra > 0 {
		s.resetEvents = append([]ResetEvent(nil), s.resetEvents[extra:]...)
	}
	s.dirty = true
	s.mu.Unlock()

	if errClaim != nil {
		return event, errClaim
	}
	if result.Result == "reset" {
		backend.AfterReset(ctx, auth)
	}
	// Whatever the answer, read the account again so the dashboard and the
	// advice show what the provider now says.
	if errRefresh := backend.RefreshResets(ctx, auth); errRefresh != nil {
		log.WithField("auth", auth.ID).Debugf("usage resets: read after use: %v", errRefresh)
	}
	return event, nil
}

type resetClaim struct {
	requestID string
	at        time.Time
	inFlight  bool
}

// applyResetResult updates the stored grants and meters from a provider's
// answer, so the dashboard and routing reflect it before the next reading.
func applyResetResult(account *accountRouting, grantID string, grant ResetGrant, result ResetClaimResult, now time.Time) {
	for i := range account.Resets.Grants {
		stored := &account.Resets.Grants[i]
		if stored.ID != grantID {
			continue
		}
		switch {
		case result.Left != nil:
			stored.Left = max(0, *result.Left)
		case result.Result == "reset" && stored.Left > 0:
			stored.Left--
		case result.Result == "already_used" || result.Result == "no_credit":
			stored.Left = 0
		}
		if stored.Left == 0 {
			stored.UsableNow = false
		}
	}
	if result.Result != "reset" {
		return
	}
	cleared := result.Cleared
	if len(cleared) == 0 {
		cleared = grant.Clears
	}
	for _, name := range cleared {
		if meter := account.Meters[name]; meter != nil {
			meter.Utilization = 0
			meter.Status = "allowed"
			meter.ObservedAt = now
			account.History[name] = appendMeterSample(account.History[name], MeterSample{At: now, Utilization: 0})
		}
	}
}

func resetResultText(result string) string {
	switch result {
	case "reset":
		return "Reset used. The limit is cleared"
	case "already_used", "already_redeemed":
		return "This reset was already used"
	case "not_limited", "nothing_to_reset":
		return "Nothing to reset: the account is not at a limit this clears"
	case "cooldown":
		return "The provider is making this account wait before another reset"
	case "ineligible":
		return "This account cannot use resets"
	case "no_credit":
		return "The account has no reset left"
	default:
		return "The provider did not apply the reset"
	}
}

// ResetAdvice says what to do with one reset right now.
type ResetAdvice struct {
	// Action is "use", "hold", "wait" or "none".
	Action string `json:"action"`
	// Reason explains the action in one line.
	Reason string `json:"reason"`
	// Gain is the share of the limit a use gives back, from 0 to 1.
	Gain float64 `json:"gain,omitempty"`
	// EarlyHours is how long before its own reset the limit would clear.
	EarlyHours float64 `json:"early_hours,omitempty"`
	// Meter names the limit the advice is about.
	Meter string `json:"meter,omitempty"`
}

// ResetGrantView is one reset with its advice, as the dashboard draws it.
type ResetGrantView struct {
	ResetGrant
	// ClearsTitles names the cleared limits in plain words.
	ClearsTitles []string    `json:"clears_titles,omitempty"`
	Advice       ResetAdvice `json:"advice"`
}

// ResetSuggestion is one reset worth using now.
type ResetSuggestion struct {
	AuthID   string      `json:"auth_id"`
	Provider string      `json:"provider"`
	Account  string      `json:"account"`
	Grant    ResetGrant  `json:"grant"`
	Advice   ResetAdvice `json:"advice"`
	// ObservedAt is when the reset was last read from the provider.
	ObservedAt time.Time `json:"observed_at"`
}

// resetPool is what the accounts of one provider can still take.
type resetPool struct {
	// open counts accounts with room on every shared window.
	open int
	// roomHours is how long the open accounts last at the pool's recent burn.
	roomHours float64
	burnKnown bool
	// nextRelief is the earliest time a used-up shared window resets itself.
	nextRelief time.Time
	// familyOpen counts open accounts that also have room on a model family's
	// own week.
	familyOpen map[string]int
}

type resetAccount struct {
	auth    *Auth
	account *accountRouting
	// usable means the account can take work when it has allowance: on, not
	// refused by the provider, and with a recent reading.
	usable bool
	// refused means the provider is turning the account away outright, which
	// a reset does not change.
	refused bool
	// spent holds the meters that are used up, by name, as they stand now.
	spent map[string]Meter
	// meters holds every meter as it stands now.
	meters map[string]Meter
}

func isSharedMeter(name string) bool {
	switch name {
	case "5h", "7d", "primary", "secondary":
		return true
	}
	return false
}

// resetViews builds, under the lock, each account's resets with advice and
// the list of resets worth using now, best first.
func (s *RoutingState) resetViews(auths []*Auth, now time.Time) (map[string][]ResetGrantView, []ResetSuggestion) {
	byProvider := make(map[string][]*resetAccount)
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		entry := &resetAccount{auth: auth, account: s.accounts[auth.ID], spent: map[string]Meter{}, meters: map[string]Meter{}}
		if entry.account != nil {
			var newest time.Time
			for name, meter := range entry.account.Meters {
				if meter == nil {
					continue
				}
				effective, spent := meterSpent(*meter, now)
				entry.meters[name] = effective
				if spent {
					entry.spent[name] = effective
				}
				if meter.ObservedAt.After(newest) {
					newest = meter.ObservedAt
				}
			}
			_, entry.refused = entry.account.Health.refusedReason(now)
			entry.usable = !auth.Disabled && !entry.refused && !newest.IsZero() && now.Sub(newest) < staleReadingAge
		}
		byProvider[provider] = append(byProvider[provider], entry)
	}

	views := make(map[string][]ResetGrantView)
	var suggestions []ResetSuggestion
	for provider, accounts := range byProvider {
		pool := buildResetPool(accounts, now)
		var providerSuggestions []ResetSuggestion
		for _, entry := range accounts {
			if entry.account == nil || entry.account.Resets == nil {
				continue
			}
			status := entry.account.Resets
			fresh := now.Sub(status.ObservedAt) < resetReadingMaxAge
			grants := append([]ResetGrant(nil), status.Grants...)
			sort.SliceStable(grants, func(i, j int) bool { return resetGrantOrder(grants[i], grants[j]) })
			// taken marks limits an earlier, better-suited grant already
			// advises a use for.
			taken := make(map[string]bool)
			for _, grant := range grants {
				advice := adviseReset(provider, entry, grant, status, pool, fresh, now)
				if advice.Action == "use" {
					if taken[advice.Meter] {
						advice = ResetAdvice{Action: "hold", Reason: "Another reset on this account goes first", Meter: advice.Meter}
					} else {
						taken[advice.Meter] = true
						providerSuggestions = append(providerSuggestions, ResetSuggestion{
							AuthID: entry.auth.ID, Provider: provider, Account: authAccountLabel(entry.auth),
							Grant: grant, Advice: advice, ObservedAt: status.ObservedAt,
						})
					}
				}
				view := ResetGrantView{ResetGrant: grant, Advice: advice}
				for _, name := range grant.Clears {
					view.ClearsTitles = append(view.ClearsTitles, resetMeterTitle(provider, name))
				}
				views[entry.auth.ID] = append(views[entry.auth.ID], view)
			}
		}
		sort.SliceStable(providerSuggestions, func(i, j int) bool {
			left, right := providerSuggestions[i].Advice, providerSuggestions[j].Advice
			if scoreLeft, scoreRight := left.Gain*left.EarlyHours, right.Gain*right.EarlyHours; scoreLeft != scoreRight {
				return scoreLeft > scoreRight
			}
			return providerSuggestions[i].AuthID < providerSuggestions[j].AuthID
		})
		// One reset at a time for a shared limit: it changes what the pool can
		// take, so the next one is judged on the new state.
		sharedFirst := ""
		for _, suggestion := range providerSuggestions {
			if isSharedMeter(suggestion.Advice.Meter) && !resetIsExpiring(suggestion.Grant, now) {
				if sharedFirst != "" {
					demoteResetView(views[suggestion.AuthID], suggestion.Grant.ID, "The reset on "+sharedFirst+" goes first")
					continue
				}
				sharedFirst = suggestion.Account
			}
			suggestions = append(suggestions, suggestion)
		}
	}
	sort.SliceStable(suggestions, func(i, j int) bool {
		return suggestions[i].Advice.Gain*suggestions[i].Advice.EarlyHours > suggestions[j].Advice.Gain*suggestions[j].Advice.EarlyHours
	})
	return views, suggestions
}

func demoteResetView(views []ResetGrantView, grantID, reason string) {
	for i := range views {
		if views[i].ID == grantID {
			views[i].Advice.Action = "hold"
			views[i].Advice.Reason = reason
		}
	}
}

// resetGrantOrder puts the grant to use first ahead: the narrowest one, then
// the one that expires soonest.
func resetGrantOrder(left, right ResetGrant) bool {
	if (left.Left > 0) != (right.Left > 0) {
		return left.Left > 0
	}
	if len(left.Clears) != len(right.Clears) {
		return len(left.Clears) < len(right.Clears)
	}
	switch {
	case left.EndsAt.IsZero() != right.EndsAt.IsZero():
		return !left.EndsAt.IsZero()
	case !left.EndsAt.Equal(right.EndsAt):
		return left.EndsAt.Before(right.EndsAt)
	}
	return left.ID < right.ID
}

func resetIsExpiring(grant ResetGrant, now time.Time) bool {
	return !grant.EndsAt.IsZero() && grant.EndsAt.Sub(now) < resetExpirySoon
}

func buildResetPool(accounts []*resetAccount, now time.Time) resetPool {
	pool := resetPool{roomHours: math.Inf(1), familyOpen: make(map[string]int)}
	room, burn := 0.0, 0.0
	for _, entry := range accounts {
		if entry.account == nil || entry.auth.Disabled {
			continue
		}
		sharedSpent := false
		for name, meter := range entry.spent {
			if !isSharedMeter(name) {
				continue
			}
			sharedSpent = true
			if pool.nextRelief.IsZero() || meter.ResetAt.Before(pool.nextRelief) {
				pool.nextRelief = meter.ResetAt
			}
		}
		for name, meter := range entry.account.Meters {
			if meter == nil || !isSharedMeter(name) || !meterIsLong(meter) {
				continue
			}
			if rate, known := burnRate(entry.account.History[name], now, meterBurnLookback(meter)); known && rate > 0 {
				burn += rate
				pool.burnKnown = true
			}
		}
		if !entry.usable || sharedSpent {
			continue
		}
		pool.open++
		left := 1.0
		for name, meter := range entry.meters {
			if isSharedMeter(name) && meterIsLong(&meter) {
				left = math.Min(left, math.Max(0, 1-meter.Utilization))
			}
		}
		room += left
		for _, family := range claudeModelFamilies {
			if _, spent := familyAllowanceSpent(entry.account, family, now); !spent {
				pool.familyOpen[family]++
			}
		}
	}
	if burn > 0 {
		pool.roomHours = room / burn
	}
	return pool
}

// adviseReset decides what to do with one grant now.
//
// A reset is worth the most when the limit it clears is used up, that limit's
// own reset is far off, and the other accounts cannot carry the work until
// then. It is wasted when the limit is about to reset by itself or when other
// accounts have room. A grant close to expiring is used while it still can be.
func adviseReset(provider string, entry *resetAccount, grant ResetGrant, status *ResetStatus, pool resetPool, fresh bool, now time.Time) ResetAdvice {
	if grant.Left <= 0 {
		return ResetAdvice{Action: "none", Reason: "Used"}
	}
	if !grant.EndsAt.IsZero() && !grant.EndsAt.After(now) {
		return ResetAdvice{Action: "none", Reason: "Expired"}
	}
	if !fresh {
		return ResetAdvice{Action: "none", Reason: "No recent reading from the provider"}
	}
	expiry := ""
	if !grant.EndsAt.IsZero() && grant.EndsAt.Sub(now) < resetExpiryNote {
		expiry = " · expires " + whenText(grant.EndsAt, now)
	}
	if entry.auth.Disabled {
		return ResetAdvice{Action: "hold", Reason: "This account is off, so a reset would add no room" + expiry}
	}
	if entry.refused {
		return ResetAdvice{Action: "hold", Reason: "The provider is refusing this account, so a reset would add no room" + expiry}
	}
	if grant.Paused {
		return ResetAdvice{Action: "hold", Reason: "Paused by the provider" + expiry}
	}
	if grant.StartsAt.After(now) {
		return ResetAdvice{Action: "hold", Reason: "Starts " + whenText(grant.StartsAt, now)}
	}
	if status.CooldownUntil.After(now) {
		return ResetAdvice{Action: "wait", Reason: "The provider allows the next reset " + whenText(status.CooldownUntil, now)}
	}

	// The limit the advice is about. A used-up shared limit comes first: it
	// stops every model, so it decides what a reset is worth even when the
	// reset also clears a model's own week. Then a used-up model week. Among
	// used-up limits of one kind, the one whose own reset is furthest off;
	// with none used up, the fullest.
	var target Meter
	found, targetSpent, targetRank := false, false, 0
	for _, name := range grant.Clears {
		meter, ok := entry.meters[name]
		if !ok {
			continue
		}
		_, spent := entry.spent[name]
		rank := 0
		switch {
		case spent && isSharedMeter(name):
			rank = 2
		case spent:
			rank = 1
		}
		better := !found || rank > targetRank ||
			(rank == targetRank && spent && meter.ResetAt.After(target.ResetAt)) ||
			(rank == targetRank && !spent && meter.Utilization > target.Utilization)
		if better {
			target, found, targetSpent, targetRank = meter, true, spent, rank
		}
	}
	if !found {
		return ResetAdvice{Action: "hold", Reason: "No reading yet for the limit this clears" + expiry}
	}
	// The advice rests on how full the limit is, so an old reading of it is
	// not acted on, however fresh the list of resets is.
	if now.Sub(target.ObservedAt) >= resetReadingMaxAge {
		return ResetAdvice{Action: "hold", Meter: target.Name, Reason: "No recent reading of the " + resetLimitName(provider, target.Name) + expiry}
	}
	advice := ResetAdvice{Meter: target.Name, Gain: target.Utilization}
	if targetSpent {
		advice.Gain = 1
	}
	if target.ResetAt.After(now) {
		advice.EarlyHours = target.ResetAt.Sub(now).Hours()
	}
	title := resetLimitName(provider, target.Name)
	early := durationText(time.Duration(advice.EarlyHours * float64(time.Hour)))

	if !targetSpent {
		switch {
		case grant.RequiresLimit:
			advice.Action, advice.Reason = "hold", "Only works once this account is at its "+title+expiry
		case resetIsExpiring(grant, now) && advice.Gain >= minExpiringGain && grant.UsableNow && advice.EarlyHours >= minResetGain(target).Hours():
			advice.Action = "use"
			advice.Reason = fmt.Sprintf("Expires %s. Gives back %s of the %s, %s early", whenText(grant.EndsAt, now), percentText(advice.Gain), title, early)
		default:
			advice.Action, advice.Reason = "hold", "Keep for when this account hits its "+title+expiry
		}
		return advice
	}

	// The account is at a limit this grant clears.
	spentNames := make([]string, 0, len(entry.spent))
	for name := range entry.spent {
		spentNames = append(spentNames, name)
	}
	sort.Strings(spentNames)
	for _, name := range spentNames {
		if !isSharedMeter(name) || containsString(grant.Clears, name) || (grant.GivesBackWeekly && meterIsLong(ptrMeter(entry.spent[name]))) {
			continue
		}
		advice.Action = "hold"
		advice.Reason = "Would not help yet: the account is also at its " + resetLimitName(provider, name) + expiry
		return advice
	}
	if !grant.UsableNow {
		advice.Action, advice.Reason = "hold", resetUnavailableText(grant.Unavailable)+expiry
		return advice
	}
	if advice.EarlyHours < minResetGain(target).Hours() {
		advice.Action = "wait"
		advice.Reason = fmt.Sprintf("The %s resets by itself %s, too soon to spend a reset on", title, whenText(target.ResetAt, now))
		return advice
	}
	gives := fmt.Sprintf("Clears the %s %s early", title, early)
	if resetIsExpiring(grant, now) {
		advice.Action, advice.Reason = "use", "Expires "+whenText(grant.EndsAt, now)+". "+gives
		return advice
	}
	if !isSharedMeter(target.Name) {
		family := claudeClaimFamily(target.Name)
		others := pool.familyOpen[family]
		if others == 0 {
			advice.Action, advice.Reason = "use", "No account has "+capitalize(family)+" left. "+gives
		} else {
			advice.Action, advice.Reason = "hold", plural(others, "other account still has ", "other accounts still have ")+capitalize(family)+" left"
		}
		return advice
	}
	switch {
	case pool.open == 0:
		advice.Action, advice.Reason = "use", "Every account is at a limit. "+gives
	case pool.burnKnown && !pool.nextRelief.IsZero() && now.Add(time.Duration(pool.roomHours*float64(time.Hour))).Before(pool.nextRelief):
		advice.Action = "use"
		advice.Reason = fmt.Sprintf("The other accounts run out %s, before any limit resets. %s",
			whenText(now.Add(time.Duration(pool.roomHours*float64(time.Hour))), now), gives)
	default:
		advice.Action = "hold"
		advice.Reason = plural(pool.open, "other account has room", "other accounts have room")
		if !pool.nextRelief.IsZero() {
			advice.Reason += " until a limit resets " + whenText(pool.nextRelief, now)
		}
	}
	return advice
}

func ptrMeter(meter Meter) *Meter { return &meter }

func minResetGain(meter Meter) time.Duration {
	if meterIsLong(&meter) {
		return minLongResetGain
	}
	return minShortResetGain
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

// resetMeterTitle names a meter for advice: "week", "5-hour window",
// "Fable week".
func resetMeterTitle(provider, name string) string {
	return meterTitle(provider, &Meter{Name: name, Window: resetMeterWindow(name)})
}

// resetLimitName names a limit for advice: "5-hour limit", "weekly limit",
// "Fable weekly limit".
func resetLimitName(provider, name string) string {
	meter := &Meter{Name: name}
	base := "limit"
	switch {
	case meterIsLong(meter):
		base = "weekly limit"
	case meterIsShort(meter):
		base = "5-hour limit"
	}
	if strings.EqualFold(provider, "claude") {
		if family := claudeClaimFamily(name); family != "" {
			return capitalize(family) + " " + base
		}
	}
	return base
}

// whenText says a time for advice: "at 15:04" today, else "on Mon 15:04".
func whenText(t, now time.Time) string {
	text := formatRouteTime(t, now)
	if strings.Contains(text, " ") {
		return "on " + text
	}
	return "at " + text
}

func resetMeterWindow(name string) time.Duration {
	switch name {
	case "primary":
		return fiveHourWindow
	case "secondary":
		return weeklyQuotaWindow
	}
	return claudeClaimWindow(name)
}

func resetUnavailableText(reason string) string {
	switch reason {
	case "cooldown":
		return "The provider is making this account wait before another reset"
	case "blocked_five_hour":
		return "Cannot be used while the account is at its 5-hour limit"
	case "blocked_seven_day":
		return "Cannot be used while the account is at its weekly limit"
	case "blocked_opus", "blocked_sonnet", "blocked_other":
		return "Cannot be used while the account is at another limit"
	case "cannot_clear":
		return "Does not clear the limit the account is at"
	case "needs_limit":
		return "Only works once the account is at a limit it clears"
	case "redeeming":
		return "A use of this reset is in progress"
	default:
		return "The provider says this reset cannot be used right now"
	}
}

func durationText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		days := int(d.Hours() / 24)
		if hours := int(d.Hours()) - days*24; hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// ResetSuggestions returns the resets worth using now, best first.
func (s *RoutingState) ResetSuggestions(auths []*Auth, now time.Time) []ResetSuggestion {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, suggestions := s.resetViews(auths, now)
	return suggestions
}

// ResetEvents returns the recorded uses of resets, oldest first.
func (s *RoutingState) ResetEvents() []ResetEvent {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ResetEvent(nil), s.resetEvents...)
}

// AutoResetCandidate returns the reset automatic mode would use now, or nil.
// It is nil unless the mode is "auto", the advice on a reset is to use it, the
// provider says the use would go through, and the limits below allow it: one
// automatic use per provider per autoResetGap, and no second try at a reset
// the provider turned down within autoResetRetryGap.
func (s *RoutingState) AutoResetCandidate(auths []*Auth, now time.Time) *ResetSuggestion {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if NormalizeResetMode(s.resetMode) != ResetModeAuto {
		return nil
	}
	_, suggestions := s.resetViews(auths, now)
	for i := range suggestions {
		candidate := suggestions[i]
		if !candidate.Grant.UsableNow || s.autoResetHeldLocked(candidate, now) {
			continue
		}
		return &candidate
	}
	return nil
}

func (s *RoutingState) autoResetHeldLocked(candidate ResetSuggestion, now time.Time) bool {
	for _, event := range s.resetEvents {
		age := now.Sub(event.At)
		if event.By == "auto" && event.Provider == candidate.Provider && age < autoResetGap {
			return true
		}
		if event.AuthID == candidate.AuthID && event.GrantID == candidate.Grant.ID && event.Result != "reset" && age < autoResetRetryGap {
			return true
		}
	}
	return false
}

// RunAutoReset uses at most one reset when automatic mode is on and a reset
// is worth using. It reads the account from the provider again first and only
// goes ahead when the fresh reading gives the same advice. It returns the
// recorded attempt, or nil when nothing was tried.
func (s *RoutingState) RunAutoReset(ctx context.Context, auths []*Auth) *ResetEvent {
	first := s.AutoResetCandidate(auths, s.now())
	if first == nil {
		return nil
	}
	var auth *Auth
	for _, candidate := range auths {
		if candidate != nil && candidate.ID == first.AuthID {
			auth = candidate
			break
		}
	}
	s.mu.Lock()
	backend := s.resetBackend
	s.mu.Unlock()
	if auth == nil || backend == nil {
		return nil
	}
	if errRefresh := backend.RefreshResets(ctx, auth); errRefresh != nil {
		log.WithField("auth", auth.ID).Debugf("usage resets: read before automatic use: %v", errRefresh)
		return nil
	}
	again := s.AutoResetCandidate(auths, s.now())
	if again == nil || again.AuthID != first.AuthID || again.Grant.ID != first.Grant.ID {
		return nil
	}
	event, _ := s.UseReset(ctx, auth, again.Grant.ID, "auto")
	if event.At.IsZero() {
		return nil
	}
	return &event
}

// ParseCodexUsageMeters reads the 5-hour and weekly windows in the body of
// GET /wham/usage, so an idle Codex account has a current reading.
func ParseCodexUsageMeters(body []byte, now time.Time) map[string]*Meter {
	meters := make(map[string]*Meter)
	limits := gjson.GetBytes(body, "rate_limit")
	for name, key := range map[string]string{"primary": "primary_window", "secondary": "secondary_window"} {
		window := limits.Get(key)
		if !window.IsObject() || window.Get("used_percent").Type != gjson.Number {
			continue
		}
		meter := &Meter{
			Name:        name,
			Utilization: math.Max(0, math.Min(1, window.Get("used_percent").Float()/100)),
			Window:      time.Duration(window.Get("limit_window_seconds").Int()) * time.Second,
			ObservedAt:  now,
		}
		if resetAt := window.Get("reset_at").Int(); resetAt > 0 {
			meter.ResetAt = time.Unix(resetAt, 0)
		} else if after := window.Get("reset_after_seconds").Int(); after > 0 {
			meter.ResetAt = now.Add(time.Duration(after) * time.Second)
		}
		meters[name] = meter
	}
	return meters
}
