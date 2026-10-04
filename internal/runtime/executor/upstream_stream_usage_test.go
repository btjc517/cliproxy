package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// authUsagePlugin passes on the usage records of one credential.
type authUsagePlugin struct {
	authID  string
	records chan usage.Record
}

func (p *authUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if record.AuthID != p.authID {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

type noopUsagePlugin struct{}

func (noopUsagePlugin) HandleUsage(context.Context, usage.Record) {}

func captureAuthUsage(t *testing.T, authID string) *authUsagePlugin {
	t.Helper()
	plugin := &authUsagePlugin{authID: authID, records: make(chan usage.Record, 4)}
	name := "test-upstream-stream-" + authID
	usage.RegisterNamedPlugin(name, plugin)
	t.Cleanup(func() { usage.RegisterNamedPlugin(name, noopUsagePlugin{}) })
	return plugin
}

func (p *authUsagePlugin) next(t *testing.T) usage.Record {
	t.Helper()
	select {
	case record := <-p.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("no usage record published")
		return usage.Record{}
	}
}

const claudeUpstreamSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

const claudeUpstreamJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":20}}`

// A non-streaming client request that Claude Execute answers from an upstream
// stream is marked as streamed upstream, so its throughput counts. A native
// JSON reply is not, even when the context says the client streamed.
func TestClaudeExecuteReportsUpstreamStreaming(t *testing.T) {
	for _, tc := range []struct {
		name         string
		source       sdktranslator.Format
		clientStream bool
		payload      string
		want         bool
	}{
		{"translated reply streams upstream", sdktranslator.FormatOpenAIResponse, false,
			`{"model":"claude-opus-5","input":"hi","stream":false}`, true},
		{"native reply is buffered", sdktranslator.FromString("claude"), true,
			`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.want {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(claudeUpstreamSSE))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(claudeUpstreamJSON))
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{
				ID:         "claude-upstream-stream-" + map[bool]string{true: "sse", false: "json"}[tc.want],
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL},
			}
			plugin := captureAuthUsage(t, auth.ID)

			ctx := usage.WithStream(context.Background(), tc.clientStream)
			_, err := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
				Model:   "claude-opus-5",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{SourceFormat: tc.source})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			record := plugin.next(t)
			if record.UpstreamStream != tc.want || record.Stream != tc.clientStream {
				t.Fatalf("record upstream stream %v client stream %v, want %v and %v", record.UpstreamStream, record.Stream, tc.want, tc.clientStream)
			}
		})
	}
}

// Codex Execute always reads an upstream SSE stream. Its record is marked as
// streamed upstream with TTFT taken from the first token event.
func TestCodexExecuteReportsUpstreamStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}` + "\n\n" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":20,"total_tokens":21}}}` + "\n\n"))
	}))
	defer server.Close()
	auth := codexAPIKeyTestAuth(server.URL)
	auth.ID = "codex-upstream-stream"
	plugin := captureAuthUsage(t, auth.ID)

	ctx := usage.WithStream(context.Background(), false)
	_, err := NewCodexExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	record := plugin.next(t)
	if !record.UpstreamStream || record.Stream {
		t.Fatalf("record upstream stream %v client stream %v, want true and false", record.UpstreamStream, record.Stream)
	}
	if record.TTFT <= 0 || record.Latency < record.TTFT {
		t.Fatalf("ttft %s latency %s, want a first token time within the latency", record.TTFT, record.Latency)
	}
}
