package toolpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
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

type requesterFunc func(context.Context, approval.Request) (approval.Response, error)

func (f requesterFunc) RequestApproval(ctx context.Context, req approval.Request) (approval.Response, error) {
	return f(ctx, req)
}

func runPolicyScript(t *testing.T, store session.Service, active session.Session, code string, targets []tool.Tool, requester approval.Requester, hooks ...hook.Set) {
	t.Helper()
	registry, err := policy.NewMemory(Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	var configured hook.Set
	if len(hooks) > 0 {
		configured = hooks[0]
	}
	configured.ToolPolicy = registry
	r, err := runtime.New(runtime.Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store.(*sessionfile.Store)), AgentFactory: chat.Factory{}, Hooks: configured})
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
			return tool.Result{Content: []tool.ContentBlock{tool.Text("observed fixture")}}, nil
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
	requester := requesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
		approvals.Add(1)
		if req.Parent == nil || req.Parent.ItemID == "" || req.Execution.ItemID == "" || req.Authorization == nil || req.Authorization.Scope.Resource != "com.example.A" {
			t.Error("approval lost actual child identity or APP")
		} else {
			assertCodeModeCallDigest(t, ctx, req)
		}
		return approval.Response{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
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

// Assert the pre-migration digest layout using a real Runtime-produced approval.
func assertCodeModeCallDigest(t *testing.T, ctx context.Context, req approval.Request) {
	t.Helper()
	base, err := presets.WorkspaceWriteMode().DecideTool(ctx, policy.ToolContext{
		Session: req.Session, Tool: req.Tool, Call: req.Call,
		Options: policy.ModeOptions{WorkspaceRoot: req.Session.CWD, TempRoot: os.TempDir()},
	})
	if err != nil {
		t.Errorf("base Policy constraints: %v", err)
		return
	}
	parent := struct {
		Kind      string                 `json:"kind,omitempty"`
		ID        string                 `json:"id"`
		Name      string                 `json:"name"`
		Execution tool.InvocationContext `json:"execution"`
	}{req.Parent.Kind, req.Parent.ID, req.Parent.Name, tool.InvocationContext{
		SessionID: req.SessionRef.SessionID, TurnID: req.TurnID, ItemID: req.Parent.ItemID,
	}}
	material, err := json.Marshal(struct {
		ID, Name    string
		Input       json.RawMessage
		Execution   tool.InvocationContext
		Parent      any
		Binding     tool.Binding
		Constraints any
	}{req.Call.ID, req.Call.Name, req.Call.Input, req.Execution, parent, mcp.BindingForDefinition(req.Tool), base.Constraints})
	if err != nil {
		t.Errorf("encode legacy call digest material: %v", err)
		return
	}
	sum := sha256.Sum256(material)
	if want := hex.EncodeToString(sum[:]); req.Authorization.CallDigest != want {
		t.Errorf("approval digest = %s, want legacy digest %s", req.Authorization.CallDigest, want)
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
	runPolicyScript(t, store, active, `await tools.call("dtw__click",{app:"com.example.A"});await tools.call("dtw__click",{app:"com.example.B"});return {};`, policyEffectTools(t, &effects), requesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
		approvals.Add(1)
		if req.Authorization.Scope.Resource == "com.example.B" {
			return approval.Response{Outcome: "selected", OptionID: "cancel", Approved: false}, nil
		}
		return approval.Response{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	}))
	if effects.Load() != 1 || approvals.Load() != 2 {
		t.Fatalf("effects=%d approvals=%d", effects.Load(), approvals.Load())
	}
}

func TestProductPolicyHookFinalAPPDoesNotReuseOriginalConsent(t *testing.T) {
	for _, synthetic := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispatch", true: "synthetic"}[synthetic], func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "owner", Workspace: session.WorkspaceRef{Key: "workspace", CWD: root}})
			if err != nil {
				t.Fatal(err)
			}
			var effects, asks, prepares atomic.Int32
			targets := policyEffectTools(t, &effects)
			code := `await tools.call("dtw__click",{app:"com.example.A"});return {};`
			runPolicyScript(t, store, active, code, targets, requesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
				asks.Add(1)
				if req.Authorization == nil || req.Authorization.Scope.Resource != "com.example.A" {
					t.Error("original APP consent not scoped")
				}
				return approval.Response{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
			}))
			hooks := hook.Set{ToolPrepare: []hook.ToolPrepare{{Name: "target-B", Apply: func(_ context.Context, e hook.ToolContext, p hook.ToolPreparation) (hook.ToolPreparation, error) {
				if e.Tool.Name != "dtw__click" {
					return p, nil
				}
				prepares.Add(1)
				p.Input = json.RawMessage(`{"app":"com.example.B"}`)
				if synthetic {
					p.Kind = hook.ToolPrepareRespond
					p.Result = &tool.Result{Content: []tool.ContentBlock{tool.Text("synthetic success")}}
				}
				return p, nil
			}}}}
			runPolicyScript(t, store, active, code, targets, requesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				asks.Add(1)
				if req.Authorization == nil || req.Authorization.Scope.Resource != "com.example.B" || string(req.Call.Input) != `{"app":"com.example.B"}` {
					t.Errorf("approval did not consume final APP: %+v", req)
				}
				if req.OnAdmission != nil {
					if err := req.OnAdmission(ctx); err != nil {
						return approval.Response{}, err
					}
				}
				events, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					return approval.Response{}, err
				}
				found := false
				for _, e := range events {
					found = found || e.Type == session.EventTypeToolCall && e.Tool != nil && e.Tool.Execution == req.Execution && e.Tool.Input["app"] == "com.example.B"
				}
				if !found {
					t.Error("approval began before the effective child fact")
				}
				delayed := make(chan struct{})
				timer := time.AfterFunc(10*time.Millisecond, func() { close(delayed) })
				defer timer.Stop()
				select {
				case <-delayed:
				case <-ctx.Done():
					return approval.Response{}, ctx.Err()
				}
				return approval.Response{Outcome: "selected", OptionID: "cancel", Approved: false}, nil
			}), hooks)
			if effects.Load() != 1 || asks.Load() != 2 || prepares.Load() != 1 {
				t.Fatalf("APP A consent or synthetic result bypassed APP B denial: effects=%d asks=%d prepares=%d", effects.Load(), asks.Load(), prepares.Load())
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
			found := false
			for _, p := range pairs {
				if p.Call.Tool.Input["app"] == "com.example.B" {
					found = true
					if p.Result == nil || p.Result.Tool.Outcome != session.ToolOutcomeFailed {
						t.Fatal("denied effective APP result absent on reopen")
					}
				}
			}
			if !found {
				t.Fatal("final APP fact absent")
			}
		})
	}
}
