package providers

import (
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestOpenAICodexReasoningSummaryIndexesBecomeStepBoundaries(t *testing.T) {
	t.Parallel()

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeOpenAICodexSSE(t, w,
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs_1", "type": "reasoning", "summary": []any{}}},
			map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "rs_1", "output_index": 0, "summary_index": 0, "delta": "**Clarifying pause behavior**"},
			map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "rs_1", "output_index": 0, "summary_index": 1, "delta": "**Analyzing scan errors**"},
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{
				"id": "rs_1", "type": "reasoning", "summary": []any{
					map[string]any{"type": "summary_text", "text": "**Clarifying pause behavior**"},
					map[string]any{"type": "summary_text", "text": "**Analyzing scan errors**"},
				},
			}},
			map[string]any{"type": "response.completed", "response": map[string]any{
				"model": "gpt-5.4", "status": "completed", "output": []any{
					map[string]any{"id": "rs_1", "type": "reasoning", "summary": []any{
						map[string]any{"type": "summary_text", "text": "**Clarifying pause behavior**"},
						map[string]any{"type": "summary_text", "text": "**Analyzing scan errors**"},
					}},
				},
			}},
		)
	}))
	defer server.Close()

	llm := newOpenAICodex(Config{
		Provider:   "openai-codex",
		Model:      "gpt-5.4",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	response, reasoningDelta, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "check")},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	want := "**Clarifying pause behavior**\n**Analyzing scan errors**"
	if reasoningDelta != want {
		t.Fatalf("reasoning delta = %q, want %q", reasoningDelta, want)
	}
	if got := response.Message.ReasoningText(); got != want {
		t.Fatalf("final reasoning = %q, want %q", got, want)
	}
}
