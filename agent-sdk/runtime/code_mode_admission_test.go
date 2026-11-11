package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCodeModeRevalidatesMCPAndGenerationAfterApproval(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "schema", "revoke", "generation", "missing-validator"} {
		t.Run(mode, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "admission", Version: "1"}, nil)
			var effects, approvals atomic.Int32
			publish := func(kind string) {
				server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					effects.Add(1)
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
				})
			}
			publish("integer")
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			if len(manager.Tools()) != 1 {
				t.Fatal("MCP fixture did not initialize")
			}
			var stale atomic.Bool
			requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				approvals.Add(1)
				if req.Tool.Name != "live__read" || req.Call.Parent == nil || req.Call.Parent.ID != "script" || req.Call.Execution.ItemID == "" {
					t.Errorf("approval lost real identity: %+v", req.Call)
				}
				switch mode {
				case "schema":
					publish("string")
					if err := manager.RefreshTools(ctx, "fixture", "live"); err != nil {
						return agent.ApprovalResponse{}, err
					}
				case "revoke":
					if err := manager.RevokeServer("fixture", "live"); err != nil {
						return agent.ApprovalResponse{}, err
					}
				case "generation":
					stale.Store(true)
				}
				option := "allow_once"
				if mode == "deny" {
					option = "cancel"
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: option, Approved: mode != "deny"}, nil
			})
			var checked atomic.Bool
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				reply, err := host.Call(ctx, "live__read", []byte(`{"value":1}`))
				switch mode {
				case "allow":
					if err != nil || reply.IsError {
						return nil, errors.New("allowed call failed")
					}
				case "deny":
					if err != nil || !reply.IsError {
						return nil, errors.New("approval rejection lost")
					}
				default:
					if err == nil {
						return nil, errors.New("stale/revoked pending call reached effect")
					}
				}
				checked.Store(true)
				return []byte(`{"checked":true}`), nil
			})}
			llm := codeModeModel(true)
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				snapshot := agent.ModelRequestSnapshot{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{}), declaration}, DeferredTools: manager, Revision: "generation-1", Validate: func(context.Context) (func(), error) {
					if stale.Load() {
						return nil, tool.NewError(tool.ErrorCodeStaleRevision, "generation revoked")
					}
					return func() {}, nil
				}}
				if mode == "missing-validator" {
					snapshot.Validate = nil
				}
				return snapshot, nil
			}}
			runCodeMode(t, store, active, spec, nil, requester)
			want := int32(0)
			if mode == "allow" {
				want = 1
			}
			wantApprovals := int32(1)
			if mode == "missing-validator" {
				wantApprovals = 0
			}
			if !checked.Load() || approvals.Load() != wantApprovals || effects.Load() != want {
				t.Fatalf("checked=%v approvals=%d effects=%d", checked.Load(), approvals.Load(), effects.Load())
			}
		})
	}
}

func TestCodeModeCancellationAndRecoveryDoNotReplayEffects(t *testing.T) {
	for _, outcome := range []string{"responsive-unknown", "unresponsive-unknown", "unresponsive-success"} {
		responsive := outcome == "responsive-unknown"
		wantStatus := session.ToolExecutionUnknownOutcome
		if outcome == "unresponsive-success" {
			wantStatus = session.ToolExecutionSucceeded
		}
		t.Run(outcome, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			var effects atomic.Int32
			started, release, executorDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{Content: []model.Part{model.NewTextPart("done once")}}, nil
			}}
			pending := tool.NamedTool{Def: tool.Definition{Name: "Pending", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				effects.Add(1)
				close(started)
				if responsive {
					<-ctx.Done()
					return tool.Result{IsError: true}, ctx.Err()
				}
				<-release
				if wantStatus == session.ToolExecutionSucceeded {
					return tool.Result{Content: []model.Part{model.NewTextPart("confirmed after cancellation")}}, nil
				}
				return tool.Result{IsError: true}, ctx.Err()
			}}
			var retained codemode.Host
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				defer close(executorDone)
				retained = host
				if _, err := host.Call(ctx, "Write", []byte(`{}`)); err != nil {
					return nil, err
				}
				_, err := host.Call(ctx, "Pending", []byte(`{}`))
				return []byte(`{}`), err
			})}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "code-mode-cancellation-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			ctx = session.ContextWithRuntimeFence(ctx, fence)
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "effects then cancel", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, write, pending}}})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("pending effect never started")
			}
			cancel()
			if !responsive {
				// Foreground cancellation is not quiescence. The existing Run
				// owner must remain joined while this producer ignores cancellation.
				wait, stop := context.WithTimeout(t.Context(), 250*time.Millisecond)
				err := run.Handle.WaitCompletion(wait)
				stop()
				if !errors.Is(err, context.DeadlineExceeded) {
					close(release)
					t.Fatalf("Runner released a live producer: %v", err)
				}
			}
			close(release)
			wait, stop := context.WithTimeout(t.Context(), 2*time.Second)
			defer stop()
			if err := run.Handle.WaitCompletion(wait); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel terminal=%v", err)
			}
			select {
			case <-executorDone:
			case <-time.After(5 * time.Second):
				t.Fatal("executor did not drain")
			}
			if _, err := retained.Call(t.Context(), "Write", []byte(`{}`)); err == nil {
				t.Fatal("retained Host accepted work after cancellation")
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			before, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var original string
			var pendingStatus session.ToolExecutionStatus
			for _, event := range before {
				if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.ToolName == "Pending" {
					original = event.Journal.ToolExecution.Identity
					pendingStatus = event.Journal.ToolExecution.Status
				}
			}
			if original == "" || pendingStatus != wantStatus {
				t.Fatalf("child terminal missing before fence release: identity=%s status=%s", original, pendingStatus)
			}
			if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
				t.Fatal(err)
			}
			// A fresh Runtime performs existing receipt recovery, without dispatching
			// the executor or either effect again.
			idle := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
			runCodeMode(t, reopened, active, agent.AgentSpec{Model: idle, Tools: []tool.Tool{declaration, write, pending}}, nil, nil)
			after, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			statuses := map[string]session.ToolExecutionStatus{}
			for _, event := range after {
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					r := event.Journal.ToolExecution
					if r.Parent != nil {
						statuses[r.ToolName] = r.Status
						if r.ToolName == "Pending" && r.Identity != original {
							t.Fatal("recovery replaced original operation identity")
						}
					}
				}
				if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Parent != nil {
					t.Fatal("recovery fabricated inner model tool result")
				}
			}
			if original == "" || effects.Load() != 2 || statuses["Write"] != session.ToolExecutionSucceeded || statuses["Pending"] != wantStatus {
				t.Fatalf("effects=%d statuses=%v original=%s", effects.Load(), statuses, original)
			}
		})
	}
}
