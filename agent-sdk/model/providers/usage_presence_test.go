package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/internal/sdkctx"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestProviderUsagePresenceDistinguishesMissingAndZero(t *testing.T) {
	for _, tc := range []struct {
		raw             string
		usage, measured bool
		cached          int
	}{
		{raw: `{}`},
		{raw: `{"usage":null}`},
		{raw: `{"usage":{}}`},
		{raw: `{"usage":{"total_tokens":0}}`, usage: true},
		{raw: `{"usage":{"input_tokens_details":{"cached_tokens":null}}}`},
		{raw: `{"usage":{"input_tokens_details":{"cached_tokens":0}}}`, usage: true, measured: true},
		{raw: `{"usage":{"input_tokens_details":{"cached_tokens":124}}}`, usage: true, measured: true, cached: 124},
	} {
		var compat struct {
			Usage openAICompatUsage `json:"usage"`
		}
		var codex struct {
			Usage openAICodexUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(tc.raw), &compat); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.raw), &codex); err != nil {
			t.Fatal(err)
		}
		for _, usage := range []model.Usage{compat.Usage.toKernelUsage(), codex.Usage.toKernelUsage()} {
			if usage.IsReported() != tc.usage || usage.CachedInputTokensReported != tc.measured || usage.CachedInputTokens != tc.cached {
				t.Fatalf("presence mismatch for %s: %+v", tc.raw, usage)
			}
		}
	}
}

func TestOpenAICompatCacheAliasesAndLegacySessionReplay(t *testing.T) {
	for _, tc := range []struct {
		name, aliases     string
		live, history     int
		cacheReadReported bool
	}{
		{"cached_tokens_zero", `,"cached_tokens":0,"prompt_cache_hit_tokens":128`, 128, 128, true},
		{"prompt_cache_hit_tokens_zero", `,"cached_tokens":128,"prompt_cache_hit_tokens":0`, 128, 128, true},
		{"nested_alias_nonzero", `,"prompt_cache_hit_tokens":0,"prompt_tokens_details":{"cached_tokens":128}`, 128, 128, true},
		{"only_cached_tokens_zero", `,"cached_tokens":0`, 0, 0, true},
		{"only_prompt_cache_hit_tokens_zero", `,"prompt_cache_hit_tokens":0`, 0, 0, true},
		{"missing", "", 0, 0, false},
		{"null", `,"cached_tokens":null,"prompt_cache_hit_tokens":null`, 0, 0, false},
		// Both readers retain their own legacy precedence between nonzero aliases.
		{"both_nonzero", `,"cached_tokens":64,"prompt_cache_hit_tokens":128`, 128, 64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageJSON := `{"prompt_tokens":1024,"completion_tokens":20,"total_tokens":1044` + tc.aliases + `}`
			fixture := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"model":"fixture","choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":%s}`, usageJSON)
			}))
			llm, err := New(Config{
				API: model.APIOpenAICompatible, Provider: "custom", BaseURL: fixture.URL, Model: "fixture",
				Auth: AuthConfig{Type: model.AuthAPIKey, Token: "fixture"}, HTTPClient: fixture.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			live, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "question")}})
			if err != nil {
				t.Fatal(err)
			}

			var rawUsage map[string]any
			if err := json.Unmarshal([]byte(usageJSON), &rawUsage); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			config := sessionfile.Config{RootDir: t.TempDir()}
			store := sdkctx.UseLogicalFileStore(t, sessionfile.NewStore(config))
			active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
				Type: session.EventTypeAssistant, Message: &live.Message, Meta: map[string]any{"usage": rawUsage},
			}})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := sdkctx.UseLogicalFileStore(t, sessionfile.NewStore(config)).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil || len(loaded.Events) != 1 {
				t.Fatalf("load legacy events: count=%d err=%v", len(loaded.Events), err)
			}
			history := session.UsageSnapshotFromSessionEvent(loaded.Events[0])
			if history == nil {
				t.Fatal("missing usage after reopening the legacy Session")
			}
			if live.Usage.CachedInputTokens != tc.live || live.Usage.CachedInputTokensReported != tc.cacheReadReported || live.Usage.TotalTokens != 1044 {
				t.Errorf("live usage = %+v, want cache read %d, reported %t, total 1044", live.Usage, tc.live, tc.cacheReadReported)
			}
			if history.CachedInputTokens != tc.history || history.CachedInputTokensReported != tc.cacheReadReported || history.TotalTokens != 1044 {
				t.Errorf("legacy usage = %+v, want cache read %d, reported %t, total 1044", history, tc.history, tc.cacheReadReported)
			}
		})
	}
}
