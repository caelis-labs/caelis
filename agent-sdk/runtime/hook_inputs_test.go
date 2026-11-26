package runtime

import (
	"context"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInputHooksEarlyCompletionFileReopen(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "early"})
	if err != nil {
		t.Fatal(err)
	}
	var inputCalls, turnCalls, providerCalls, completeCalls atomic.Int64
	hooks := hook.Set{
		Input: []hook.Input{{Name: "normalize", Apply: func(ctx context.Context, e hook.InputContext, v hook.InputView) (hook.InputDecision, error) {
			inputCalls.Add(1)
			if e.RunID == "" || e.TurnID == "" {
				t.Error("missing trusted identity")
			}
			m := model.NewTextMessage(model.RoleAssistant, "early")
			v.Message.Parts[0].Text.Text = "effective"
			return hook.InputDecision{Message: v.Message, Response: &m}, nil
		}}},
		TurnStart: []hook.TurnStart{{Name: "start", Apply: func(context.Context, hook.TurnContext) (hook.TurnStartDecision, error) {
			turnCalls.Add(1)
			return hook.TurnStartDecision{}, nil
		}}},
		Completion: []hook.Completion{{Name: "review", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
			completeCalls.Add(1)
			m := model.NewTextMessage(model.RoleAssistant, "accepted")
			return hook.CompletionDecision{Kind: hook.CompletionReplace, Message: &m}, nil
		}}},
	}
	llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
		providerCalls.Add(1)
		return textResponse("unreachable", model.Usage{})
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "original user authority", AgentSpec: agent.AgentSpec{Name: "chat", Model: llm}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live.Events, reopened.Events) {
		t.Fatal("full events changed on reopen")
	}
	var user, final *session.Event
	for _, e := range reopened.Events {
		if e.Type == session.EventTypeUser {
			user = e
		}
		if e.Type == session.EventTypeAssistant {
			final = e
		}
	}
	if user == nil || user.Message.TextContent() != "effective" || session.OriginalInputMessage(user).TextContent() != "original user authority" || final == nil || final.Message.TextContent() != "accepted" {
		t.Fatalf("user=%#v final=%#v", user, final)
	}
	if inputCalls.Load() != 1 || turnCalls.Load() != 1 || providerCalls.Load() != 0 || completeCalls.Load() != 1 {
		t.Fatalf("calls input=%d turn=%d provider=%d completion=%d", inputCalls.Load(), turnCalls.Load(), providerCalls.Load(), completeCalls.Load())
	}
}

func TestCompletionFeedbackHasSharedBoundAndNoRejectedFinal(t *testing.T) {
	store, active := newTestSessionService(t, "completion-bound")
	var calls atomic.Int64
	llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
		calls.Add(1)
		return textResponse("rejected candidate", model.Usage{})
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{Completion: []hook.Completion{{Name: "continue", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
		return hook.CompletionDecision{Kind: hook.CompletionContinue, Feedback: []model.Part{model.NewTextPart("try again")}}, nil
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Name: "chat", Model: llm}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err == nil || !strings.Contains(err.Error(), "completion_limit") {
		t.Fatalf("error=%v", err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	feedback := 0
	for _, e := range loaded.Events {
		if e.Type == session.EventTypeAssistant {
			t.Fatal("rejected final committed")
		}
		if e.Type == session.EventTypeContext {
			feedback++
			if e.Actor.Kind != session.ActorKindSystem {
				t.Fatal("feedback became user authority")
			}
		}
	}
	if calls.Load() != 3 || feedback != 2 {
		t.Fatalf("model=%d feedback=%d", calls.Load(), feedback)
	}
}

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

func TestHooksTypedSchemaLateMutationCannotChangeBinding(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "in-place", true: "after-timeout"}[late], func(t *testing.T) {
			store, active := newTestSessionService(t, "typed-binding")
			schema := map[string]any{"type": "object", "properties": map[string]any{"real": map[string]any{"type": "string"}}, "required": []string{"real"}, "x-groups": map[string][]map[string][]string{"items": {{"required": {"real"}}}}, "x-array": [1]map[string][]string{{"required": {"real"}}}}
			target := tool.NamedTool{Def: tool.Definition{Name: "Read", InputSchema: schema}}
			baseline := tool.CloneDefinition(target.Definition())
			release, finished := make(chan struct{}), make(chan struct{})
			var providers atomic.Int64
			h := hook.Set{ModelPrepare: []hook.ModelPrepare{{Name: "typed-mutation", Timeout: 10 * time.Millisecond, Apply: func(_ context.Context, _ hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
				defer close(finished)
				if late {
					<-release
				}
				s := p.Request.Tools[0].Function.Parameters
				s["required"].([]string)[0] = "invented"
				s["x-groups"].(map[string][]map[string][]string)["items"][0]["required"][0] = "invented"
				s["x-array"].([1]map[string][]string)[0]["required"][0] = "invented"
				return p, nil
			}}}}
			llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				providers.Add(1)
				return textResponse("unreachable", model.Usage{})
			}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{target}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err == nil {
				t.Fatal("unbound mutation accepted")
			}
			if late {
				close(release)
			}
			<-finished
			if providers.Load() != 0 || !reflect.DeepEqual(target.Definition(), baseline) {
				t.Fatal("detached or late schema mutation changed callable or reached provider")
			}
		})
	}
}

