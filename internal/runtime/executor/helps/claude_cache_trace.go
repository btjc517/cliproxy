package helps

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// The "claude thread" and "claude cache" log lines let one day of traffic show
// whether thread_not_found follows idle gaps and what the replay costs. Both
// carry the request ID, the full Claude Code session ID and the agent ID so
// lines join per request and chain per session. They hold IDs, counts and
// status codes only, never message content or credentials.

// ClaudeThreadTrace describes one Claude request that carries a thread field.
type ClaudeThreadTrace struct {
	Session           string
	Agent             string
	ThreadType        string
	PreviousMessageID string
	PreviousAge       time.Duration
	PreviousAgeKnown  bool
}

// NewClaudeThreadTrace reads the thread fields of payload. caller is the key the
// thread tool-name store files responses under. It returns false when the
// request has no thread.
func NewClaudeThreadTrace(ctx context.Context, headers http.Header, payload []byte, caller string) (ClaudeThreadTrace, bool) {
	threadType := gjson.GetBytes(payload, "thread.type").String()
	if threadType == "" {
		return ClaudeThreadTrace{}, false
	}
	trace := ClaudeThreadTrace{
		Session:           ExtractClaudeCodeSessionID(ctx, payload, headers),
		Agent:             ExtractClaudeCodeAgentID(ctx, headers),
		ThreadType:        threadType,
		PreviousMessageID: gjson.GetBytes(payload, "thread.previous_message_id").String(),
	}
	trace.PreviousAge, trace.PreviousAgeKnown = ClaudeThreadPreviousAge(caller, trace.PreviousMessageID)
	return trace, true
}

var claudeLogTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// claudeLogToken keeps a value that is safe and short enough for one log field.
func claudeLogToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if !claudeLogTokenPattern.MatchString(value) {
		return "invalid"
	}
	return value
}

// Message formats the log line for the upstream status. errorBody is the error
// response, from which only the machine error code is kept.
func (t ClaudeThreadTrace) Message(status int, errorBody []byte) string {
	age := "unknown"
	if t.PreviousAgeKnown {
		age = fmt.Sprintf("%.1f", t.PreviousAge.Seconds())
	}
	errorCode := "-"
	if len(errorBody) > 0 && gjson.ValidBytes(errorBody) {
		code := gjson.GetBytes(errorBody, "error.details.error_code").String()
		if code == "" {
			code = gjson.GetBytes(errorBody, "error.type").String()
		}
		errorCode = claudeLogToken(code)
	}
	return fmt.Sprintf("claude thread: session=%s agent=%s type=%s previous_message_id=%s previous_age_s=%s status=%d error_code=%s",
		claudeLogToken(t.Session), claudeLogToken(t.Agent), claudeLogToken(t.ThreadType), claudeLogToken(t.PreviousMessageID), age, status, errorCode)
}

// Log writes the line for the upstream status.
func (t ClaudeThreadTrace) Log(ctx context.Context, status int, errorBody []byte) {
	LogWithRequestID(ctx).Info(t.Message(status, errorBody))
}

// claudeCacheUsageMessage formats per-request cache reads and writes. The 1h and
// 5m write split is zero when the response did not carry it.
func claudeCacheUsageMessage(session, agent, model string, failed bool, detail usage.Detail) string {
	return fmt.Sprintf("claude cache: session=%s agent=%s model=%s failed=%t input=%d output=%d cache_read=%d cache_write=%d cache_write_1h=%d cache_write_5m=%d",
		claudeLogToken(session), claudeLogToken(agent), claudeLogToken(model), failed,
		detail.InputTokens, detail.OutputTokens, detail.CacheReadTokens, detail.CacheCreationTokens,
		detail.CacheCreation1hTokens, detail.CacheCreation5mTokens)
}
