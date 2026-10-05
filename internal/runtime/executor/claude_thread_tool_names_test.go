package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func TestClaudeThreadToolNamesContinueWithoutDefinitions(t *testing.T) {
	options := claudeMCPAliasOptions{secret: t.Name()}
	initial := []byte(`{"thread":{"type":"create"},"tools":[{"name":"Bash"},{"name":"Read"}]}`)
	first, firstMap := prepareClaudeOAuthToolNamesForUpstream(initial, options)
	alias := gjson.GetBytes(first, "tools.0.name").String()
	if alias == "Bash" {
		t.Fatal("fixture was not aliased")
	}
	helps.StoreClaudeThreadToolNames(options.secret, "msg_thread_initial", firstMap)
	next := []byte(`{"thread":{"type":"continue","previous_message_id":"msg_thread_initial"},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`)
	prepared, restoredMap, err := prepareClaudeOAuthThreadToolNamesForUpstream(next, next, options)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(prepared, "tools").Exists() {
		t.Fatal("continuation gained tool declarations")
	}
	line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":%q}}`, alias))
	restored, err := restoreClaudeOAuthToolNamesFromStreamLine(line, restoredMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(restored[6:], "content_block.name").String(); got != "Bash" {
		t.Fatalf("continuation tool name = %q, want Bash", got)
	}
}

func TestClaudeThreadToolNamesMissingAndExplicitDefinitions(t *testing.T) {
	options := claudeMCPAliasOptions{secret: t.Name()}
	for _, extra := range []string{"", `,"tools":[]`, `,"tools":[{"name":"Read"}]`} {
		payload := []byte(`{"thread":{"type":"continue","previous_message_id":"msg_unknown"}` + extra + `}`)
		_, names, err := prepareClaudeOAuthThreadToolNamesForUpstream(payload, payload, options)
		if extra == "" {
			var scoped claudeMCPAliasRestoreError
			var status interface{ StatusCode() int }
			if !errors.As(err, &scoped) || !scoped.IsRequestScoped() || !errors.As(err, &status) || status.StatusCode() != 400 {
				t.Fatalf("missing state error = %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		} else if extra == `,"tools":[]` && len(names) != 0 {
			t.Fatalf("empty tools inherited %v", names)
		}
	}
}

func TestClaudeThreadToolNamesPreservesReferencesAndCallerIsolation(t *testing.T) {
	options := claudeMCPAliasOptions{secret: t.Name()}
	first, mapOne := prepareClaudeOAuthToolNamesForUpstream([]byte(`{"tools":[{"name":"Read"},{"name":"mcp__docs__lookup"}]}`), options)
	alias := gjson.GetBytes(first, "tools.0.name").String()
	helps.StoreClaudeThreadToolNames(options.secret, "msg_refs", mapOne)
	payload := []byte(`{"thread":{"type":"continue","previous_message_id":"msg_refs"},"tool_choice":{"type":"tool","name":"Read"},"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"tool_reference","tool_name":"Read"},{"type":"tool_reference","tool_name":"mcp__docs__lookup"}]}]}]}`)
	prepared, names, err := prepareClaudeOAuthThreadToolNamesForUpstream(payload, payload, options)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(prepared, "tool_choice.name").String() != alias || gjson.GetBytes(prepared, "messages.0.content.0.content.0.tool_name").String() != alias {
		t.Fatalf("references not remapped: %s", prepared)
	}
	if names["mcp__docs__lookup"] != "mcp__docs__lookup" {
		t.Fatal("caller MCP name not preserved")
	}
	if _, _, err = prepareClaudeOAuthThreadToolNamesForUpstream(payload, payload, claudeMCPAliasOptions{secret: "other"}); err == nil {
		t.Fatal("caller inherited another caller's tools")
	}
	options.inheritedReverseMap = mapOne
	batched, bmap, ok := remapOAuthToolNamesWithBatchedEdits(payload, options)
	legacy, lmap := remapOAuthToolNamesWithOptionsLegacy(payload, options)
	if !ok || !bytes.Equal(batched, legacy) || fmt.Sprint(bmap) != fmt.Sprint(lmap) {
		t.Fatal("batched and legacy thread remapping differ")
	}
}

func TestClaudeExecutorThreadToolNamesRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%v", stream), func(t *testing.T) {
			var alias string
			calls := 0
			messagePrefix := strings.ReplaceAll(t.Name(), "/", "_")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				calls++
				if calls == 1 {
					alias = gjson.GetBytes(body, "tools.0.name").String()
				} else if gjson.GetBytes(body, "tools").Exists() {
					t.Error("continuation gained tools")
				}
				if alias == "" || alias == "Bash" {
					t.Error("fixture alias missing")
				}
				id := fmt.Sprintf("msg_%s_%d", messagePrefix, calls)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":1}}}\n\n", id)
					fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":%q,\"input\":{}}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", alias)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"id":%q,"type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, id, alias)
				}
			}))
			defer server.Close()
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "sk-ant-oat-thread-fixture", "base_url": server.URL}, Metadata: claudeOAuthTestMetadata()}
			for i := 0; i < 5; i++ {
				thread := `{"type":"create"}`
				declarations := `,"tools":[{"name":"Bash","input_schema":{"type":"object"}}]`
				if i > 0 {
					thread = fmt.Sprintf(`{"type":"continue","previous_message_id":"msg_%s_%d"}`, messagePrefix, i)
					declarations = ""
				}
				payload := []byte(fmt.Sprintf(`{"model":"claude-opus-5","thread":%s,"messages":[{"role":"user","content":"next"}]%s,"stream":%v}`, thread, declarations, stream))
				req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				var output []byte
				if stream {
					response, err := executor.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range response.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
				} else {
					response, err := executor.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = response.Payload
				}
				if !bytes.Contains(output, []byte(`"name":"Bash"`)) || bytes.Contains(output, []byte(alias)) {
					t.Fatalf("turn %d leaked tool alias: %s", i+1, output)
				}
			}
			if calls != 5 {
				t.Fatalf("upstream calls = %d", calls)
			}
		})
	}
}
