package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestCodeModeChildJournalRequiresOriginalAdmission(t *testing.T) {
	for _, name := range []string{"missing", "wrong-call", "wrong-name", "wrong-parent", "nil-validator", "valid", "ordinary-parent", "no-parent"} {
		t.Run(name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			call := tool.Call{ID: "child", Name: "Write", Input: []byte(`{}`), Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "step"}, Parent: &tool.ParentCall{Kind: codemode.ToolKind, Name: "arbitrary_script_name", ID: "outer"}}
			var effects, validations atomic.Int32
			binding := &codeModeAdmissionBinding{callID: call.ID, name: call.Name, parent: *call.Parent, validate: func(context.Context) (func(), error) {
				validations.Add(1)
				return func() {}, nil
			}}
			switch name {
			case "wrong-call":
				binding.callID = "another-child"
			case "wrong-name":
				binding.name = "AnotherTool"
			case "wrong-parent":
				binding.parent.ID = "another-parent"
			case "nil-validator":
				binding.validate = nil
			case "ordinary-parent":
				call.Parent = &tool.ParentCall{Name: "js_repl", ID: "ordinary-provenance"}
			case "no-parent":
				call.Parent = nil
			}
			ctx := t.Context()
			if name != "missing" {
				ctx = context.WithValue(ctx, codeModeAdmissionKey{}, binding)
			}
			wrapped := journaledTool{base: tool.NamedTool{Def: tool.Definition{Name: "Write"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}, sessions: store, sessionRef: active.SessionRef, runID: "run", turnID: "turn"}
			_, err := wrapped.Call(ctx, call)
			allowed := name == "valid" || name == "ordinary-parent" || name == "no-parent"
			if allowed != (err == nil) || allowed != (effects.Load() == 1) {
				t.Fatalf("allowed=%v effects=%d error=%v", allowed, effects.Load(), err)
			}
			if name == "valid" && validations.Load() != 1 || name != "valid" && validations.Load() != 0 {
				t.Fatalf("unexpected validation count=%d", validations.Load())
			}
			if !allowed {
				events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range events {
					if event.Journal != nil && event.Journal.ToolExecution != nil {
						t.Fatal("unbound child reached durable execution claim")
					}
				}
			}
		})
	}
}

type codeModeStartBoundaryRuntime struct {
	sandbox.Runtime
	beforeStart func(context.Context) error
}

func (r codeModeStartBoundaryRuntime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if err := r.beforeStart(ctx); err != nil {
		return nil, err
	}
	return r.Runtime.Start(ctx, req)
}

func TestCodeModeNativeContinuationClaimBoundaries(t *testing.T) {
	for _, mode := range []string{"guard", "complete-media", "claimed-start-failure"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			tasks := sessionfile.NewTaskStore(store)
			target := filepath.Join(root, "effect.txt")
			asked, approve := make(chan struct{}), make(chan struct{})
			var revision sync.RWMutex
			var taskID string
			var starts atomic.Int32
			base := hostRuntimeForTest(t, root)
			command := mustRuntimeRunCommandTool(t, codeModeStartBoundaryRuntime{Runtime: base, beforeStart: func(ctx context.Context) error {
				if !revision.TryLock() {
					return errors.New("configuration guard crossed external Start")
				}
				revision.Unlock()
				if err := ctx.Err(); err != nil {
					return err
				}
				starts.Add(1)
				if mode == "claimed-start-failure" {
					return errors.New("injected Start failure with no sandbox session returned")
				}
				return nil
			}})
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
				if input.Tool.Name == shell.RunCommandToolName {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				if !revision.TryLock() {
					return agent.ApprovalResponse{}, errors.New("configuration guard crossed approval wait")
				}
				revision.Unlock()
				if req.OnAdmission == nil {
					return agent.ApprovalResponse{}, errors.New("missing native approval producer")
				}
				if err := req.OnAdmission(ctx); err != nil {
					return agent.ApprovalResponse{}, err
				}
				taskID = req.OperationTaskID
				close(asked)
				<-approve
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			})
			image := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}, "image/png", "fixture.png")
			complete := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{TurnComplete: true, Content: []model.Part{image}}, nil
			}}
			var terminal taskapi.State
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				args, _ := json.Marshal(map[string]any{"command": shellWriteFileForTest(target, "once"), "yield_time_ms": 0})
				if _, err := host.Call(ctx, shell.RunCommandToolName, args); err != nil {
					return nil, err
				}
				<-asked
				var mediaHandle string
				if mode == "complete-media" {
					reply, err := host.Call(ctx, "Complete", []byte(`{}`))
					if err != nil || !reply.TurnComplete || len(reply.Parts) != 1 {
						close(approve)
						return nil, errors.New("completion fixture failed")
					}
					mediaHandle = reply.Parts[0].Handle
				}
				close(approve)
				wait, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				for {
					entry, err := tasks.Get(wait, taskID)
					if err != nil {
						return nil, err
					}
					if taskapi.IsTerminalState(entry.State) {
						terminal = entry.State
						break
					}
					select {
					case <-time.After(10 * time.Millisecond):
					case <-wait.Done():
						return nil, wait.Err()
					}
				}
				if mediaHandle != "" {
					return []byte(`{}`), host.Emit(ctx, mediaHandle)
				}
				return []byte(`{}`), nil
			})}
			llm := codeModeModel(false)
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: codeModeImageModel{llm}, Tools: []tool.Tool{declaration, command, complete}, Validate: func(context.Context) (func(), error) { revision.RLock(); return revision.RUnlock, nil }}, nil
			}}
			rt, err := New(Config{Sessions: store, TaskStore: tasks, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "native continuation boundary", AgentSpec: spec, ApprovalRequester: requester})
			if err != nil {
				t.Fatal(err)
			}
			runErr := run.Handle.WaitCompletion(t.Context())
			if mode != "claimed-start-failure" && runErr != nil {
				t.Fatal(runErr)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			terminals, emitted := 0, false
			for _, event := range events {
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					r := event.Journal.ToolExecution
					if r.ToolName == shell.RunCommandToolName && (r.Status == session.ToolExecutionSucceeded || r.Status == session.ToolExecutionUnknownOutcome || r.Status == session.ToolExecutionFailed) {
						terminals++
					}
				}
				if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Name == "js_repl" && event.Type == session.EventTypeToolResult && event.Message != nil {
					for _, part := range event.Message.Parts[0].ToolResult.Content {
						if part.Media != nil && part.Media.Source.Data == image.Media.Source.Data && event.Tool.TurnComplete {
							emitted = true
						}
					}
				}
			}
			data, fileErr := os.ReadFile(target)
			switch mode {
			case "guard":
				if starts.Load() != 1 || terminal != taskapi.StateCompleted || terminals != 1 || fileErr != nil || string(data) != "once" {
					t.Fatalf("starts=%d task=%s receipts=%d file=%q %v", starts.Load(), terminal, terminals, data, fileErr)
				}
			case "complete-media":
				if starts.Load() != 0 || terminal != taskapi.StateFailed || terminals != 0 || !os.IsNotExist(fileErr) || !emitted || len(llm.requests) != 1 {
					t.Fatalf("starts=%d task=%s receipts=%d emitted=%v requests=%d file=%v", starts.Load(), terminal, terminals, emitted, len(llm.requests), fileErr)
				}
			case "claimed-start-failure":
				if starts.Load() != 1 || terminal != taskapi.StateFailed || terminals != 1 || !os.IsNotExist(fileErr) {
					t.Fatalf("starts=%d task=%s receipts=%d file=%v", starts.Load(), terminal, terminals, fileErr)
				}
			}
		})
	}
}
