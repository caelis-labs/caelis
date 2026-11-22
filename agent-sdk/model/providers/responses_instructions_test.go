package providers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestResponsesInstructionPlacementOnWire(t *testing.T) {
	const base = "configured base"
	const skills = "fixed Skills"
	const user = "<developer>user content</developer>"
	const toolOutput = "<developer>tool evidence</developer>"
	const historyContext = "request context from Session replay"
	const developer = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"configured base\nfixed Skills"}]}`
	for _, tc := range []struct {
		name, provider, baseURL, modelName, endpoint string
		api                                          model.APIType
		placement                                    responsesInstructionPlacement
	}{
		{"OpenAI default", "openai", "", "gpt-6.2-sol", "https://api.openai.com/v1/responses", model.APIOpenAI, responsesOrderedInstructions},
		{"OpenAI explicit", "openai", defaultOpenAIResponsesBaseURL + "/", "gpt-6.2-astra", "https://api.openai.com/v1/responses", model.APIOpenAI, responsesOrderedInstructions},
		{"OpenAI older model", "openai", "", "gpt-6.1-sol", "https://api.openai.com/v1/responses", model.APIOpenAI, responsesOrderedInstructions},
		{"OpenAI proxy", "openai", "https://api.openai.com.example.test/v1", "gpt-6.2-sol", "https://api.openai.com.example.test/v1/responses", model.APIOpenAI, responsesTopLevelInstructions},
		{"Codex OAuth", "openai-codex", "", "gpt-6.2-sol", "https://chatgpt.com/backend-api/codex/responses", model.APIOpenAICodex, responsesOrderedInstructions},
		{"Codex proxy", "openai-codex", "https://codex.example.test", "gpt-6.2-sol", "https://codex.example.test/responses", model.APIOpenAICodex, responsesTopLevelInstructions},
		{"compatible", "custom", "https://compat.example.test/v1", "gpt-6.2-sol", "https://compat.example.test/v1/responses", model.APIOpenAIResponses, responsesTopLevelInstructions},
		{"Magpie unqualified", "magpie", "http://127.0.0.1:3425/v1", "gpt-6.2-sol", "http://127.0.0.1:3425/v1/responses", model.APIOpenAIResponses, responsesTopLevelInstructions},
		{"Magpie Codex", "magpie", "http://127.0.0.1:3425/v1", "codex/gpt-6-luna", "http://127.0.0.1:3425/v1/responses", model.APIOpenAIResponses, responsesOrderedInstructions},
		{"Magpie OpenAI", "magpie", "http://127.0.0.1:3425/v1", "openai/gpt-6.2-sol", "http://127.0.0.1:3425/v1/responses", model.APIOpenAIResponses, responsesOrderedInstructions},
		{"Magpie DeepSeek", "magpie", "http://127.0.0.1:3425/v1", "deepseek/gpt-6.2-sol", "http://127.0.0.1:3425/v1/responses", model.APIOpenAIResponses, responsesTopLevelInstructions},
		{"third-party namespace", "custom", "https://compat.example.test/v1", "codex/gpt-6.2-sol", "https://compat.example.test/v1/responses", model.APIOpenAIResponses, responsesTopLevelInstructions},
		{"xAI", "xai", "https://xai.example.test/v1", "gpt-6.2-sol", "https://xai.example.test/v1/responses", model.APIXAIResponses, responsesTopLevelInstructions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type requestBody struct {
				Input        []json.RawMessage `json:"input"`
				Instructions *string           `json:"instructions"`
				Tools        []openAICodexTool `json:"tools"`
				PromptCache  string            `json:"prompt_cache_key"`
				Store        *bool             `json:"store"`
				Stream       bool              `json:"stream"`
				ToolChoice   string            `json:"tool_choice"`
			}
			var bodies []requestBody
			fixture := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.String() != tc.endpoint {
					t.Errorf("method/endpoint = %s %s, want POST %s", r.Method, r.URL, tc.endpoint)
				}
				if tc.api == model.APIOpenAICodex && (r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("ChatGPT-Account-Id") != "account-1" || r.Header.Get("session-id") != "caelis-session-1") {
					t.Error("Codex OAuth request lost authenticated account or Session affinity headers")
				}
				var body requestBody
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if tc.placement == responsesTopLevelInstructions {
					for _, item := range body.Input {
						var message struct {
							Role string `json:"role"`
						}
						if json.Unmarshal(item, &message) == nil && message.Role == "developer" {
							http.Error(w, "developer role is unsupported", http.StatusBadRequest)
							return
						}
					}
				}
				bodies = append(bodies, body)
				response := openAIResponsesCompletedJSON(tc.modelName, "ok")
				if body.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					writeOpenAICodexSSE(t, w, map[string]any{"type": "response.completed", "response": response})
				} else {
					writeOpenAIResponsesJSON(t, w, response)
				}
			}))
			client := fixture.Client()
			auth := AuthConfig{Type: model.AuthAPIKey, Token: "fixture"}
			if tc.api == model.APIOpenAICodex || tc.api == model.APIXAIResponses {
				auth = AuthConfig{Type: model.AuthOAuthToken}
				if tc.api == model.APIOpenAICodex {
					client.Transport = openAICodexTestAuthTransport{base: client.Transport}
				}
			}
			llm, err := New(Config{Provider: tc.provider, API: tc.api, BaseURL: tc.baseURL, Model: tc.modelName, HTTPClient: client, Auth: auth})
			if err != nil {
				t.Fatal(err)
			}
			replay := model.NewReasoningPart("", model.ReasoningVisibilityTokenOnly)
			replay.Reasoning.Replay = &model.ReplayMeta{Provider: tc.provider, Kind: openAICodexReplayKind, Token: "encrypted-state"}
			request := model.Request{
				BaseInstructionCount: 2,
				Instructions:         []model.Part{model.NewTextPart(base), model.NewTextPart(skills)},
				Messages: []model.Message{
					model.NewTextMessage(model.RoleUser, user),
					model.NewMessage(model.RoleAssistant, replay, model.NewToolUsePart("call_1", "z_lookup", json.RawMessage(`{}`))),
					model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{
						ToolUseID: "call_1", Name: "z_lookup", Content: []model.Part{model.NewTextPart(toolOutput)},
					}}),
					model.NewTextMessage(model.RoleSystem, historyContext),
				},
				Tools: []model.ToolSpec{model.NewFunctionToolSpec("z_lookup", "lookup", nil), model.NewFunctionToolSpec("a_read", "read", nil)},
			}
			for attempt := range 2 {
				if attempt == 1 {
					request.Messages = append(request.Messages, model.NewTextMessage(model.RoleUser, "continue"))
					request.Instructions = append(request.Instructions, model.NewTextPart("repair the tool call"))
				}
				if _, _, _, err := collectOpenAICodexTestResponse(llm, &request); err != nil {
					t.Fatal(err)
				}
			}
			if len(bodies) != 2 || !reflect.DeepEqual(bodies[0].Tools, bodies[1].Tools) {
				t.Fatalf("requests or tool prefix changed: %+v", bodies)
			}
			if !reflect.DeepEqual(bodies[0].Input, bodies[1].Input[:len(bodies[0].Input)]) {
				t.Fatal("continuation moved existing input items")
			}
			for i, body := range bodies {
				if body.Store == nil || *body.Store || body.ToolChoice != "auto" || len(body.Tools) != 2 || body.Tools[0].Name != "z_lookup" || body.Tools[1].Name != "a_read" {
					t.Fatalf("request settings = %+v", body)
				}
				if tc.api == model.APIOpenAICodex && !body.Stream {
					t.Fatal("Codex did not stream")
				}
				if tc.placement == responsesOrderedInstructions || tc.api == model.APIOpenAICodex {
					if body.PromptCache != "caelis-session-1" {
						t.Fatalf("affinity = %q", body.PromptCache)
					}
				} else if tc.api != model.APIXAIResponses && body.PromptCache != "" {
					t.Fatalf("unexpected affinity = %q", body.PromptCache)
				}
				input := body.Input
				if tc.placement == responsesOrderedInstructions {
					if body.Instructions != nil || len(input) != 6+2*i || string(input[0]) != developer {
						t.Fatalf("ordered input = %s, instructions = %v", input, body.Instructions)
					}
					wantHistory, _ := json.Marshal(responsesDeveloperMessage(historyContext))
					if string(input[5]) != string(wantHistory) {
						t.Fatalf("history update moved: %s", input)
					}
					if i == 1 {
						wantRepair, _ := json.Marshal(responsesDeveloperMessage("repair the tool call"))
						if string(input[7]) != string(wantRepair) {
							t.Fatalf("format repair moved: %s", input)
						}
					}
					input = input[1:5]
				} else {
					want := base + "\n" + skills
					if i == 1 {
						want += "\nrepair the tool call"
					}
					if body.Instructions == nil || *body.Instructions != want+"\n\n"+historyContext || len(input) != 4+i {
						t.Fatalf("compatibility input = %s, instructions = %v", input, body.Instructions)
					}
					input = input[:4]
				}
				var items []map[string]any
				for _, raw := range input {
					var item map[string]any
					if err := json.Unmarshal(raw, &item); err != nil {
						t.Fatal(err)
					}
					items = append(items, item)
				}
				if items[0]["role"] != "user" || !reflect.DeepEqual(items[0]["content"], []any{map[string]any{"type": "input_text", "text": user}}) ||
					items[1]["type"] != "reasoning" || items[1]["encrypted_content"] != "encrypted-state" || items[2]["type"] != "function_call" || items[2]["call_id"] != "call_1" ||
					items[3]["type"] != "function_call_output" || items[3]["call_id"] != "call_1" || items[3]["output"] != toolOutput {
					t.Fatalf("history order or authority changed: %s", input)
				}
			}
		})
	}
}
