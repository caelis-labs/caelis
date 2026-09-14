package providers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestToolAvailabilityPreservesHistoryAcrossProviderProtocols(t *testing.T) {
	for _, protocol := range []string{"anthropic", "gemini", "chat", "ollama", "responses", "codex", "xai"} {
		t.Run(protocol, func(t *testing.T) {
			req := &model.Request{
				Tools: model.ToolSpecsFromDefinitions([]model.ToolDefinition{{Name: "Read", Parameters: map[string]any{"type": "object"}}}),
				Messages: []model.Message{
					model.NewTextMessage(model.RoleUser, "Decide using the available evidence."),
					model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call_1", Name: "Read", Args: `{}`}}, ""),
					model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("call_1", "Read", map[string]any{"error": "evidence unavailable"}, true)),
				},
			}
			wire := func() map[string]any {
				var value any
				var err error
				switch protocol {
				case "codex":
					value, err = openAICodexRequestFromModel(req, "test-model")
				case "xai":
					value, err = xAIResponsesRequestFromModel(req, "test-model", 100)
				case "responses":
					value, err = newOpenAIResponses(Config{API: APIOpenAI, Model: "test-model"}, "token").buildRequest(req)
				default:
					captured := make(chan map[string]any, 1)
					server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var payload map[string]any
						if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
							t.Error(decodeErr)
						}
						captured <- payload
						// Only the outgoing wire is under test; a non-retryable response
						// avoids relying on each provider's response fixture format.
						http.Error(w, "synthetic response", http.StatusBadRequest)
					}))
					defer server.Close()
					cfg := Config{Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client()}
					var llm model.LLM
					switch protocol {
					case "anthropic":
						llm = newAnthropic(cfg, "token")
					case "gemini":
						llm = newGemini(cfg, "token")
					case "chat":
						llm = newOpenAICompat(cfg, "token")
					case "ollama":
						llm = newOllama(cfg, "token")
					}
					for range llm.Generate(t.Context(), req) {
					}
					select {
					case value = <-captured:
					default:
						t.Fatal("provider did not send request")
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				return payload
			}
			before := wire()
			req.DisableTools = true
			after := wire()
			for _, key := range []string{"messages", "input", "contents"} {
				if !reflect.DeepEqual(before[key], after[key]) {
					t.Fatalf("disabling selection changed %s history", key)
				}
			}
			if protocol == "ollama" {
				if after["tools"] != nil {
					t.Fatal("native Ollama must omit tools to disable selection")
				}
				return
			}
			if before["tools"] == nil || !reflect.DeepEqual(before["tools"], after["tools"]) {
				t.Fatal("tool definitions changed with historical results still present")
			}
			choice := after["tool_choice"]
			if protocol == "anthropic" {
				choice = choice.(map[string]any)["type"]
			}
			if protocol == "gemini" {
				choice = after["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)["mode"]
				if choice != "NONE" {
					t.Fatalf("tool selection = %v", choice)
				}
			} else if choice != "none" {
				t.Fatalf("tool selection = %v", choice)
			}
		})
	}
}
