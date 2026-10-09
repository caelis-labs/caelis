package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// Exercise the SDK's actual streaming HTTP serialization, not just the
// MessageNewParams builder: DeepSeek ignores thinking.budget_tokens and takes
// reasoning strength from output_config.effort.
func TestDeepSeekAnthropicStreamingEffortWire(t *testing.T) {
	for _, tc := range []struct {
		effort, wantThinking, wantEffort string
	}{
		{"", "", ""},
		{"none", "disabled", ""},
		{"low", "enabled", "low"},
		{"high", "enabled", "high"},
		{"max", "enabled", "max"},
		{"minimal", "enabled", "low"},
		{"medium", "enabled", "high"},
		{"xhigh", "enabled", "high"},
		{"ultra", "enabled", "max"},
	} {
		t.Run("effort="+tc.effort, func(t *testing.T) {
			requests := 0
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/anthropic/v1/messages" {
					t.Errorf("path=%q", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if body["stream"] != true || body["model"] != "deepseek-flash" {
					t.Errorf("stream/model = %v/%v", body["stream"], body["model"])
				}
				thinking, _ := body["thinking"].(map[string]any)
				if got, _ := thinking["type"].(string); got != tc.wantThinking {
					t.Errorf("thinking.type=%q want %q", got, tc.wantThinking)
				}
				output, _ := body["output_config"].(map[string]any)
				if got, _ := output["effort"].(string); got != tc.wantEffort {
					t.Errorf("output_config.effort=%q want %q", got, tc.wantEffort)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"deepseek-flash\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
				_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			llm := newDeepSeek(Config{Provider: "deepseek", Model: "deepseek-flash", BaseURL: server.URL + "/anthropic", HTTPClient: server.Client(), Auth: AuthConfig{Type: AuthAPIKey, Token: "synthetic"}}, "synthetic")
			var final *model.Response
			for event, err := range llm.Generate(context.Background(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "synthetic")}, Reasoning: model.ReasoningConfig{Effort: tc.effort}, Stream: true}) {
				if err != nil {
					t.Fatalf("Generate error: %v", err)
				}
				if event != nil && event.Response != nil {
					final = event.Response
				}
			}
			if requests != 1 || final == nil || final.FinishReason != model.FinishReasonStop || final.Usage.TotalTokens != 3 {
				t.Fatalf("requests=%d final=%+v", requests, final)
			}
		})
	}
}
