package toolpolicy

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// An unrelated catalog change does not invalidate app approval, but the
// pending Code Mode call must still use its own current MCP binding.
func TestProductPolicyPendingCodeModeBindingChange(t *testing.T) {
	for _, change := range []string{"unchanged", "same-tool-schema", "other-atomic-schema", "source-revoke"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "dtw-fixture", Version: "1"}, &mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{Tools: &mcpsdk.ToolCapabilities{}}})
			var effects, asks atomic.Int32
			publish := func(name, description string) {
				srv.AddTool(&mcpsdk.Tool{Name: name, Description: description, InputSchema: map[string]any{"type": "object", "required": []string{"app"}, "properties": map[string]any{"app": map[string]any{"type": "string"}, "revision_marker": map[string]any{"type": "string", "const": description}}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					effects.Add(1)
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "effect"}}}, nil
				})
			}
			for _, name := range append(AtomicOperations(), "apps") {
				publish(name, "original")
			}
			ep := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil))
			defer ep.Close()
			mgr, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "desktop-world", Name: "dtw", Transport: mcp.TransportStreamableHTTP, URL: ep.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			<-mgr.Initialized()
			before := make(map[string]tool.Binding)
			for _, name := range []string{"dtw__look", "dtw__click"} {
				old, found, err := mgr.Lookup(ctx, name)
				if err != nil || !found {
					t.Fatalf("initial lookup of %s: found=%v err=%v", name, found, err)
				}
				before[name] = mcp.BindingForDefinition(old.Definition())
			}
			store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "fixture", UserID: "owner", Workspace: session.WorkspaceRef{Key: "fixture", CWD: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			policies, err := policy.NewMemory(Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			llm := &fixedPolicyModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"return await dtw.look({app:'com.example.A'});"}`}}
				}
				return nil
			}}
			req := requesterFunc(func(_ context.Context, request approval.Request) (approval.Response, error) {
				asks.Add(1)
				if request.Authorization == nil {
					return approval.Response{}, fmt.Errorf("missing product authorization")
				}
				switch change {
				case "same-tool-schema":
					publish("look", "changed")
				case "other-atomic-schema":
					publish("click", "changed")
				case "source-revoke":
					if err := mgr.RevokeServer("desktop-world", "dtw"); err != nil {
						return approval.Response{}, err
					}
				}
				if change != "source-revoke" {
					if err := mgr.RefreshTools(ctx, "desktop-world", "dtw"); err != nil {
						return approval.Response{}, err
					}
					for name, old := range before {
						current, found, err := mgr.Lookup(ctx, name)
						if err != nil || !found {
							t.Errorf("refreshed lookup of %s: found=%v err=%v", name, found, err)
							continue
						}
						wantChanged := change == "same-tool-schema" && name == "dtw__look" || change == "other-atomic-schema" && name == "dtw__click"
						if changed := mcp.BindingForDefinition(current.Definition()) != old; changed != wantChanged {
							t.Errorf("binding for %s changed=%v, want %v", name, changed, wantChanged)
						}
					}
				}
				return approval.Response{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
			})
			rt, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: policies}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "test", ApprovalRequester: req, AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: mgr}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(ctx); err != nil {
				t.Fatal(err)
			}
			t.Logf("change=%s approvals=%d effects=%d", change, asks.Load(), effects.Load())
			if asks.Load() != 1 {
				t.Fatalf("expected actual approval, got %d", asks.Load())
			}
			want := int32(0)
			if change == "unchanged" || change == "other-atomic-schema" {
				want = 1
			}
			if effects.Load() != want {
				t.Fatalf("dispatch after %s: got effects=%d want=%d", change, effects.Load(), want)
			}
		})
	}
}

type fixedPolicyModel struct {
	scriptModel
	index int
	steps func(int, *model.Request) []model.ToolCall
}

func (m *fixedPolicyModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	calls := m.steps(m.index, req)
	m.index++
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "done")
		reason := model.FinishReasonStop
		if len(calls) > 0 {
			message = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
			reason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Status: model.ResponseStatusCompleted, FinishReason: reason, StepComplete: true, TurnComplete: true}), nil)
	}
}
