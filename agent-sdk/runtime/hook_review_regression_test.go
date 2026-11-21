package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// A durable edit must remain the active context after the first model step,
// including when a real tool causes a second model step in the same Run.
func TestHooksContextEditSurvivesToolRound(t *testing.T) {
	for _, feedback := range []bool{false, true} {
		for _, mode := range []string{"append", "replace", "empty-replacement"} {
			t.Run(fmt.Sprintf("%s/feedback=%v", mode, feedback), func(t *testing.T) {
				root := t.TempDir()
				store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "review", UserID: "context"})
				if err != nil {
					t.Fatal(err)
				}
				var requests [][]model.Message
				driver := scriptedTestModel{generate: func(_ context.Context, r *model.Request) *model.Response {
					requests = append(requests, model.CloneMessages(r.Messages))
					if !feedback && len(requests) <= 2 {
						return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: fmt.Sprintf("read-%d", len(requests)), Name: "Read", Args: `{}`}}, ""), TurnComplete: true}
					}
					return textResponse("done", model.Usage{})
				}}
				var prepares atomic.Int64
				hooks := agent.Hooks{ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "once", Apply: func(_ context.Context, e agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
					if prepares.Add(1) == 1 {
						edit := &agent.ContextEdit{Source: e.ContextSource}
						switch mode {
						case "append":
							edit.Append = []model.Part{model.NewTextPart("EDIT-MARKER")}
						case "replace":
							edit.ReplacePrefix = []model.Message{model.NewTextMessage(model.RoleUser, "EDIT-MARKER")}
						default:
							edit.ReplacePrefix = []model.Message{}
						}
						p.ContextEdit = edit
					}
					return p, nil
				}}}}
				if feedback {
					hooks.Completion = []agent.Hook[model.Message, agent.CompletionDecision]{{Name: "feedback", Apply: func(context.Context, agent.HookContext, model.Message) (agent.CompletionDecision, error) {
						if len(requests) == 1 {
							return agent.CompletionDecision{Kind: agent.CompletionContinue, Feedback: []model.Part{model.NewTextPart("FEEDBACK")}}, nil
						}
						return agent.CompletionDecision{Kind: agent.CompletionAccept}, nil
					}}}
				}
				target := tool.NamedTool{Def: tool.Definition{Name: "Read"}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
					return tool.Result{Content: []tool.ContentBlock{tool.Text("read once")}}, nil
				}}
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
				if err != nil {
					t.Fatal(err)
				}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "ORIGINAL-INPUT", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target}}})
				if err != nil {
					t.Fatal(err)
				}
				if err = run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
				wantRequests := 3
				if feedback {
					wantRequests = 2
				}
				if len(requests) != wantRequests {
					t.Fatalf("requests=%d", len(requests))
				}
				for i, req := range requests {
					var text []string
					for _, m := range req {
						text = append(text, m.TextContent())
					}
					t.Logf("request%d=%q", i+1, text)
				}
				first, second := requests[0], requests[1]
				delta := 2
				if feedback {
					delta = 1
				}
				if len(second) != len(first)+delta || len(first) > 0 && !reflect.DeepEqual(first, second[:len(first)]) {
					t.Errorf("committed edit lost between provider requests: first=%#v second=%#v", first, second)
				}
				all := ""
				for _, m := range second {
					all += m.TextContent() + "|"
				}
				if mode == "append" && !strings.Contains(all, "EDIT-MARKER") {
					t.Error("appended context absent from second request")
				}
				if mode != "append" && strings.Contains(all, "ORIGINAL-INPUT") {
					t.Error("replaced original input resurrected in second request")
				}
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				var edits int
				for _, e := range loaded.Events {
					if e.ContextEdit != nil || e.Type == session.EventTypeCompact {
						edits++
					}
				}
				projected := compact.PromptEventsFromLatestCompact(loaded.Events)
				var replay []model.Message
				for _, e := range projected {
					if e.Message != nil {
						replay = append(replay, model.CloneMessage(*e.Message))
					}
				}
				last := requests[len(requests)-1]
				if len(replay) != len(last)+1 || !reflect.DeepEqual(last, replay[:len(last)]) {
					t.Fatalf("live/replay diverged: live=%+v replay=%+v", last, replay)
				}
				if edits != 1 {
					t.Fatalf("committed edits=%d", edits)
				}
			})
		}
	}

}

