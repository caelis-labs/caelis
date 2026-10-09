package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

// Exercise the selector through the actual Anthropic SDK stream decoder. The
// large configured output limit used to fail before an HTTP request when the
// selector sent a non-streaming request.
func TestToolSearchDeepSeekAnthropicStreamAtLargeOutputLimit(t *testing.T) {
	requests := 0
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Errorf("request path = %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Stream    bool  `json:"stream"`
			MaxTokens int64 `json:"max_tokens"`
			Messages  any   `json:"messages"`
			Tools     []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !body.Stream || body.MaxTokens != 256000 || len(body.Tools) != 1 || body.Tools[0].Name != "InspectToolSchema" {
			t.Errorf("request %d: stream=%t max_tokens=%d tools=%v", requests, body.Stream, body.MaxTokens, body.Tools)
		}
		messages, _ := json.Marshal(body.Messages)
		if strings.Contains(string(messages), "syntheticSecretProperty") != (requests > 1) {
			t.Errorf("request %d schema visibility is wrong", requests)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"deepseek-v4-flash\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n", requests)
		if requests == 1 {
			_, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"inspect_1\",\"name\":\"InspectToolSchema\",\"input\":{}}}\n\n")
			for _, fragment := range []string{`{"name":"`, `docs__lookup"}`} {
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", fragment)
			}
			_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			for _, fragment := range []string{`{"tools":["`, `docs__lookup"]}`} {
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", fragment)
			}
			_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		}
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	llm := newDeepSeek(Config{Provider: "deepseek", Model: "deepseek-v4-flash", BaseURL: server.URL + "/anthropic", HTTPClient: server.Client(), MaxOutputTok: 256000, Auth: AuthConfig{Type: AuthAPIKey, Token: "synthetic"}}, "synthetic")
	definition := tool.Definition{Name: "docs__lookup", Description: "Find a synthetic document", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"syntheticSecretProperty": map[string]any{"type": "string"}}}}
	reads := 0
	var receipts []model.Invocation
	ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
	names, err := toolsearch.NewAgentRanker().Rank(ctx, "find a synthetic document", []tool.Definition{definition}, 8, toolsearch.SearchModel{
		Model: llm,
		ReadSchema: func(_ context.Context, name string) (tool.Definition, error) {
			reads++
			if name != definition.Name {
				return tool.Definition{}, fmt.Errorf("unexpected schema name %q", name)
			}
			return definition, nil
		},
	})
	if err != nil || requests != 2 || reads != 1 || len(names) != 1 || names[0] != definition.Name {
		t.Fatalf("selection=%v error=%v HTTP_requests=%d schema_reads=%d", names, err, requests, reads)
	}
	if len(receipts) != 2 || receipts[0].Usage.TotalTokens != 18 || receipts[1].Usage.TotalTokens != 16 || receipts[0].Outcome != "completed" || receipts[1].Outcome != "completed" {
		t.Fatalf("provider attempt receipts=%+v", receipts)
	}
}

func TestToolSearchDeepSeekHeartbeatWithoutFinalHonorsCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			exited := make(chan struct{})
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				reader, writer := io.Pipe()
				go func() {
					defer close(exited)
					defer writer.Close()
					for {
						select {
						case <-req.Context().Done():
							_ = writer.CloseWithError(req.Context().Err())
							return
						case <-time.After(10 * time.Millisecond):
							if _, err := io.WriteString(writer, ": keep-alive\n\n"); err != nil {
								return
							}
						}
					}
				}()
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader, Request: req}, nil
			})}
			llm := newDeepSeek(Config{Provider: "deepseek", Model: "deepseek-v4-flash", HTTPClient: client, MaxOutputTok: 256000, Auth: AuthConfig{Type: AuthAPIKey, Token: "synthetic"}}, "synthetic")
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			if mode == "cancel" {
				time.AfterFunc(30*time.Millisecond, cancel)
			}
			var receipts []model.Invocation
			ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) { receipts = append(receipts, in) })
			_, err := toolsearch.NewAgentRanker().Rank(ctx, "synthetic lookup", []tool.Definition{{Name: "docs__lookup", Description: "Find a synthetic document"}}, 8, toolsearch.SearchModel{
				Model: llm,
				ReadSchema: func(context.Context, string) (tool.Definition, error) {
					t.Fatal("heartbeat without final result reached schema read")
					return tool.Definition{}, nil
				},
			})
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Fatal("stream reader did not exit after context cancellation")
			}
			if err == nil || requests != 1 || len(receipts) != 1 || receipts[0].Outcome != "cancelled" {
				t.Fatalf("error=%v requests=%d receipts=%+v", err, requests, receipts)
			}
		})
	}
}