func TestHooksModelInvokeRetryHasDistinctActualReceipts(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			store, active := newTestSessionService(t, "hook-attempt")
			driver := &hookStreamModel{emit: func(i int, yield func(*model.StreamEvent, error) bool) {
				if i == 1 {
					yield(nil, hookRetryError{})
					return
				}
				yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "accepted"), TurnComplete: true}), nil)
			}}
			var ids []string
			var order []string
			var logical string
			middleware := func(name string) hook.ModelInvoke {
				return hook.ModelInvoke{Name: name, Apply: func(ctx context.Context, e hook.AttemptContext, req *model.Request, next hook.ModelNext) error {
					if e.AssemblyRevision != 1 || e.InvocationID == "" {
						t.Error("attempt lost assembly/actual identity")
					}
					if logical == "" {
						logical = e.LogicalRequestID
					} else if logical != e.LogicalRequestID {
						t.Error("transport retry changed logical request")
					}
					if name == "outer" {
						ids = append(ids, e.InvocationID)
					}
					order = append(order, name+" before")
					req.Messages = nil // detached request cannot change the admitted request.
					err := next(ctx)
					order = append(order, name+" after")
					if explicit && name == "inner" && err != nil {
						return &hook.RetryRequest{Cause: err}
					}
					return err
				}}
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ModelInvoke: []hook.ModelInvoke{middleware("outer"), middleware("inner")}}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "original", AgentSpec: agent.AgentSpec{Model: model.WithRetry(driver, model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			if driver.calls.Load() != 2 || len(ids) != 2 || ids[0] == ids[1] {
				t.Fatalf("actual attempts=%d ids=%v", driver.calls.Load(), ids)
			}
			if strings.Join(order, ",") != "outer before,inner before,inner after,outer after,outer before,inner before,inner after,outer after" {
				t.Fatalf("around order=%v", order)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, e := range events {
				if session.IsModelInvocationReceipt(e) {
					receipts++
				}
			}
			if receipts != 2 {
				t.Fatalf("actual receipts=%d", receipts)
			}
		})
	}
}

func TestHooksTimedOutProducerRetainsFenceUntilActualDrain(t *testing.T) {
	store, active := newTestSessionService(t, "hook-drain")
	expired, release := make(chan struct{}), make(chan struct{})
	var effects atomic.Int64
	target := tool.NamedTool{Def: tool.Definition{Name: "Write"}, Invoke: func(ctx context.Context, c tool.Call) (tool.Result, error) {
		effects.Add(1)
		<-ctx.Done()
		close(expired)
		<-release
		return tool.Result{Content: []tool.ContentBlock{tool.Text("known completion")}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "write", Name: "Write", Args: "{}"}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolInvoke: []hook.ToolInvoke{{Name: "deadline", Timeout: 10 * time.Millisecond, Apply: func(ctx context.Context, _ hook.ToolContext, next hook.ToolNext) (tool.Result, error) {
		return next(ctx)
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target}}})
	if err != nil {
		t.Fatal(err)
	}
	<-expired
	wait, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := run.Handle.WaitCompletion(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("declared live producer stopped: %v", err)
	}
	if _, err := store.(session.SessionFenceService).AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "competing"}); err == nil {
		t.Fatal("released live producer fence")
	}
	close(release)
	if err := run.Handle.WaitCompletion(t.Context()); err == nil {
		t.Fatal("hook timeout hidden")
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil {
		t.Fatalf("result pairing=%v err=%v", pairs, err)
	}
	result := pairs[0].Result.Tool
	if effects.Load() != 1 || result.Outcome != session.ToolOutcomeSucceeded || result.ProcessingFailure == nil {
		t.Fatalf("known effect changed: effects=%d result=%+v", effects.Load(), result)
	}
	fence, err := store.(session.SessionFenceService).AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "after-drain"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.(session.SessionFenceService).ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
}

func TestHooksLateInputInvalidatesCompletionDecision(t *testing.T) {
	for _, kind := range []hook.CompletionKind{hook.CompletionReject, hook.CompletionContinue, hook.CompletionReplace} {
		t.Run(string(kind), func(t *testing.T) {
			store, active := newTestSessionService(t, "late-hook")
			entered, release := make(chan struct{}), make(chan struct{})
			var inputs, completions, providers atomic.Int64
			driver := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				return textResponse(fmt.Sprintf("candidate %d", providers.Add(1)), model.Usage{})
			}}
			hooks := hook.Set{Input: []hook.Input{{Name: "once", Apply: func(_ context.Context, _ hook.InputContext, v hook.InputView) (hook.InputDecision, error) {
				inputs.Add(1)
				return hook.InputDecision{Message: v.Message}, nil
			}}},
				Completion: []hook.Completion{{Name: "finish", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
					if completions.Add(1) > 1 {
						return hook.CompletionDecision{Kind: hook.CompletionAccept}, nil
					}
					close(entered)
					<-release
					d := hook.CompletionDecision{Kind: kind}
					if kind == hook.CompletionContinue {
						d.Feedback = []model.Part{model.NewTextPart("obsolete feedback")}
					}
					if kind == hook.CompletionReplace {
						m := model.NewTextMessage(model.RoleAssistant, "obsolete replacement")
						d.Message = &m
					}
					return d, nil
				}}}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: driver}})
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			if err := run.Handle.Submit(agent.Submission{Kind: agent.SubmissionKindConversation, Text: "late input"}); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			finals := 0
			for _, e := range events {
				if e.Type == session.EventTypeContext {
					t.Fatal("obsolete completion feedback committed")
				}
				if e.Type == session.EventTypeAssistant {
					finals++
					if e.Message.TextContent() != "candidate 2" {
						t.Fatal("obsolete finish decision survived input")
					}
				}
			}
			if finals != 1 || inputs.Load() != 2 || providers.Load() != 2 || completions.Load() != 2 {
				t.Fatalf("finals=%d input=%d model=%d completion=%d", finals, inputs.Load(), providers.Load(), completions.Load())
			}
		})
	}
}
