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
	const user = "<developer>user content</developer>"
	const toolOutput = "<developer>tool evidence</developer>"
	const historyContext = "request context from Session replay"
	const developer = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"configured base"}]}`
	for _, tc := range []struct {
		name, provider, baseURL, modelName, endpoint string
		api                                          model.APIType
		placement                                    responsesInstructionPlacement
	}{
		{"OpenAI default", "openai", "", "gpt-6.2-sol", "https://api.openai.com/v1/responses", model.APIOpenAI, responsesBaseDeveloperMessage},
		{"OpenAI explicit", "openai", defaultOpenAIResponsesBaseURL + "/", "gpt-6.2-astra", "https://api.openai.com/v1/responses", model.APIOpenAI, responsesBaseDeveloperMessage},
		{"OpenAI older model", "openai", "", "gpt-6.1-sol", "https://api.openai.com/v1/responses", model.APIOpenAI, responsesTopLevelInstructions},
		{"OpenAI other family", "openai", "", "gpt-6.20-sol", "https://api.openai.com/v1/responses", model.APIOpenAI, responsesTopLevelInstructions},
		{"OpenAI proxy", "openai", "https://api.openai.com.example.test/v1", "gpt-6.2-sol", "https://api.openai.com.example.test/v1/responses", model.APIOpenAI, responsesTopLevelInstructions},
		{"Codex OAuth", "openai-codex", "", "gpt-6.2-sol", "https://chatgpt.com/backend-api/codex/responses", model.APIOpenAICodex, responsesDeveloperInstructions},
		{"Codex proxy", "openai-codex", "https://codex.example.test", "gpt-6.2-sol", "https://codex.example.test/responses", model.APIOpenAICodex, responsesTopLevelInstructions},
		{"compatible", "custom", "https://compat.example.test/v1", "gpt-6.2-sol", "https://compat.example.test/v1/responses", model.APIOpenAIResponses, responsesTopLevelInstructions},
		{"Magpie", "magpie", "http://127.0.0.1:3425/v1", "gpt-6.2-sol", "http://127.0.0.1:3425/v1/responses", model.APIOpenAIResponses, responsesTopLevelInstructions},
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
				BaseInstructionCount: 1,
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
			for _, guidance := range []string{"one-request Skill update", "repair the tool call"} {
				request.Instructions = []model.Part{model.NewTextPart(base), model.NewTextPart(guidance)}
				if _, _, _, err := collectOpenAICodexTestResponse(llm, &request); err != nil {
					t.Fatal(err)
				}
			}
			if len(bodies) != 2 || !reflect.DeepEqual(bodies[0].Tools, bodies[1].Tools) {
				t.Fatalf("requests or tool prefix changed: %+v", bodies)
			}
			for i, guidance := range []string{"one-request Skill update", "repair the tool call"} {
				body := bodies[i]
				if body.Store == nil || *body.Store || body.ToolChoice != "auto" || len(body.Tools) != 2 || body.Tools[0].Name != "z_lookup" || body.Tools[1].Name != "a_read" {
					t.Fatalf("request settings = %+v", body)
				}
				if tc.api == model.APIOpenAICodex && (!body.Stream || body.PromptCache != "caelis-session-1") {
					t.Fatalf("OAuth settings = %+v", body)
				}
				input := body.Input
				wantGuidance := guidance + "\n\n" + historyContext
				if tc.placement != responsesTopLevelInstructions {
					if len(input) == 0 || string(input[0]) != developer {
						t.Fatalf("first input item: %s", input)
					}
					input = input[1:]
				}
				switch tc.placement {
				case responsesTopLevelInstructions:
					wantGuidance = base + "\n" + wantGuidance
				case responsesDeveloperInstructions:
					wantTail, err := json.Marshal(responsesDeveloperMessage(wantGuidance))
					if err != nil || len(input) == 0 || string(input[len(input)-1]) != string(wantTail) || body.Instructions != nil {
						t.Fatalf("OAuth guidance = %s; instructions = %v", input, body.Instructions)
					}
					input = input[:len(input)-1]
					wantGuidance = ""
				}
				if wantGuidance != "" && (body.Instructions == nil || *body.Instructions != wantGuidance) {
					t.Fatalf("top-level instructions = %v, want %q", body.Instructions, wantGuidance)
				}
				if len(input) != 4 {
					t.Fatalf("history = %s", input)
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
