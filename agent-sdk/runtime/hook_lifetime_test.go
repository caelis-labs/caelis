package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

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
			hook := func(name string) agent.ModelInvokeHook {
				return agent.ModelInvokeHook{Name: name, Invoke: func(ctx context.Context, e agent.HookContext, req *model.Request, next agent.ModelNext) error {
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
						return &agent.RetryRequest{Cause: err}
					}
					return err
				}}
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: agent.Hooks{ModelInvoke: []agent.ModelInvokeHook{hook("outer"), hook("inner")}}})
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
		return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart("known completion")}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "write", Name: "Write", Args: "{}"}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: agent.Hooks{ToolInvoke: []agent.ToolInvokeHook{{Name: "deadline", Timeout: 10 * time.Millisecond, Invoke: func(ctx context.Context, _ agent.HookContext, next agent.ToolNext) (tool.Result, error) {
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
	for _, kind := range []agent.CompletionKind{agent.CompletionReject, agent.CompletionContinue, agent.CompletionReplace} {
		t.Run(string(kind), func(t *testing.T) {
			store, active := newTestSessionService(t, "late-hook")
			entered, release := make(chan struct{}), make(chan struct{})
			var inputs, completions, providers atomic.Int64
			driver := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				return textResponse(fmt.Sprintf("candidate %d", providers.Add(1)), model.Usage{})
			}}
			hooks := agent.Hooks{Input: []agent.Hook[agent.InputView, agent.InputDecision]{{Name: "once", Apply: func(_ context.Context, _ agent.HookContext, v agent.InputView) (agent.InputDecision, error) {
				inputs.Add(1)
				return agent.InputDecision{Message: v.Message}, nil
			}}},
				Completion: []agent.Hook[model.Message, agent.CompletionDecision]{{Name: "finish", Apply: func(context.Context, agent.HookContext, model.Message) (agent.CompletionDecision, error) {
					if completions.Add(1) > 1 {
						return agent.CompletionDecision{Kind: agent.CompletionAccept}, nil
					}
					close(entered)
					<-release
					d := agent.CompletionDecision{Kind: kind}
					if kind == agent.CompletionContinue {
						d.Feedback = []model.Part{model.NewTextPart("obsolete feedback")}
					}
					if kind == agent.CompletionReplace {
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
