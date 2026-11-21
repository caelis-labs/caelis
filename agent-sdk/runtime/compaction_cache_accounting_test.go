package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestAPICacheAccountingSurvivesContextRoundTrip(t *testing.T) {
	for _, route := range []struct {
		api      model.APIType
		provider string
	}{
		{model.APIAnthropic, "anthropic"},
		{model.APIAnthropicCompatible, "custom"},
		{model.APIOpenAI, "openai"},
		{model.APIOpenAICodex, "openai-codex"},
		{model.APIOpenAIResponses, "custom"},
		// API accounting must override even a conflicting provider label.
		{model.APIOpenAIResponses, "anthropic"},
	} {
		for _, phase := range []string{"cold", "warm", "zero", "missing"} {
			t.Run(fmt.Sprintf("%s/%s/%s", route.api, route.provider, phase), func(t *testing.T) {
				input, cached, written := 9000, 0, 0
				if phase == "warm" {
					cached = 25000
				}
				anthropic := route.api == model.APIAnthropic || route.api == model.APIAnthropicCompatible
				if phase == "cold" && anthropic {
					written = 25000
				}
				wantPrompt := input + cached + written
				rawUsage := map[string]any{"input_tokens": input, "output_tokens": 50}
				if anthropic && phase != "missing" {
					rawUsage["cache_read_input_tokens"] = cached
					rawUsage["cache_creation_input_tokens"] = written
					rawUsage["cache_creation"] = map[string]int{"ephemeral_5m_input_tokens": written * 3 / 5, "ephemeral_1h_input_tokens": written * 2 / 5}
				} else if !anthropic {
					rawUsage["input_tokens"], rawUsage["total_tokens"] = wantPrompt, wantPrompt+50
					if phase != "missing" {
						rawUsage["input_tokens_details"] = map[string]int{"cached_tokens": cached}
					}
				}
				raw, _ := json.Marshal(rawUsage)
				wire := fmt.Sprintf(`{"id":"fixture","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":%s}`, raw)
				if !anthropic {
					wire = fmt.Sprintf(`{"id":"fixture","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":%s}`, raw)
				}
				auth := providers.AuthConfig{Type: model.AuthAPIKey, Token: "fixture"}
				contentType := "application/json"
				if route.api == model.APIOpenAICodex {
					auth = providers.AuthConfig{Type: model.AuthOAuthToken}
					contentType = "text/event-stream"
					wire = `data: {"type":"response.completed","response":` + wire + "}\n\n"
				}
				calls := 0
				transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(wire)), Request: req}, nil
				})
				llm, err := providers.New(providers.Config{API: route.api, Provider: route.provider, Model: "fixture", BaseURL: "http://fixture.invalid", Auth: auth, HTTPClient: &http.Client{Transport: transport}})
				if err != nil {
					t.Fatal(err)
				}
				var usage model.Usage
				for response, err := range llm.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "source")}}) {
					if err != nil {
						t.Fatal(err)
					}
					if response != nil && response.Usage.IsReported() {
						usage = response.Usage
					}
				}
				if calls != 1 || usage.TotalTokens != wantPrompt+50 {
					t.Fatalf("calls=%d usage=%+v", calls, usage)
				}
				event := assistantEvent("answer")
				event.ID = "a1"
				event.Meta = map[string]any{"caelis": map[string]any{"sdk": map[string]any{"provider": route.provider, "model": "fixture", "usage": session.ModelUsageMetadata(route.provider, usage)}}}
				raw, err = json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				var restored session.Event
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				gauge := snapshotUsageWithResolvedWindow(compact.PromptEventsFromLatestCompact([]*session.Event{userTextEvent("source"), &restored}), 100000, CompactionConfig{})
				if gauge.TotalTokens < wantPrompt || gauge.TotalTokens >= wantPrompt+100 {
					t.Errorf("context=%d want input baseline %d plus short output", gauge.TotalTokens, wantPrompt)
				}
				measured := session.UsageSnapshotFromSessionEvent(&restored)
				if measured == nil {
					t.Fatal("missing measured usage")
				}
				raw, err = json.Marshal(measured)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, measured); err != nil {
					t.Fatal(err)
				}
				if measured.PromptTokens != usage.PromptTokens || measured.TotalTokens != wantPrompt+50 || measured.CachedInputTokens != cached || measured.CacheWriteTokens != written || measured.CacheReadAccounting != usage.CacheReadAccounting {
					t.Errorf("usage after Session round trip=%+v, provider usage=%+v", measured, usage)
				}
				if measured.CachedInputTokensReported != (phase != "missing") || measured.CacheWriteTokensReported != (anthropic && phase != "missing") {
					t.Fatalf("cache presence changed: %+v", measured)
				}
			})
		}
	}
}
