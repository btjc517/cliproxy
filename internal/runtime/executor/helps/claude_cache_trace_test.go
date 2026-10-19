package helps

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestClaudeThreadToolNameCacheAge(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cache := newClaudeThreadToolNameCache(8, 24*time.Hour, func() time.Time { return now })
	cache.store("caller", "msg_1", map[string]string{})

	now = now.Add(9*time.Minute + 30*time.Second)
	age, ok := cache.age("caller", "msg_1")
	if !ok || age != 9*time.Minute+30*time.Second {
		t.Fatalf("age = %v, %v; want 9m30s, true", age, ok)
	}
	if _, ok := cache.age("other-caller", "msg_1"); ok {
		t.Fatal("age found an entry stored for another caller")
	}
	if _, ok := cache.age("caller", ""); ok {
		t.Fatal("age found an entry for an empty message id")
	}
}

func TestClaudeThreadTraceMessage(t *testing.T) {
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "6f1c2d3e-aaaa-bbbb-cccc-1234567890ab")
	headers.Set(ClaudeCodeAgentHeader, "agent-7")
	payload := []byte(`{"thread":{"type":"continue","previous_message_id":"msg_01ABC"},"messages":[{"role":"user","content":"secret words"}]}`)

	trace, ok := NewClaudeThreadTrace(context.Background(), headers, payload, "")
	if !ok {
		t.Fatal("NewClaudeThreadTrace returned false for a thread request")
	}
	trace.PreviousAge, trace.PreviousAgeKnown = 332*time.Second, true

	got := trace.Message(http.StatusNotFound, []byte(`{"type":"error","error":{"type":"not_found_error","message":"thread gone","details":{"error_code":"thread_not_found"}}}`))
	want := "claude thread: session=6f1c2d3e-aaaa-bbbb-cccc-1234567890ab agent=agent-7 type=continue previous_message_id=msg_01ABC previous_age_s=332.0 status=404 error_code=thread_not_found"
	if got != want {
		t.Fatalf("message\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "thread gone") {
		t.Fatalf("message leaked content: %s", got)
	}
}

func TestClaudeThreadTraceUnknownAgeAndMainAgent(t *testing.T) {
	payload := []byte(`{"thread":{"type":"create"}}`)
	trace, ok := NewClaudeThreadTrace(context.Background(), nil, payload, "caller")
	if !ok {
		t.Fatal("NewClaudeThreadTrace returned false for a thread request")
	}
	got := trace.Message(http.StatusOK, nil)
	want := "claude thread: session=- agent=main type=create previous_message_id=- previous_age_s=unknown status=200 error_code=-"
	if got != want {
		t.Fatalf("message\n got: %s\nwant: %s", got, want)
	}
}

func TestNewClaudeThreadTraceSkipsRequestsWithoutThread(t *testing.T) {
	if _, ok := NewClaudeThreadTrace(context.Background(), nil, []byte(`{"messages":[]}`), "caller"); ok {
		t.Fatal("NewClaudeThreadTrace returned true without a thread field")
	}
}

func TestClaudeLogTokenRejectsUnsafeValues(t *testing.T) {
	if got := claudeLogToken("a b=c"); got != "invalid" {
		t.Fatalf("claudeLogToken = %q, want invalid", got)
	}
	if got := claudeLogToken(strings.Repeat("a", 129)); got != "invalid" {
		t.Fatalf("claudeLogToken accepted an over-long value")
	}
}

func TestParseClaudeUsageCacheWriteSplit(t *testing.T) {
	detail := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":3,"output_tokens":10,"cache_read_input_tokens":200000,"cache_creation_input_tokens":7000,"cache_creation":{"ephemeral_1h_input_tokens":5000,"ephemeral_5m_input_tokens":2000}}}`))
	if detail.CacheCreation1hTokens != 5000 || detail.CacheCreation5mTokens != 2000 || detail.CacheCreationTokens != 7000 {
		t.Fatalf("split = 1h %d, 5m %d, total %d", detail.CacheCreation1hTokens, detail.CacheCreation5mTokens, detail.CacheCreationTokens)
	}
}

func TestClaudeStreamUsageKeepsCacheWriteSplit(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":40}}}}`))
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"output_tokens":12,"cache_read_input_tokens":100,"cache_creation_input_tokens":40}}`))
	detail, ok := buffer.Detail()
	if !ok || detail.CacheCreation5mTokens != 40 || detail.CacheCreationTokens != 40 || detail.OutputTokens != 12 {
		t.Fatalf("detail = %+v, ok %v", detail, ok)
	}
}

func TestClaudeCacheUsageMessage(t *testing.T) {
	got := claudeCacheUsageMessage("sess-1", "main", "claude-opus-5-5", false, usage.Detail{
		InputTokens: 3, OutputTokens: 10, CacheReadTokens: 200000, CacheCreationTokens: 7000,
		CacheCreation1hTokens: 5000, CacheCreation5mTokens: 2000,
	})
	want := "claude cache: session=sess-1 agent=main model=claude-opus-5-5 failed=false input=3 output=10 cache_read=200000 cache_write=7000 cache_write_1h=5000 cache_write_5m=2000"
	if got != want {
		t.Fatalf("message\n got: %s\nwant: %s", got, want)
	}
}
