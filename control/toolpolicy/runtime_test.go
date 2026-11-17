package toolpolicy

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == gojaexec.WorkerArgument {
		os.Exit(gojaexec.WorkerMain())
	}
	os.Exit(m.Run())
}

type scriptModel struct {
	code  string
	calls atomic.Int32
}

func (*scriptModel) Name() string { return "policy-fixture" }
func (*scriptModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (m *scriptModel) Generate(_ context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "done")
		if m.calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"code": m.code, "yield_time_ms": 60000})
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "outer-script", Name: "js_exec", Args: string(args)}}, "")
		}
		reason := model.FinishReasonStop
		if len(message.ToolCalls()) > 0 {
			reason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted, FinishReason: reason}), nil)
	}
}

type requesterFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f requesterFunc) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	return f(ctx, req)
}

func runPolicyScript(t *testing.T, store session.Service, active session.Session, code string, targets []tool.Tool, requester agent.ApprovalRequester) {
	t.Helper()
	registry, err := policy.NewMemory(Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runtime.New(runtime.Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store.(*sessionfile.Store)), AgentFactory: chat.Factory{}, PolicyRegistry: registry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.ReleaseCodeModeEnvironments() })
	tools := append(append([]tool.Tool(nil), targets...), &codemode.Tool{Name: "js_exec", Executor: &gojaexec.Executor{}})
	run, err := r.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "Synthetic application policy fixture", AgentSpec: agent.AgentSpec{Model: &scriptModel{code: code}, Tools: tools}, ApprovalRequester: requester})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Tool != nil && event.Tool.Status == "failed" {
			t.Logf("failed tool %s: %+v", event.Tool.Name, event.Tool.Output)
		}
	}
}

func policyEffectTools(t *testing.T, count *atomic.Int32) []tool.Tool {
	t.Helper()
	var targets []tool.Tool
	for _, remote := range []string{"look", "click"} {
		in := policyFixture(t, remote)
		def := in.Tool
		def.InputSchema = map[string]any{"type": "object", "required": []string{"app"}, "properties": map[string]any{"app": map[string]any{"type": "string"}}}
		targets = append(targets, tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			count.Add(1)
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewTextPart("observed fixture")}}, nil
		}})
	}
	return targets
}

func TestProductPolicyCodeModeSharesAPPAndRebuildsAfterStoreReopen(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "owner", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	var effects, approvals atomic.Int32
	targets := policyEffectTools(t, &effects)
	requester := requesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approvals.Add(1)
		if req.Call.Parent == nil || req.Call.Execution.ItemID == "" || req.Authorization == nil || req.Authorization.Scope.Resource != "com.example.A" {
			t.Error("approval lost actual child identity or APP")
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	})
	code := `const results=await Promise.all([tools.call("dtw__look",{app:"com.example.A"}),tools.call("dtw__click",{app:"com.example.A"})]);return {count:results.length};`
	runPolicyScript(t, store, active, code, targets, requester)
	if effects.Load() != 2 || approvals.Load() != 1 {
		t.Fatalf("first batch: effects=%d approvals=%d", effects.Load(), approvals.Load())
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	loaded, err := reopened.Session(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	runPolicyScript(t, reopened, loaded, code, targets, requester)
	if effects.Load() != 4 || approvals.Load() != 1 {
		t.Fatalf("reopen: effects=%d approvals=%d", effects.Load(), approvals.Load())
	}
	events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	calls := 0
	for _, event := range events {
		if event.Tool != nil && event.Tool.Parent != nil && event.Type == session.EventTypeToolCall {
			calls++
			if ids[event.Tool.ID] {
				t.Fatalf("child call identity reused: %s", event.Tool.ID)
			}
			ids[event.Tool.ID] = true
		}
	}
	if calls != 4 {
		t.Fatalf("nested calls=%d", calls)
	}
	other, err := reopened.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "owner", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	runPolicyScript(t, reopened, other, code, targets, requester)
	if effects.Load() != 6 || approvals.Load() != 2 {
		t.Fatalf("another Session: effects=%d approvals=%d", effects.Load(), approvals.Load())
	}
}

func TestProductPolicyCrossAPPDenialDoesNotReplayEarlierEffect(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "owner", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	var effects, approvals atomic.Int32
	runPolicyScript(t, store, active, `await tools.call("dtw__click",{app:"com.example.A"});await tools.call("dtw__click",{app:"com.example.B"});return {};`, policyEffectTools(t, &effects), requesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approvals.Add(1)
		if req.Authorization.Scope.Resource == "com.example.B" {
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "cancel", Approved: false}, nil
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	}))
	if effects.Load() != 1 || approvals.Load() != 2 {
		t.Fatalf("effects=%d approvals=%d", effects.Load(), approvals.Load())
	}
}
