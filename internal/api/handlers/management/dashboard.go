package management

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnetname"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// routingStrategies are the canonical values normalizeRoutingStrategy returns.
var routingStrategies = []string{"soonest-reset", "fill-first", "round-robin", "weighted-round-robin"}

// planDateLayout is the format of the manual plan_renews_on and plan_ends_on
// auth file fields.
const planDateLayout = "2006-01-02"

// planDateFields are the auth file fields that hold a manual plan date.
var planDateFields = map[string]struct{}{"plan_renews_on": {}, "plan_ends_on": {}}

// dashboardServer describes this proxy for the dashboard header.
func (h *Handler) dashboardServer(c *gin.Context, now time.Time) gin.H {
	address := ""
	if name := tailnetname.SelfName(); name != "" {
		address = name
		if h.cfg != nil && h.cfg.Port > 0 {
			address = net.JoinHostPort(name, strconv.Itoa(h.cfg.Port))
		}
	} else if c != nil && c.Request != nil {
		address = c.Request.Host
	}
	version := strings.TrimSpace(buildinfo.Commit)
	if version == "none" || version == "unknown" {
		version = ""
	}
	if len(version) > 8 {
		version = version[:8]
	}
	return gin.H{
		"host":    tailnetname.LocalHostName(),
		"address": address,
		"version": version,
		"now":     now,
	}
}

// dashboardAccountMode is how the router treats a credential: "off" when
// disabled, "reserve" when its priority is below zero, else "rotation".
func dashboardAccountMode(entry gin.H) string {
	if disabled, _ := entry["disabled"].(bool); disabled {
		return "off"
	}
	if priority, ok := numberValue(entry["priority"]); ok && priority < 0 {
		return "reserve"
	}
	return "rotation"
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		parsed, errParse := typed.Float64()
		return parsed, errParse == nil
	case string:
		parsed, errParse := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, errParse == nil
	default:
		return 0, false
	}
}

// dashboardAccountPlan returns the credential's plan name, its renewal and end
// dates and the time of its next bill. Manual dates from the auth file win
// over what the provider reports: the Codex id_token's paid-until time, or the
// start of a Claude subscription in facts. Both bill each month at the same
// day and time, so a time that has passed is carried forward to the next one.
// A manual date has no time of day, so it gives no next_bill_at.
func dashboardAccountPlan(auth *coreauth.Auth, facts *coreauth.PlanFacts, now time.Time) gin.H {
	plan := gin.H{"type": "", "renews_on": "", "ends_on": "", "source": "", "next_bill_at": ""}
	if auth == nil {
		return plan
	}
	location := now.Location()
	claims := extractCodexIDTokenClaims(auth)
	if planType, _ := claims["plan_type"].(string); planType != "" {
		plan["type"] = planType
	} else if planType := metadataString(auth, "plan_type"); planType != "" {
		plan["type"] = planType
	}
	plan["ends_on"] = metadataPlanDate(auth, "plan_ends_on")
	free := strings.EqualFold(fmt.Sprint(plan["type"]), "free")
	paidUntil, paidUntilDate := claimTime(claims["chatgpt_subscription_active_until"])
	var anchor time.Time
	switch {
	case metadataPlanDate(auth, "plan_renews_on") != "":
		plan["renews_on"] = metadataPlanDate(auth, "plan_renews_on")
		plan["source"] = "manual"
	case paidUntilDate != "":
		plan["renews_on"] = paidUntilDate
		plan["source"] = "token"
	case !paidUntil.IsZero() && !free:
		anchor = paidUntil
		plan["source"] = "token"
	case facts != nil && facts.Active() && strings.EqualFold(auth.Provider, "claude"):
		anchor = facts.StartedAt
		plan["source"] = "plan_start"
	}
	if next := coreauth.NextMonthlyBill(anchor, now); !next.IsZero() {
		plan["renews_on"] = next.In(location).Format(planDateLayout)
		// A plan that ends does not bill again.
		if plan["ends_on"] == "" {
			plan["next_bill_at"] = next.UTC().Format(time.RFC3339)
		}
	}
	return plan
}

func metadataString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

// metadataPlanDate returns a manual plan date when it is a valid YYYY-MM-DD.
func metadataPlanDate(auth *coreauth.Auth, key string) string {
	value := metadataString(auth, key)
	if _, errParse := parsePlanDate(value); errParse != nil {
		return ""
	}
	return value
}

func parsePlanDate(value string) (time.Time, error) {
	if len(value) != len(planDateLayout) {
		return time.Time{}, fmt.Errorf("not a YYYY-MM-DD date")
	}
	return time.Parse(planDateLayout, value)
}

// validatePlanDateValue checks a PATCH value for a plan date field: a
// YYYY-MM-DD string, or nil to clear it.
func validatePlanDateValue(field string, value any) error {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if ok {
		if _, errParse := parsePlanDate(text); errParse == nil {
			return nil
		}
	}
	return fmt.Errorf("%s must be a YYYY-MM-DD date or null", field)
}

// claimTime reads the chatgpt_subscription_active_until claim: a timestamp
// string or Unix seconds gives the time, and a bare YYYY-MM-DD gives the date
// alone, which has no time of day to bill at.
func claimTime(value any) (at time.Time, dateOnly string) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if parsed, errParse := time.Parse(time.RFC3339, text); errParse == nil {
			return parsed, ""
		}
		if parsed, errDate := parsePlanDate(text); errDate == nil {
			return time.Time{}, parsed.Format(planDateLayout)
		}
		if seconds, errNumber := strconv.ParseFloat(text, 64); errNumber == nil {
			return unixTime(seconds), ""
		}
	default:
		if seconds, ok := numberValue(typed); ok {
			return unixTime(seconds), ""
		}
	}
	return time.Time{}, ""
}

func unixTime(seconds float64) time.Time {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}
	}
	if seconds > 1e12 {
		// Milliseconds.
		seconds /= 1000
	}
	return time.Unix(int64(seconds), 0)
}
