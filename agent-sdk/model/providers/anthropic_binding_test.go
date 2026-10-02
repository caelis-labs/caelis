package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// Tool discovery and refreshed instructions can change a signed block's prefix.
// This server models the documented rejection for accounts enforcing binding.
func TestSonnet55ContinuesAfterThinkingPrefixChanges(t *testing.T) {
	for _, effort := range []string{"high", "off", "none", "disabled"} {
		for _, change := range []string{"tools", "instructions"} {
			t.Run(effort+"/"+change, func(t *testing.T) {
				calls := 0
				var originalPrefix any
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if calls == 1 {
						field := "tools"
						if change == "instructions" {
							field = "system"
						}
						originalPrefix = payload[field]
						_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"Find a tool.","signature":"signed-original-prefix"},{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{}}],"usage":{"input_tokens":1,"output_tokens":1}}`)
						return
					}
					thinking := nestedMapForTest(t, payload, "thinking")
					field := "tools"
					if change == "instructions" {
						field = "system"
					}
					if reflect.DeepEqual(payload[field], originalPrefix) {
						t.Error("follow-up did not change the signed prefix")
					}
					messages := payload["messages"].([]any)
					content := messages[1].(map[string]any)["content"].([]any)
					var signed bool
					for _, item := range content {
						block := item.(map[string]any)
						if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
							signed = true
						}
					}
					if effort != "high" {
						if signed || thinking["type"] != "between_tools" || thinking["block_binding"] != nil {
							w.WriteHeader(http.StatusBadRequest)
							_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid thinking prefix or between_tools block_binding"}}`)
							return
						}
					} else {
						binding, _ := thinking["block_binding"].(map[string]any)
						if !signed || binding["prefix_mismatch_behavior"] != "drop_block" || !strings.Contains(r.Header.Get("anthropic-beta"), "thinking-binding-controls-2026-08-01") {
							w.WriteHeader(http.StatusBadRequest)
							_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Thinking block is bound to a different conversation"}}`)
							return
						}
						if !strings.Contains(r.Header.Get("anthropic-beta"), "existing-beta") {
							t.Error("existing beta header was lost")
						}
					}
					_, _ = fmt.Fprint(w, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
				}))
				defer server.Close()
				name := "claude-sonnet-5-5"
				if change == "instructions" {
					name += "-20260928"
				}
				llm := newAnthropic(Config{Provider: "anthropic", Model: name, BaseURL: server.URL, HTTPClient: server.Client(), Headers: map[string]string{"Anthropic-Beta": "existing-beta"}}, "synthetic-token")
				request := &model.Request{
					Instructions: []model.Part{model.NewTextPart("original instructions")},
					Messages:     []model.Message{model.NewTextMessage(model.RoleUser, "find a tool")},
					Tools:        []model.ToolSpec{model.NewFunctionToolSpec("ToolSearch", "find tools", map[string]any{"type": "object"})},
					Reasoning:    model.ReasoningConfig{Effort: effort},
				}
				var first model.Message
				for event, err := range llm.Generate(context.Background(), request) {
					if err != nil {
						t.Fatal(err)
					}
					if event.Response != nil {
						first = event.Response.Message
					}
				}
				request.Messages = append(request.Messages, first, model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("search_1", "ToolSearch", map[string]any{"tools": []string{"lookup"}}, false)))
				if change == "tools" {
					request.Tools = append(request.Tools, model.NewFunctionToolSpec("lookup", "look up a value", map[string]any{"type": "object"}))
				} else {
					request.Instructions = []model.Part{model.NewTextPart("refreshed instructions")}
				}
				before := model.CloneMessages(request.Messages)
				for _, err := range llm.Generate(context.Background(), request) {
					if err != nil {
						t.Fatalf("follow-up after %s changed: %v", change, err)
					}
				}
				if calls != 2 || !reflect.DeepEqual(request.Messages, before) {
					t.Fatalf("calls = %d; request history mutated = %v", calls, !reflect.DeepEqual(request.Messages, before))
				}
			})
		}
	}
}

func TestSonnet55ThinkingBindingLeavesOtherModelsUnchanged(t *testing.T) {
	for _, name := range []string{"claude-sonnet-5", "claude-opus-5-5", "claude-fable-5-1"} {
		t.Run(name, func(t *testing.T) {
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("anthropic-beta"); got != "existing-beta" {
					t.Errorf("anthropic-beta = %q, want unchanged header", got)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if thinking := nestedMapForTest(t, payload, "thinking"); thinking["block_binding"] != nil {
					t.Errorf("unexpected binding control: %#v", thinking)
				}
				messages := payload["messages"].([]any)
				content := messages[1].(map[string]any)["content"].([]any)
				if block := content[0].(map[string]any); block["type"] != "thinking" || block["signature"] != "unchanged-signature" {
					t.Errorf("prior reasoning changed: %#v", block)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,"stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":1}}`, name)
			}))
			defer server.Close()
			llm := newAnthropic(Config{Provider: "anthropic", Model: name, BaseURL: server.URL, HTTPClient: server.Client(), Headers: map[string]string{"Anthropic-Beta": "existing-beta"}}, "synthetic-token")
			part := model.NewReasoningPart("prior reasoning", model.ReasoningVisibilityVisible)
			part.Reasoning.Replay = anthropicReplayMeta("anthropic", "unchanged-signature")
			for _, err := range llm.Generate(context.Background(), &model.Request{
				Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hello"), model.NewMessage(model.RoleAssistant, part, model.NewTextPart("done")), model.NewTextMessage(model.RoleUser, "continue")},
				Reasoning: model.ReasoningConfig{Effort: "off"},
			}) {
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
