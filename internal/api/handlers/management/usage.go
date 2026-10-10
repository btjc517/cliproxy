package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

// GetUsageSummary returns per-credential usage totals, hourly buckets and recent
// session-to-credential bindings collected by the usagestats store.
func (h *Handler) GetUsageSummary(c *gin.Context) {
	limit := 100
	if raw := strings.TrimSpace(c.Query("sessions")); raw != "" {
		parsed, errParse := strconv.Atoi(raw)
		if errParse != nil || parsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sessions must be a non-negative integer"})
			return
		}
		limit = parsed
	}
	routing := gin.H{}
	if h != nil && h.cfg != nil {
		strategy, _ := normalizeRoutingStrategy(h.cfg.Routing.Strategy)
		routing = gin.H{"strategy": strategy, "session_affinity": h.cfg.Routing.SessionAffinity, "prime_after_reset": h.cfg.Routing.PrimeAfterReset}
	}
	c.JSON(http.StatusOK, gin.H{"routing": routing, "summary": usagestats.Default().Summary(limit)})
}

// dashboardAccountFields are the credential fields the read-only dashboard
// exposes. Tokens, id_token claims and cooldown internals are left out.
var dashboardAccountFields = []string{
	"id", "name", "provider", "label", "email", "status", "status_message",
	"disabled", "unavailable", "next_retry_after", "priority", "quota",
}

// parsePerfWindow reads ?perf_start= and ?perf_end=. Both empty means no
// custom window and gives zero times. Otherwise both must be RFC3339 dates
// between 1970 and 2100, in order, 1 hour to 366 days apart.
func parsePerfWindow(rawStart, rawEnd string) (time.Time, time.Time, error) {
	if rawStart == "" && rawEnd == "" {
		return time.Time{}, time.Time{}, nil
	}
	if rawStart == "" || rawEnd == "" {
		return time.Time{}, time.Time{}, errors.New("perf_start and perf_end must be given together")
	}
	start, errStart := time.Parse(time.RFC3339, rawStart)
	end, errEnd := time.Parse(time.RFC3339, rawEnd)
	if errStart != nil || errEnd != nil {
		return time.Time{}, time.Time{}, errors.New("perf_start and perf_end must be RFC3339 dates such as 2026-10-08T14:00:00Z")
	}
	if start.Year() < 1970 || end.Year() > 2100 {
		return time.Time{}, time.Time{}, errors.New("perf_start and perf_end must fall between 1970 and 2100")
	}
	if errWindow := usagestats.ValidPerfWindow(start, end); errWindow != nil {
		return time.Time{}, time.Time{}, errors.New("perf_start must come before perf_end, 1 hour to 366 days apart")
	}
	return start, end, nil
}

// parsePadding reads an optional padding pair such as ?usage_pad_start= and
// ?usage_pad_end= around the window start..end. Both empty means none. Each
// given end must be an RFC3339 date on the outside of the window; the store
// trims a padding longer than one window on either side.
func parsePadding(name, rawStart, rawEnd string, start, end time.Time) (time.Time, time.Time, error) {
	if rawStart == "" && rawEnd == "" {
		return time.Time{}, time.Time{}, nil
	}
	if start.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("%s_pad_start and %s_pad_end need %s_start and %s_end", name, name, name, name)
	}
	var padStart, padEnd time.Time
	var errStart, errEnd error
	if rawStart != "" {
		padStart, errStart = time.Parse(time.RFC3339, rawStart)
	}
	if rawEnd != "" {
		padEnd, errEnd = time.Parse(time.RFC3339, rawEnd)
	}
	if errStart != nil || errEnd != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%s_pad_start and %s_pad_end must be RFC3339 dates such as 2026-10-08T14:00:00Z", name, name)
	}
	if (!padStart.IsZero() && padStart.After(start)) || (!padEnd.IsZero() && padEnd.Before(end)) {
		return time.Time{}, time.Time{}, fmt.Errorf("%s_pad_start must be at or before %s_start and %s_pad_end at or after %s_end", name, name, name, name)
	}
	return padStart, padEnd, nil
}

