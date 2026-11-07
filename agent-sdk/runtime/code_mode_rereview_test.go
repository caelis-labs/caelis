package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// Exercise the actual native command approval producer, which yields a Task
// before approval. NamedTool and MCP synchronous approval fixtures do not use it.
func TestRereviewCodeModeAsyncApprovalKeepsFinalHostClaim(t *testing.T) {
	for _, mode := range []string{"allow-control", "generation", "complete"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			target := filepath.Join(root, "effect.txt")
			asked, approve := make(chan struct{}), make(chan struct{})
			tasks := sessionfile.NewTaskStore(store)
			var taskID string // Published before asked is closed.
			var stale atomic.Bool
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
				if input.Tool.Name == shell.RunCommandToolName {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				if req.OnAdmission == nil || req.OperationTaskID == "" || req.Call.Parent == nil {
					return agent.ApprovalResponse{}, errors.New("not the nested Task approval path")
				}
				if err := req.OnAdmission(ctx); err != nil {
					return agent.ApprovalResponse{}, err
				}
				t.Logf("original command call_id=%s step=%s task=%s", req.Call.ID, req.Call.Execution.ItemID, req.OperationTaskID)
				taskID = req.OperationTaskID
				close(asked)
				select {
				case <-approve:
				case <-ctx.Done():
					return agent.ApprovalResponse{}, ctx.Err()
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			})
			command := mustRuntimeRunCommandTool(t, hostRuntimeForTest(t, root))
			complete := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{TurnComplete: true}, nil }}
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				args, _ := json.Marshal(map[string]any{"command": shellWriteFileForTest(target, "synthetic effect"), "yield_time_ms": 0})
				reply, err := host.Call(ctx, shell.RunCommandToolName, args)
				if err != nil || reply.IsError {
					return nil, errors.Join(err, errors.New("command did not yield approval Task"))
				}
				<-asked
				wire, _ := json.Marshal(reply)
				t.Logf("yielded reply=%s", wire)
				switch mode {
				case "generation":
					stale.Store(true)
				case "complete":
					reply, err := host.Call(ctx, "Complete", []byte(`{}`))
					if err != nil || !reply.TurnComplete {
						close(approve)
						return nil, errors.New("completion fixture failed")
					}
				}
				close(approve) // Approval resolves only after replacement/completion.
				// Keep Execute alive until this existing Task reaches a terminal.
				// Observe durable Task truth directly, without another effectful Host
				// request after the test has revoked generation or completed the Turn.
				wait, stop := context.WithTimeout(ctx, 5*time.Second)
				defer stop()
				for {
					entry, err := tasks.Get(wait, taskID)
					if err != nil {
						return nil, err
					}
					if taskapi.IsTerminalState(entry.State) {
						t.Logf("terminal task=%s state=%s", taskID, entry.State)
						break
					}
					select {
					case <-time.After(10 * time.Millisecond):
					case <-wait.Done():
						return nil, wait.Err()
					}
				}
				return []byte(`{}`), nil
			})}
			llm := codeModeModel(false)
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: llm, Tools: []tool.Tool{declaration, command, complete}, Revision: "original", Validate: func(context.Context) (func(), error) {
					if stale.Load() {
						return nil, tool.NewError(tool.ErrorCodeStaleRevision, "generation replaced")
					}
					return func() {}, nil
				}}, nil
			}}
			fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "rereview-native-task"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = store.ReleaseSessionFence(context.WithoutCancel(t.Context()), session.SessionFenceReleaseRequest(fence))
			}()
			rt, err := New(Config{Sessions: store, TaskStore: tasks, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(session.ContextWithRuntimeFence(t.Context(), fence), agent.RunRequest{SessionRef: active.SessionRef, Input: "synthetic native Task admission review", AgentSpec: spec, ApprovalRequester: requester})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				if mode == "allow-control" {
					t.Fatal(err)
				}
				t.Logf("revoked/completed Task admission returned: %v", err)
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					r := event.Journal.ToolExecution
					if r.ToolName == shell.RunCommandToolName {
						t.Logf("child identity=%s parent=%+v status=%s revision=%d", r.Identity, r.Parent, r.Status, r.Revision)
					}
				}
				if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Parent != nil {
					t.Error("nested protocol pollution")
				}
			}
			data, err := os.ReadFile(target)
			if mode == "allow-control" {
				if err != nil || string(data) != "synthetic effect" {
					t.Fatalf("allowed control failed: %q %v", data, err)
				}
			} else if err == nil {
				t.Fatalf("native Task continuation executed after %s: effect=%q", mode, data)
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}
