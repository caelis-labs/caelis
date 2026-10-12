package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestNoSelectorReasoningKeepsProviderDefaultAndExplicitControls(t *testing.T) {
	for _, provider := range []string{"openrouter", "ollama"} {
		for _, tc := range []struct {
			name, mode, effort string
			levels             []string
			omitted            bool
		}{
			{name: "no selector", mode: "none", effort: "none", omitted: true},
			{name: "explicit effort", mode: "none", effort: "high"},
			{name: "declared levels", mode: "none", effort: "none", levels: []string{"none", "high"}},
			{name: "manual mode", mode: "toggle", effort: "none"},
			{name: "compatible default", effort: "none"},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				called := false
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					field := "reasoning"
					if provider == "ollama" {
						field = "think"
					}
					_, present := payload[field]
					if present == tc.omitted {
						t.Errorf("%s = %#v, omitted = %t", field, payload[field], tc.omitted)
					}
					if provider == "openrouter" {
						if tc.omitted {
							if _, present := payload["reasoning_effort"]; present {
								t.Error("legacy disable parameter still present")
							}
						} else if got := payload["reasoning"].(map[string]any)["effort"]; got != tc.effort {
							t.Errorf("explicit effort = %#v, want %s", got, tc.effort)
						}
						_, _ = fmt.Fprint(w, `{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
					} else {
						if !tc.omitted && payload["think"] != (tc.effort != "none") {
							t.Errorf("explicit think = %#v", payload["think"])
						}
						_, _ = fmt.Fprint(w, `{"model":"fixture","message":{"role":"assistant","content":"done"},"done":true}`)
					}
				}))
				defer server.Close()
				cfg := Config{Provider: provider, Model: "fixture", BaseURL: server.URL, HTTPClient: server.Client(), ReasoningMode: tc.mode, ReasoningLevels: tc.levels}
				var llm model.LLM
				if provider == "openrouter" {
					llm = newOpenRouter(cfg, "synthetic-token")
				} else {
					llm = newOllama(cfg, "synthetic-token")
				}
				for _, err := range llm.Generate(context.Background(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")}, Reasoning: model.ReasoningConfig{Effort: tc.effort}}) {
					if err != nil {
						t.Fatal(err)
					}
				}
				if !called {
					t.Fatal("no wire request")
				}
			})
		}
	}
}