func TestHooksSyntheticResponseStreamTransform(t *testing.T) {
	for _, branch := range []string{"cache", "input-early", "turn-early"} {
		t.Run(branch, func(t *testing.T) {
			store, active := newTestSessionService(t, "review-redaction")
			var providers, factories atomic.Int64
			driver := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				providers.Add(1)
				return textResponse("secret", model.Usage{})
			}}
			h := agent.Hooks{Stream: []agent.Hook[struct{}, agent.StreamTransformer]{{Name: "redact", Apply: func(context.Context, agent.HookContext, struct{}) (agent.StreamTransformer, error) {
				factories.Add(1)
				return &tokenRedactor{}, nil
			}}}}
			switch branch {
			case "cache":
				h.ModelPrepare = []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "cache", Apply: func(_ context.Context, _ agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
					p.Response = textResponse("secret", model.Usage{})
					return p, nil
				}}}
			case "input-early":
				h.Input = []agent.Hook[agent.InputView, agent.InputDecision]{{Name: "early", Apply: func(_ context.Context, _ agent.HookContext, v agent.InputView) (agent.InputDecision, error) {
					m := model.NewTextMessage(model.RoleAssistant, "secret")
					return agent.InputDecision{Message: v.Message, Response: &m}, nil
				}}}
			case "turn-early":
				h.TurnStart = []agent.Hook[struct{}, agent.TurnStartDecision]{{Name: "early", Apply: func(context.Context, agent.HookContext, struct{}) (agent.TurnStartDecision, error) {
					m := model.NewTextMessage(model.RoleAssistant, "secret")
					return agent.TurnStartDecision{Response: &m}, nil
				}}}
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: driver}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				if e.Type == session.EventTypeAssistant {
					t.Logf("branch=%s factory=%d provider=%d final=%q", branch, factories.Load(), providers.Load(), e.Message.TextContent())
					if got := e.Message.TextContent(); got != "[redacted]" {
						t.Errorf("untransformed final %q", got)
					}
				}
			}
			if providers.Load() != 0 {
				t.Fatal(fmt.Sprint("unexpected provider: ", providers.Load()))
			}
		})
	}
}

// Around postprocessing has the same safe-failure obligation as ToolResult.
// Run both routes through actual Runtime, and use Goja/FileStore for children.
func TestHooksInvokePostFailureDoesNotPersistRaw(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested=%v", nested), func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "review", UserID: "postfail"})
			if err != nil {
				t.Fatal(err)
			}
			var effects atomic.Int64
			probe := tool.NamedTool{Def: tool.Definition{Name: "ReadSecret"}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{Content: []tool.ContentBlock{tool.Text("SECRET-RAW-RESULT")}}, nil
			}}
			h := agent.Hooks{ToolInvoke: []agent.ToolInvokeHook{{Name: "redaction", Invoke: func(ctx context.Context, e agent.HookContext, next agent.ToolNext) (tool.Result, error) {
				r, err := next(ctx)
				if err != nil {
					return r, err
				}
				if e.Tool.Name == "ReadSecret" {
					return tool.Result{}, fmt.Errorf("redactor failed")
				}
				return r, nil
			}}}}
			var requests atomic.Int64
			driver := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				if requests.Add(1) == 1 {
					c := model.ToolCall{ID: "once", Name: "ReadSecret", Args: `{}`}
					if nested {
						c.Name = "js_exec"
						c.Args = `{"code":"await tools.call('ReadSecret',{});return 1;"}`
					}
					return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{c}, ""), TurnComplete: true}
				}
				return textResponse("done", model.Usage{})
			}}
			targets := []tool.Tool{probe}
			if nested {
				targets = append(targets, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
			}
			rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: h})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: driver, Tools: targets}})
			if err != nil {
				t.Fatal(err)
			}
			completionErr := run.Handle.WaitCompletion(t.Context())
			t.Logf("completion=%v effects=%d", completionErr, effects.Load())
			if effects.Load() != 1 {
				t.Fatalf("effects=%d", effects.Load())
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			pairs, err := session.PairToolFacts(events)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range pairs {
				if p.Call.Tool.Name == "ReadSecret" {
					if p.Result == nil {
						t.Fatal("missing result")
					}
					t.Logf("outcome=%s processing=%+v", p.Result.Tool.Outcome, p.Result.Tool.ProcessingFailure)
				}
			}
			raw, err := json.Marshal(events)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "SECRET-RAW-RESULT") {
				t.Error("failed around redaction fell back to raw durable result")
			}
		})
	}
}

