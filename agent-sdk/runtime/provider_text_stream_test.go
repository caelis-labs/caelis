package runtime

import (
	"io"
	"net/http"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestProviderIncrementalTextWithReasoning(t *testing.T) {
	for _, tc := range []struct {
		api  model.APIType
		wire string
	}{
		{model.APIOllama, `{"message":{"role":"assistant","thinking":"think"},"done":false}
{"message":{"role":"assistant","content":"hello"},"done":true,"done_reason":"stop"}
`},
		{model.APIOpenAICompatible, `data: {"choices":[{"delta":{"content":"hello"}}]}

data: {"choices":[{"delta":{"reasoning_content":"think"},"finish_reason":"stop"}]}

data: [DONE]

`},
	} {
		t.Run(string(tc.api), func(t *testing.T) {
			transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.wire)), Request: req}, nil
			})
			llm, err := providers.New(providers.Config{Model: "fixture", API: tc.api, BaseURL: "http://fixture.invalid", Auth: providers.AuthConfig{Type: model.AuthNone}, HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			store, active := newTestSessionService(t, "provider-stream")
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{StreamMode: hook.StreamIncremental}})
			if err != nil {
				t.Fatal(err)
			}
			enabled := true
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "hello", AgentSpec: agent.AgentSpec{Model: llm, Request: agent.ModelRequestOptions{Stream: &enabled}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == session.EventTypeAssistant && event.Message != nil {
					if event.Message.TextContent() != "hello" || event.Message.ReasoningText() != "think" {
						t.Fatalf("assistant parts: %+v", event.Message.Parts)
					}
					return
				}
			}
			t.Fatal("missing final assistant message")
		})
	}
}
