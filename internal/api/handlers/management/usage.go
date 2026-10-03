package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
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
		routing = gin.H{"strategy": strategy, "session_affinity": h.cfg.Routing.SessionAffinity}
	}
	c.JSON(http.StatusOK, gin.H{"routing": routing, "summary": usagestats.Default().Summary(limit)})
}

// dashboardAccountFields are the credential fields the read-only dashboard
// exposes. Tokens, id_token claims and cooldown internals are left out.
var dashboardAccountFields = []string{
	"id", "name", "provider", "label", "email", "status", "status_message",
	"disabled", "unavailable", "next_retry_after", "priority", "quota",
}

// GetDashboardData returns what the /dashboard page renders: routing settings,
// each credential's state and quota snapshot, and the usage summary. The
// caller is responsible for restricting who may reach it.
func (h *Handler) GetDashboardData(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	accounts := make([]gin.H, 0)
	if h.authManager != nil {
		for _, auth := range h.authManager.List() {
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
			accounts = append(accounts, account)
		}
	}
	routing := gin.H{}
	if h.cfg != nil {
		strategy, _ := normalizeRoutingStrategy(h.cfg.Routing.Strategy)
		routing = gin.H{"strategy": strategy, "session_affinity": h.cfg.Routing.SessionAffinity}
	}
	c.JSON(http.StatusOK, gin.H{"routing": routing, "accounts": accounts, "summary": usagestats.Default().Summary(100)})
}