func TestHooksTypedSchemaCloneCannotDefeatBindingCheck(t *testing.T) {
	store, active := newTestSessionService(t, "schema-alias")
	var providers atomic.Int64
	probe := tool.NamedTool{Def: tool.Definition{Name: "Read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"real": map[string]any{"type": "string"}}, "required": []string{"real"}}}}
	h := agent.Hooks{ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "unbound-schema", Apply: func(_ context.Context, _ agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
		// Mutate the detached hook input using the SDK's supported JSON value type.
		p.Request.Tools[0].Function.Parameters["required"].([]string)[0] = "invented"
		return p, nil
	}}}}
	driver := scriptedTestModel{generate: func(_ context.Context, r *model.Request) *model.Response {
		providers.Add(1)
		t.Logf("provider required=%v real target required=%v", r.Tools[0].Function.Parameters["required"], probe.Definition().InputSchema["required"])
		return textResponse("done", model.Usage{})
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe}}})
	if err != nil {
		t.Fatal(err)
	}
	err = run.Handle.WaitCompletion(t.Context())
	t.Logf("completion=%v providers=%d", err, providers.Load())
	if err == nil || providers.Load() != 0 {
		t.Error("detached schema mutation was accepted by configured-binding check")
	}
}

type hookBudgetCompactor struct{ calls atomic.Int64 }

func (*hookBudgetCompactor) Prepare(_ context.Context, r compact.Request) (compact.Result, error) {
	return compact.Result{PromptEvents: compact.PromptEventsFromLatestCompact(r.Events), Usage: compact.UsageSnapshot{TotalTokens: 1, ContextWindowTokens: 32000, EffectiveInputBudget: 30000}}, nil
}
func (c *hookBudgetCompactor) CompactOnOverflow(_ context.Context, r compact.Request, _ error) (compact.Result, error) {
	revision := c.calls.Add(1)
	data := session.CheckpointData{Revision: int(revision), ContractVersion: compact.CompactContractVersion, SummarizedThroughID: lastEventID(r.Events), SummarizedThroughSeq: session.LastEventSeq(r.Events), SourceEventCount: len(r.Events), Generator: "review", Trigger: "overflow"}
	e := buildCompactEvent(r.Session, "prior history summarized", data)
	return compact.Result{Compacted: true, CompactEvent: e, PromptEvents: compact.PromptEventsFromLatestCompact([]*session.Event{e})}, nil
}
func TestHooksCompletionBudgetSurvivesRuntimeRebuild(t *testing.T) {
	for _, overflows := range []int{1, 2} {
		t.Run(fmt.Sprint(overflows), func(t *testing.T) {
			store, active := newTestSessionService(t, "completion-rebuild")
			prior := model.NewTextMessage(model.RoleUser, "prior history to compact")
			if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeUser, Message: &prior}}); err != nil {
				t.Fatal(err)
			}
			var completions atomic.Int64
			driver := &hookStreamModel{emit: func(i int, yield func(*model.StreamEvent, error) bool) {
				if i%2 == 0 && i <= 2*overflows {
					yield(nil, &model.ContextOverflowError{Cause: fmt.Errorf("forced provider overflow")})
					return
				}
				yield(model.StreamEventFromResponse(textResponse("candidate", model.Usage{})), nil)
			}}
			compactor := &hookBudgetCompactor{}
			h := agent.Hooks{CompletionLimit: 2, Completion: []agent.Hook[model.Message, agent.CompletionDecision]{{Name: "continue", Apply: func(context.Context, agent.HookContext, model.Message) (agent.CompletionDecision, error) {
				completions.Add(1)
				return agent.CompletionDecision{Kind: agent.CompletionContinue, Feedback: []model.Part{model.NewTextPart("try again")}}, nil
			}}}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h, Compactor: compactor, Compaction: CompactionConfig{Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: driver}})
			if err != nil {
				t.Fatal(err)
			}
			err = run.Handle.WaitCompletion(t.Context())
			events, loadErr := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			feedback := 0
			for _, e := range events {
				if e.Type == session.EventTypeContext && strings.HasPrefix(e.Actor.Name, "hook/completion/") {
					feedback++
				}
			}
			t.Logf("completion=%v compact=%d completions=%d provider=%d feedback=%d", err, compactor.calls.Load(), completions.Load(), driver.calls.Load(), feedback)
			if compactor.calls.Load() != int64(overflows) {
				t.Fatal("did not exercise Runtime reconstruction")
			}
			if feedback != 2 {
				t.Errorf("global CompletionLimit=2 admitted %d feedback commits in one Run", feedback)
			}
		})
	}
}

type hookResultAckStore struct {
	*sessionfile.Store
	writes     atomic.Int64
	originalID string
}

