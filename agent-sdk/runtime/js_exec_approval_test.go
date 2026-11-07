package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// Replaces comparison-only command-in-script approval tests with real JS and
// an allowed filesystem effect, keeping the original Runtime approval/Journal.
func TestJSExecYieldApprovalGuardAndTermination(t *testing.T) {
	for _, mode := range []string{"allow", "generation", "complete", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			sandbox, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close()
			write, err := filesystem.NewWrite(sandbox)
			if err != nil {
				t.Fatal(err)
			}
			asked, approve := make(chan struct{}), make(chan struct{})
			var revision sync.RWMutex
			var stale atomic.Bool
			var effects, after, approvals atomic.Int32
			counted := tool.NamedTool{Def: write.Definition(), Invoke: func(ctx context.Context, c tool.Call) (tool.Result, error) {
				if !revision.TryLock() {
					t.Error("configuration guard held during filesystem I/O")
				} else {
					revision.Unlock()
				}
				effects.Add(1)
				return write.Call(ctx, c)
			}}
			later := tool.NamedTool{Def: tool.Definition{Name: "After", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { after.Add(1); return tool.Result{}, nil }}
			complete := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{TurnComplete: true}, nil }}
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == "Write" {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				approvals.Add(1)
				if req.Tool.Name != "Write" || req.Call.Parent == nil || req.Call.Parent.ID != "script" {
					t.Error("approval lost original child identity")
				}
				if !revision.TryLock() {
					t.Error("configuration guard held during approval")
				} else {
					revision.Unlock()
				}
				close(asked)
				select {
				case <-approve:
				case <-ctx.Done():
				}
				// Deliberately race a late approval against invalidation; the original
				// admission chain, not a second approval owner, must still prevent dispatch.
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			})
			var handle string
			driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"await tools.call('Write',{path:'effect.txt',content:'once'});await tools.call('After',{});return {done:true};","yield_time_ms":0}`}}
				case 1:
					select {
					case <-asked:
					case <-time.After(4 * time.Second):
						t.Error("real JS did not reach pending approval")
						return nil
					}
					handle = jsExecTaskHandle(r)
					if handle == "" {
						t.Error("missing original yielded Task")
						return nil
					}
					if mode == "complete" {
						return []model.ToolCall{{ID: "complete", Name: "Complete", Args: `{}`}}
					}
					action := "wait"
					if mode == "cancel" {
						action = "cancel"
					} else {
						if mode == "generation" {
							revision.Lock()
							stale.Store(true)
							revision.Unlock()
						}
						close(approve)
					}
					raw, _ := json.Marshal(map[string]any{"action": action, "handle": handle})
					return []model.ToolCall{{ID: "observe", Name: "Task", Args: string(raw)}}
				}
				return nil
			}}
			tools := []tool.Tool{counted, later, complete, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: driver, Tools: tools, Validate: func(context.Context) (func(), error) {
					revision.RLock()
					if stale.Load() {
						revision.RUnlock()
						return nil, tool.NewError(tool.ErrorCodeStaleRevision, "generation replaced")
					}
					return revision.RUnlock, nil
				}}, nil
			}}
			_, rt := runCodeMode(t, store, active, spec, registry, requester)
			want := int32(0)
			if mode == "allow" {
				want = 1
			}
			data, fileErr := os.ReadFile(filepath.Join(root, "effect.txt"))
			if effects.Load() != want || after.Load() != want || approvals.Load() != 1 {
				t.Fatalf("effects=%d after=%d approvals=%d", effects.Load(), after.Load(), approvals.Load())
			}
			if want == 1 && (fileErr != nil || string(data) != "once") {
				t.Fatalf("actual filesystem effect=%q err=%v", data, fileErr)
			}
			if want == 0 && !os.IsNotExist(fileErr) {
				t.Fatalf("unexpected filesystem effect: %q %v", data, fileErr)
			}
			entries, _ := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if len(entries) != 1 || entries[0].Running {
				t.Fatalf("original Task did not terminate: %+v", entries)
			}
			events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			starts := 0
			for _, e := range events {
				if e.Journal != nil && e.Journal.ToolExecution != nil {
					r := e.Journal.ToolExecution
					if r.ToolName == "Write" && r.Status == session.ToolExecutionStarted {
						starts++
						if r.Parent == nil || r.Parent.ID != "script" {
							t.Fatal("receipt lost parent")
						}
					}
				}
			}
			if starts != int(want) {
				t.Fatalf("original durable started claims=%d want=%d", starts, want)
			}
		})
	}
}
