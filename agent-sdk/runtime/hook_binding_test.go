package runtime

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestHooksRejectUnboundModelToolSchemaAndVolatileMessages(t *testing.T) {
	for _, mutation := range []string{"model", "tool-schema", "messages"} {
		t.Run(mutation, func(t *testing.T) {
			store, active := newTestSessionService(t, "hook-binding")
			var providers atomic.Int64
			llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				providers.Add(1)
				return textResponse("unreachable", model.Usage{})
			}}
			hooks := agent.Hooks{ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "unbound", Apply: func(_ context.Context, _ agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
				switch mutation {
				case "model":
					p.ModelName = "unconfigured"
				case "tool-schema":
					p.Request.Tools[0].Function.Parameters = map[string]any{"type": "object", "required": []string{"invented"}}
				case "messages":
					p.Request.Messages = []model.Message{model.NewTextMessage(model.RoleUser, "volatile replacement")}
				}
				return p, nil
			}}}}
			probe := tool.NamedTool{Def: tool.Definition{Name: "probe", InputSchema: map[string]any{"type": "object"}}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "original", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{probe}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err == nil {
				t.Fatal("unbound request transformation accepted")
			}
			if providers.Load() != 0 {
				t.Fatal("unbound request reached provider")
			}
		})
	}
}

func TestHooksExternalACPRequiredPhaseFailsBeforeInputMutation(t *testing.T) {
	store, active := newTestSessionService(t, "external-hook")
	active, err := store.BindController(t.Context(), session.BindControllerRequest{SessionRef: active.SessionRef, Binding: session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "remote", EpochID: "epoch", RemoteSessionID: "remote-session"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	rt, err := New(testConfigWithACPForwarder(Config{Sessions: store, AgentFactory: chat.Factory{}, Controllers: stubACPController{}, Hooks: agent.Hooks{ModelResult: []agent.Hook[model.Response, model.Response]{{Name: "required", Apply: func(_ context.Context, _ agent.HookContext, r model.Response) (model.Response, error) {
		calls.Add(1)
		return r, nil
	}}}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "must not commit"})
	if !errorcode.Is(err, errorcode.Unsupported) || calls.Load() != 0 {
		t.Fatalf("external private phase claimed protection: %v", err)
	}
	after, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unsupported phase mutated canonical input")
	}
}
