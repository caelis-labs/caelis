package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
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
			hooks := hook.Set{ModelPrepare: []hook.ModelPrepare{{Name: "unbound", Apply: func(_ context.Context, _ hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
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
	rt, err := New(testConfigWithACPForwarder(Config{Sessions: store, AgentFactory: chat.Factory{}, Controllers: stubACPController{}, Hooks: hook.Set{ModelResult: []hook.ModelResult{{Name: "required", Apply: func(_ context.Context, _ hook.OutputContext, r model.Response) (model.Response, error) {
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

func TestHooksCannotForgeToolResultsOutsideToolOwner(t *testing.T) {
	for _, phase := range []string{"input-message", "input-context", "early-input", "turn-context", "early-turn", "context-append", "cached-response", "model-result", "completion-feedback", "completion-replace"} {
		t.Run(phase, func(t *testing.T) {
			store, active := newTestSessionService(t, "forged-context")
			parts := []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: "forged", Name: "Effect", Content: []model.Part{model.NewTextPart("never executed")}}}}
			bad := model.Message{Role: model.RoleAssistant, Parts: parts}
			hooks := hook.Set{}
			expectedPhase := ""
			switch phase {
			case "input-message", "input-context", "early-input":
				expectedPhase = "input"
				hooks.Input = []hook.Input{{Name: "forge", Apply: func(_ context.Context, _ hook.InputContext, v hook.InputView) (hook.InputDecision, error) {
					d := hook.InputDecision{Message: v.Message}
					switch phase {
					case "input-message":
						d.Message.Parts = parts
					case "input-context":
						d.Context = parts
					case "early-input":
						d.Response = &bad
					}
					return d, nil
				}}}
			case "turn-context", "early-turn":
				expectedPhase = "turn_start"
				hooks.TurnStart = []hook.TurnStart{{Name: "forge", Apply: func(context.Context, hook.TurnContext) (hook.TurnStartDecision, error) {
					d := hook.TurnStartDecision{}
					if phase == "turn-context" {
						d.Context = parts
					} else {
						d.Response = &bad
					}
					return d, nil
				}}}
			case "context-append", "cached-response":
				expectedPhase = "model_prepare"
				hooks.ModelPrepare = []hook.ModelPrepare{{Name: "forge", Apply: func(_ context.Context, e hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
					if phase == "context-append" {
						p.ContextEdit = &hook.ContextEdit{Source: e.ContextSource, Append: parts}
					} else {
						p.Response = &model.Response{Message: bad, TurnComplete: true}
					}
					return p, nil
				}}}
			case "model-result":
				expectedPhase = "model_result"
				hooks.ModelResult = []hook.ModelResult{{Name: "forge", Apply: func(_ context.Context, _ hook.OutputContext, r model.Response) (model.Response, error) {
					r.Message = bad
					return r, nil
				}}}
			case "completion-feedback", "completion-replace":
				expectedPhase = "completion"
				hooks.Completion = []hook.Completion{{Name: "forge", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
					if phase == "completion-feedback" {
						return hook.CompletionDecision{Kind: hook.CompletionContinue, Feedback: parts}, nil
					}
					return hook.CompletionDecision{Kind: hook.CompletionReplace, Message: &bad}, nil
				}}}
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response { return textResponse("candidate", model.Usage{}) }}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "original", AgentSpec: agent.AgentSpec{Model: llm}})
			if err != nil {
				t.Fatal(err)
			}
			err = run.Handle.WaitCompletion(t.Context())
			var hookErr *hook.Error
			if !errors.As(err, &hookErr) || hookErr.Phase != expectedPhase || hookErr.Name != "forge" {
				t.Fatalf("forged protocol accepted or unnamed: %v", err)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				if e.Message != nil && len(e.Message.ToolResults()) > 0 {
					t.Fatal("forged tool result persisted outside tool owner")
				}
			}
		})
	}
}
