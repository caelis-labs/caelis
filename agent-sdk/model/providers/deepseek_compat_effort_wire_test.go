package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// The custom /v1 route uses the OpenAI-compatible adapter, not the Anthropic
// SDK. Check its actual serialized request for each supported DeepSeek model
// name and effort, including the current public deepseek-flash name.
func TestDeepSeekCustomCompatStreamingEffortWire(t *testing.T) {
	for _, modelName := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro"} {
		for _, tc := range []struct {
			requested, wantThinking, wantEffort string
		}{
			{"", "enabled", "high"},
			{"none", "disabled", ""},
			{"low", "enabled", "low"},
			{"high", "enabled", "high"},
			{"max", "enabled", "max"},
			{"minimal", "enabled", "low"},
			{"medium", "enabled", "high"},
			{"xhigh", "enabled", "high"},
			{"ultra", "enabled", "max"},
		} {
			t.Run(modelName+"/"+tc.requested, func(t *testing.T) {
				requests := 0
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.URL.Path != "/v1/chat/completions" {
						t.Errorf("path = %q, want custom OpenAI-compatible route", r.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if body["stream"] != true || body["model"] != modelName {
						t.Errorf("stream/model = %v/%v", body["stream"], body["model"])
					}
					thinking, _ := body["thinking"].(map[string]any)
					if got, _ := thinking["type"].(string); got != tc.wantThinking {
						t.Errorf("thinking.type = %q, want %q", got, tc.wantThinking)
					}
					if got, _ := body["reasoning_effort"].(string); got != tc.wantEffort {
						t.Errorf("reasoning_effort = %q, want %q", got, tc.wantEffort)
					}
					if _, ok := body["reasoning"]; ok {
						t.Errorf("unexpected generic reasoning field")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n")
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				}))
				defer server.Close()
				llm := newDeepSeek(Config{Provider: "deepseek", Model: modelName, BaseURL: server.URL + "/v1", HTTPClient: server.Client()}, "synthetic")
				if _, ok := llm.(*openAICompatLLM); !ok {
					t.Fatalf("custom /v1 model = %T, want OpenAI-compatible adapter", llm)
				}
				var final *model.Response
				for event, err := range llm.Generate(context.Background(), &model.Request{
					Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "synthetic")},
					Reasoning: model.ReasoningConfig{Effort: tc.requested}, Stream: true,
				}) {
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
}
