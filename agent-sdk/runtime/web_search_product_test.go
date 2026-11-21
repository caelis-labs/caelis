package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

func TestWebSearchUnavailableReachesProviderAndFileHistory(t *testing.T) {
	for _, fixture := range []struct {
		name, baseURL, path, detail, first, final string
		api                                       model.APIType
	}{
		{
			name: "xiaomi", api: model.APIMimo, baseURL: "https://token-plan-cn.xiaomimimo.com/v1",
			path: "/v1/chat/completions", detail: "Token Plan",
			first: `{"model":"fixture","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_search","type":"function","function":{"name":"WebSearch","arguments":"{\"query\":\"release notes\"}"}}]},"finish_reason":"tool_calls"}]}`,
			final: `{"model":"fixture","choices":[{"message":{"role":"assistant","content":"finished"},"finish_reason":"stop"}]}`,
		},
		{
			name: "minimax", api: model.APIMiniMax, baseURL: "https://fixture.invalid",
			path: "/v1/messages", detail: "minimax does not support provider-native web search",
			first: `{"id":"msg-first","type":"message","role":"assistant","model":"fixture","content":[{"type":"tool_use","id":"call_search","name":"WebSearch","input":{"query":"release notes"}}],"stop_reason":"tool_use","usage":{"input_tokens":6,"output_tokens":3}}`,
			final: `{"id":"msg-final","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"finished"}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":3}}`,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			var requests atomic.Int32
			var nextRequest []byte
			transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != fixture.path {
					return nil, fmt.Errorf("unexpected provider dispatch to %s", req.URL.Path)
				}
				var wire string
				switch requests.Add(1) {
				case 1:
					wire = fixture.first
				case 2:
					var err error
					nextRequest, err = io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					wire = fixture.final
				default:
					return nil, fmt.Errorf("unexpected additional provider dispatch")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(wire)), Request: req}, nil
			})
			llm, err := providers.New(providers.Config{
				Provider: fixture.name, API: fixture.api, Model: "fixture", BaseURL: fixture.baseURL,
				Auth:       providers.AuthConfig{Type: model.AuthAPIKey, Token: "fixture"},
				HTTPClient: &http.Client{Transport: transport},
				Retry:      model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond},
			})
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			config := sessionfile.Config{RootDir: root}
			store := sessionfile.NewStore(config)
			active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runtime.Run(ctx, agent.RunRequest{
				SessionRef: active.SessionRef, Input: "Find release notes",
				AgentSpec: agent.AgentSpec{Name: "search", Model: llm, Tools: []tool.Tool{web.NewSearch()}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 || !json.Valid(nextRequest) {
				t.Fatalf("provider requests=%d, next body=%s", requests.Load(), nextRequest)
			}
			reopened := sessionfile.NewStore(config)
			loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			var unavailable string
			for _, event := range loaded.Events {
				msg, ok := session.ModelMessageOf(event)
				if !ok {
					continue
				}
				if result := msg.ToolResponse(); result != nil && result.ID == "call_search" {
					if result.Result["status"] != "unavailable" || result.Result["query"] != "release notes" || result.Result["provider"] != fixture.name {
						t.Fatalf("durable model result = %#v", result)
					}
					unavailable, _ = result.Result["message"].(string)
				}
			}
			for _, want := range []string{fixture.detail, "Use " + web.FetchToolName + " with a known URL"} {
				if !strings.Contains(unavailable, want) || !strings.Contains(string(nextRequest), want) {
					t.Fatalf("missing %q in durable message %q or provider request %s", want, unavailable, nextRequest)
				}
			}
			if strings.Contains(unavailable, "through Caelis") || strings.Contains(unavailable, "use web_fetch") {
				t.Fatalf("adapter fallback escaped into model tool message: %q", unavailable)
			}
		})
	}
}