// GetDashboardData returns what the /dashboard page renders: this server,
// routing settings, each credential's state, quota snapshot and plan, and the
// usage summary with performance and per-credential usage over the range
// named by ?range= (24h, 7d, 14d, 30d, 180d or all). A missing or unknown
// range falls back to 24h, which summary.range reports. ?scope= takes comma
// separated credential ids and adds a "selection" performance scope that
// merges them. ?perf_start= and ?perf_end= (RFC3339, see parsePerfWindow)
// replace summary.performance with that window, Range "custom", and
// ?usage_start= with ?usage_end= replace summary.usage_range. Each window may
// carry a padding (?perf_pad_start=, ?perf_pad_end=, ?usage_pad_start=,
// ?usage_pad_end=, see parsePadding): its series then reach over the padding
// in the buckets the window alone gets, while totals stay the window's. The
// caller is responsible for restricting who may reach it.
func (h *Handler) GetDashboardData(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	window := usagestats.ResolveWindow(strings.TrimSpace(c.Query("range")))
	selection := usagestats.ParseSelection(c.Query("scope"))
	var usageStart, usageEnd time.Time
	if c.Query("usage_start") != "" || c.Query("usage_end") != "" {
		var errStart, errEnd error
		usageStart, errStart = time.Parse(time.RFC3339, c.Query("usage_start"))
		usageEnd, errEnd = time.Parse(time.RFC3339, c.Query("usage_end"))
		if errStart != nil || errEnd != nil || !usageEnd.After(usageStart) || usageEnd.Sub(usageStart) > 366*24*time.Hour || usageStart.Year() < 1970 || usageEnd.Year() > 2100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "usage window must be valid RFC3339 dates, ordered and at most 366 days"})
			return
		}
	}
	perfStart, perfEnd, errPerf := parsePerfWindow(c.Query("perf_start"), c.Query("perf_end"))
	if errPerf != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errPerf.Error()})
		return
	}
	usagePadStart, usagePadEnd, errUsagePad := parsePadding("usage", c.Query("usage_pad_start"), c.Query("usage_pad_end"), usageStart, usageEnd)
	if errUsagePad != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errUsagePad.Error()})
		return
	}
	perfPadStart, perfPadEnd, errPerfPad := parsePadding("perf", c.Query("perf_pad_start"), c.Query("perf_pad_end"), perfStart, perfEnd)
	if errPerfPad != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errPerfPad.Error()})
		return
	}

	now := time.Now()
	accounts := make([]gin.H, 0)
	var auths []*coreauth.Auth
	if h.authManager != nil {
		auths = h.authManager.List()
		for _, auth := range auths {
			entry := h.buildAuthFileEntry(auth)
			if entry == nil {
				continue
			}
			account := gin.H{}
			for _, field := range dashboardAccountFields {
				if value, ok := entry[field]; ok {
					account[field] = value
				}
			}
			account["mode"] = dashboardAccountMode(entry)
			account["plan"] = dashboardAccountPlan(auth, now.Location())
			accounts = append(accounts, account)
		}
	}
	routing := gin.H{}
	if h.cfg != nil {
		strategy, _ := normalizeRoutingStrategy(h.cfg.Routing.Strategy)
		routing = gin.H{
			"strategy":          strategy,
			"session_affinity":  h.cfg.Routing.SessionAffinity,
			"prime_after_reset": h.cfg.Routing.PrimeAfterReset,
			"strategies":        routingStrategies,
		}
	}
	router := coreauth.DefaultRoutingState().Dashboard(auths, now)
	authIDs := make([]string, 0, len(auths))
	for _, auth := range auths {
		if auth != nil {
			authIDs = append(authIDs, auth.ID)
		}
	}
	summary := usagestats.Default().SummaryForSelection(100, window, selection, authIDs)
	if !perfStart.IsZero() {
		summary.Performance = usagestats.Default().PerformanceBetweenPadded(perfStart, perfEnd, perfPadStart, perfPadEnd, selection, authIDs)
	}
	if !usageStart.IsZero() {
		custom := usagestats.Default().UsageBetween(usageStart, usageEnd)
		if !usagePadStart.IsZero() || !usagePadEnd.IsZero() {
			custom = usagestats.Default().UsageBetweenPadded(usageStart, usageEnd, usagePadStart, usagePadEnd)
		}
		custom.Ranges = summary.UsageRange.Ranges
		summary.UsageRange = custom
	}
	c.JSON(http.StatusOK, gin.H{
		"server":    h.dashboardServer(c, now),
		"routing":   routing,
		"accounts":  accounts,
		"router":    router,
		"allowance": coreauth.DefaultRoutingState().MeterHistory(authIDs, now),
		"summary":   summary,
	})
}