func (s *hookResultAckStore) AppendEvent(ctx context.Context, r session.AppendEventRequest) (*session.Event, error) {
	if r.Event != nil && r.Event.Type == session.EventTypeToolResult && r.Event.Tool != nil && r.Event.Tool.Name == "WriteOnce" {
		s.writes.Add(1)
		s.originalID = r.Event.ID
		if _, err := s.Store.AppendEvent(ctx, r); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("review: result committed but ACK lost")
	}
	return s.Store.AppendEvent(ctx, r)
}
func TestHooksConcurrentNextAndResultReadback(t *testing.T) {
	root := t.TempDir()
	store := &hookResultAckStore{Store: sessionfile.NewStore(sessionfile.Config{RootDir: root})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "review", UserID: "next"})
	if err != nil {
		t.Fatal(err)
	}
	var effects, accepted atomic.Int64
	var retained agent.ToolNext
	target := tool.NamedTool{Def: tool.Definition{Name: "WriteOnce"}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{Content: []tool.ContentBlock{tool.Text("known result")}}, nil
	}}
	h := agent.Hooks{ToolInvoke: []agent.ToolInvokeHook{{Name: "concurrent", Invoke: func(ctx context.Context, _ agent.HookContext, next agent.ToolNext) (tool.Result, error) {
		retained = next
		results := make(chan tool.Result, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				r, e := next(ctx)
				if e == nil {
					accepted.Add(1)
					results <- r
				}
			})
		}
		wg.Wait()
		close(results)
		for r := range results {
			return r, nil
		}
		return tool.Result{}, fmt.Errorf("all next rejected")
	}}}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "once", Name: "WriteOnce", Args: `{}`}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target}}})
	if err != nil {
		t.Fatal(err)
	}
	completion := run.Handle.WaitCompletion(t.Context())
	if completion == nil {
		t.Fatal("duplicate continuation violation suppressed")
	}
	if retained == nil {
		t.Fatal("hook not invoked")
	}
	if _, err = retained(t.Context()); err == nil {
		t.Fatal("retained next remained usable")
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil {
		t.Fatalf("pairs=%v err=%v", pairs, err)
	}
	if effects.Load() != 1 || accepted.Load() != 1 || store.writes.Load() != 1 || store.originalID != pairs[0].Result.ID {
		t.Fatalf("effects=%d next=%d writes=%d requested=%s persisted=%s", effects.Load(), accepted.Load(), store.writes.Load(), store.originalID, pairs[0].Result.ID)
	}
	t.Logf("one effect; one successful next; one result append; original ID read back: %s; completion=%v", store.originalID, completion)
}

func TestHooksContextEditRecoveryUsesCurrentProjection(t *testing.T) {
	for _, mode := range []string{"append", "replace", "empty"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "recovery"})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			h := agent.Hooks{ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "edit", Apply: func(_ context.Context, e agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
				count++
				if count == 1 {
					edit := &agent.ContextEdit{Source: e.ContextSource}
					switch mode {
					case "append":
						edit.Append = []model.Part{model.NewTextPart("EDIT")}
					case "replace":
						edit.ReplacePrefix = []model.Message{model.NewTextMessage(model.RoleUser, "EDIT")}
					default:
						edit.ReplacePrefix = []model.Message{}
					}
					p.ContextEdit = edit
				}
				return p, nil
			}}}}
			driver := &hookStreamModel{observe: func(req *model.Request) {
				loaded, e := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if e != nil {
					t.Fatal(e)
				}
				var replay []model.Message
				for _, event := range compact.PromptEventsFromLatestCompact(loaded.Events) {
					if event.Message != nil {
						replay = append(replay, model.CloneMessage(*event.Message))
					} else if event.Type == session.EventTypeCompact && session.EventText(event) != "" {
						replay = append(replay, model.NewTextMessage(model.RoleUser, session.EventText(event)))
					}
				}
				if !reflect.DeepEqual(req.Messages, replay) {
					t.Errorf("recovery live/replay mismatch: live=%+v replay=%+v", req.Messages, replay)
				}
			}, emit: func(i int, yield func(*model.StreamEvent, error) bool) {
				switch i {
				case 1:
					yield(model.StreamEventFromResponse(&model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "read", Name: "Read", Args: `{}`}}, ""), TurnComplete: true}), nil)
				case 2:
					yield(nil, &model.ContextOverflowError{Cause: fmt.Errorf("provider overflow")})
				default:
					yield(model.StreamEventFromResponse(textResponse("done", model.Usage{})), nil)
				}
			}}
			target := tool.NamedTool{Def: tool.Definition{Name: "Read"}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
				return tool.Result{Content: []tool.ContentBlock{tool.Text("read")}}, nil
			}}
			compactor := &hookBudgetCompactor{}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h, Compactor: compactor, Compaction: CompactionConfig{Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "ORIGINAL", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			if driver.calls.Load() != 3 || compactor.calls.Load() != 1 {
				t.Fatalf("recovery not exercised: provider=%d compact=%d", driver.calls.Load(), compactor.calls.Load())
			}
		})
	}
}
