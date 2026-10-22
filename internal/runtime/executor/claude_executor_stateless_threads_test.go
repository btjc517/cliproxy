package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const statelessThreadContinue = `{"model":"claude-opus-5-5","max_tokens":16,"thread":{"type":"continue","previous_message_id":"msg_prev"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`

func newStatelessThreadServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"upstream reached"}}`))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func statelessThreadAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         "claude-auth-stateless",
		Provider:   "claude",
		Attributes: map[string]string{"api_key": "test-key", "base_url": baseURL},
	}
}

// With stateless threads on, a thread request is refused before any upstream
// call, with the error Claude Code reads as "threads unsupported", so it
// resends the turn as a full conversation.
func TestClaudeStatelessThreadsRefuseThreadRequests(t *testing.T) {
	for _, stream := range []bool{false, true} {
		server, calls := newStatelessThreadServer(t)
		executor := NewClaudeExecutor(&config.Config{Claude: config.ClaudeConfig{StatelessThreads: true}})
		req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(statelessThreadContinue)}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}

		var err error
		if stream {
			_, err = executor.ExecuteStream(context.Background(), statelessThreadAuth(server.URL), req, opts)
		} else {
			_, err = executor.Execute(context.Background(), statelessThreadAuth(server.URL), req, opts)
		}
		if err == nil {
			t.Fatalf("stream=%v: expected a refusal, got nil", stream)
		}
		if got := atomic.LoadInt32(calls); got != 0 {
			t.Fatalf("stream=%v: upstream was called %d times, want 0", stream, got)
		}
		var status interface{ StatusCode() int }
		if !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
			t.Fatalf("stream=%v: error %v, want status 400", stream, err)
		}
		if code := gjson.Get(err.Error(), "error.details.error_code").String(); code != "thread_unsupported_request" {
			t.Fatalf("stream=%v: error_code %q, want thread_unsupported_request; body %s", stream, code, err.Error())
		}
		var scoped interface{ IsRequestScoped() bool }
		if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
			t.Fatalf("stream=%v: refusal must be request scoped so no other account is tried", stream)
		}
	}
}

// Off by default, and requests without "thread" always pass through.
func TestClaudeStatelessThreadsLeaveOtherRequestsAlone(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		payload string
	}{
		{"setting off", &config.Config{}, statelessThreadContinue},
		{"no thread field", &config.Config{Claude: config.ClaudeConfig{StatelessThreads: true}},
			`{"model":"claude-opus-5-5","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
	}
	for _, tc := range cases {
		server, calls := newStatelessThreadServer(t)
		executor := NewClaudeExecutor(tc.cfg)
		_, _ = executor.Execute(context.Background(), statelessThreadAuth(server.URL),
			cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(tc.payload)},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		if got := atomic.LoadInt32(calls); got == 0 {
			t.Fatalf("%s: upstream was not called", tc.name)
		}
	}
}
