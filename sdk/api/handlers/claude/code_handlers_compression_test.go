package claude

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type compressionCaptureExecutor struct{ payload []byte }

func (e *compressionCaptureExecutor) Identifier() string { return "compression-test" }
func (e *compressionCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.payload = bytes.Clone(req.Payload)
	return coreexecutor.Response{Payload: []byte(`{"content":[],"stop_reason":"end_turn"}`)}, nil
}
func (e *compressionCaptureExecutor) CountTokens(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.payload = bytes.Clone(req.Payload)
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":123}`)}, nil
}
func (e *compressionCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.payload = bytes.Clone(req.Payload)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}
func (e *compressionCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (e *compressionCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestClaudeZstdRequestsPreservePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "stream", "count_tokens"} {
		t.Run(endpoint, func(t *testing.T) {
			executor := &compressionCaptureExecutor{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{ID: "claude-compression-" + endpoint, Provider: executor.Identifier(), Status: coreauth.StatusActive}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "compression-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			h := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			url := "/v1/messages"
			router.POST(url, h.ClaudeMessages)
			if endpoint == "count_tokens" {
				url += "/count_tokens"
				router.POST(url, h.ClaudeCountTokens)
			}
			body := []byte(`{"model":"compression-model","messages":[{"role":"user","content":"Keep this exact text and spacing."}]}`)
			if endpoint == "stream" {
				body = append(body[:len(body)-1], []byte(`,"stream":true}`)...)
			}
			encoder, err := zstd.NewWriter(nil)
			if err != nil {
				t.Fatal(err)
			}
			defer encoder.Close()
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(encoder.EncodeAll(body, nil)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "zstd")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
			if !bytes.Equal(executor.payload, body) {
				t.Fatal("executor did not receive the exact uncompressed request")
			}
		})
	}
}

func TestClaudeRejectsCorruptZstdRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	for _, handler := range []gin.HandlerFunc{h.ClaudeMessages, h.ClaudeCountTokens} {
		resp := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(resp)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte("broken compressed request")))
		ctx.Request.Header.Set("Content-Encoding", "zstd")
		handler(ctx)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want 400", resp.Code)
		}
	}
}
