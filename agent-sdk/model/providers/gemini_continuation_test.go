package providers

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

// A dependency update must not turn an incomplete response into additional
// billed generations hidden inside one Runtime admission and usage receipt.
func TestGeminiContinuationDoesNotBypassInvocationBoundary(t *testing.T) {
	for _, mode := range []string{"nonstream", "stream", "toolsearch", "websearch"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				wire := `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"CONTINUATION","continuationToken":"dG9rZW4="}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`
				if requests.Add(1) > 1 {
					wire = `{"candidates":[{"content":{"role":"model","parts":[{"text":"continued"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":13,"candidatesTokenCount":5,"totalTokenCount":18}}`
				}
				if mode == "stream" || mode == "toolsearch" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", wire)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, wire)
				}
			}))
			defer server.Close()
			llm := newGemini(Config{Provider: "gemini", Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client()}, "synthetic")
			var receipts []model.Invocation
			ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
			switch mode {
			case "websearch":
				// SearchWeb retains its existing partial-answer contract.
				out, err := llm.(*geminiLLM).SearchWeb(ctx, model.WebSearchRequest{Query: "synthetic"})
				if err != nil || out.Answer != "partial" || out.Usage.TotalTokens != 18 {
					t.Fatalf("search=%+v err=%v", out, err)
				}
			case "toolsearch":
				names, err := toolsearch.NewAgentRanker().Rank(ctx, "lookup", []tool.Definition{{Name: "docs__lookup", Description: "Lookup"}}, 1, toolsearch.SearchModel{
					Model: llm,
					ReadSchema: func(context.Context, string) (tool.Definition, error) {
						return tool.Definition{}, fmt.Errorf("unexpected schema read")
					},
				})
				if err == nil || len(names) != 0 {
					t.Fatalf("incomplete selection: names=%v err=%v", names, err)
				}
			default:
				var final *model.Response
				var generateErr error
				for event, err := range model.Generate(ctx, llm, &model.Request{
					Messages: []model.Message{model.NewTextMessage(model.RoleUser, "synthetic")},
					Stream:   mode == "stream",
				}) {
					if err != nil {
						generateErr = err
					}
					if event != nil && event.Response != nil {
						final = event.Response
					}
				}
				if generateErr == nil || final != nil {
					t.Fatalf("incomplete generation: final=%+v err=%v", final, generateErr)
				}
			}
			if requests.Load() != 1 {
				t.Fatalf("provider continued without another admission: requests=%d", requests.Load())
			}
			if mode != "websearch" && (len(receipts) != 1 || receipts[0].Outcome != "failed" || receipts[0].Usage.TotalTokens != 18) {
				t.Fatalf("incomplete invocation receipts=%+v", receipts)
			}
		})
	}
}
