package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

func TestToolSearchOpenAICompatRequiresStreamTerminal(t *testing.T) {
	selection := `{"tools":["docs__lookup"]}`
	delta, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"role": "assistant", "content": selection}, "finish_reason": nil,
	}}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}})
	if err != nil {
		t.Fatal(err)
	}
	stop := `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	for _, tc := range []struct {
		name        string
		suffix      string
		wantNames   []string
		wantOutcome string
	}{
		{"EOF without terminal", "", nil, "failed"},
		{"finish reason then EOF", stop, []string{"docs__lookup"}, "completed"},
		{"DONE without finish reason", "data: [DONE]\n\n", []string{"docs__lookup"}, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Stream {
					t.Errorf("selector request stream=%t decode=%v", request.Stream, err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n%s", delta, tc.suffix)
			}))
			defer server.Close()
			llm := newOpenAICompat(Config{Provider: "openai-compatible", Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client(), Timeout: time.Second}, "synthetic")
			reads := 0
			var receipts []model.Invocation
			ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
			names, err := toolsearch.NewAgentRanker().Rank(ctx, "lookup", []tool.Definition{{Name: "docs__lookup", Description: "Lookup"}}, 1, toolsearch.SearchModel{
				Model: llm, ReadSchema: func(context.Context, string) (tool.Definition, error) {
					reads++
					return tool.Definition{}, fmt.Errorf("unexpected schema read")
				},
			})
			if tc.wantNames == nil && err == nil {
				t.Fatal("premature EOF published a selection")
			}
			if tc.wantNames != nil && err != nil {
				t.Fatalf("complete stream rejected: %v", err)
			}
			if !reflect.DeepEqual(names, tc.wantNames) || reads != 0 || requests != 1 {
				t.Fatalf("names=%v error=%v schema_reads=%d requests=%d", names, err, reads, requests)
			}
			if len(receipts) != 1 || receipts[0].Outcome != tc.wantOutcome || receipts[0].Usage.TotalTokens != 18 {
				t.Fatalf("attempt receipts=%+v", receipts)
			}
		})
	}
}

func TestToolSearchOpenAICompatCancelledBeforeTerminal(t *testing.T) {
	requests := 0
	exited := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		reader, writer := io.Pipe()
		go func() {
			defer close(exited)
			defer writer.Close()
			_, _ = io.WriteString(writer, `data: {"choices":[{"delta":{"role":"assistant","content":"{\"tools\":[\"docs__lookup\"]}"},"finish_reason":null}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`+"\n\n")
			<-req.Context().Done()
			_ = writer.CloseWithError(req.Context().Err())
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader, Request: req}, nil
	})}
	llm := newOpenAICompat(Config{Provider: "openai-compatible", Model: "test-model", HTTPClient: client, Timeout: time.Second}, "synthetic")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	var receipts []model.Invocation
	ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) { receipts = append(receipts, in) })
	names, err := toolsearch.NewAgentRanker().Rank(ctx, "lookup", []tool.Definition{{Name: "docs__lookup", Description: "Lookup"}}, 1, toolsearch.SearchModel{
		Model: llm, ReadSchema: func(context.Context, string) (tool.Definition, error) {
			t.Fatal("cancelled stream reached schema read")
			return tool.Definition{}, nil
		},
	})
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("cancelled stream reader did not exit")
	}
	if err == nil || len(names) != 0 || requests != 1 || len(receipts) != 1 || receipts[0].Outcome != "cancelled" || receipts[0].Usage.TotalTokens != 18 {
		t.Fatalf("names=%v error=%v requests=%d receipts=%+v", names, err, requests, receipts)
	}
	if !strings.Contains(err.Error(), "context") {
		t.Fatalf("cancel cause missing: %v", err)
	}
}
