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
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// Code Mode retains the original Run authority through producer quiescence.
func TestReviewCodeModeTurnCompleteFencesPendingApproval(t *testing.T) {
	store, active, _ := codeModeStore(t)
	asked, release := make(chan struct{}), make(chan struct{})
	var effects atomic.Int32
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if input.Tool.Name == "Write" {
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}}}}, nil
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	requester := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		close(asked)
		<-release
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow", Approved: true}, nil
	})
	complete := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}, Capabilities: tool.Capabilities{ParallelSafe: true}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{TurnComplete: true}, nil
	}}
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}, Capabilities: tool.Capabilities{ParallelSafe: true}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		done := make(chan error, 1)
		go func() { _, err := host.Call(ctx, "Write", []byte(`{}`)); done <- err }()
		<-asked
		reply, err := host.Call(ctx, "Complete", []byte(`{}`))
		if err != nil || !reply.TurnComplete {
			close(release)
			<-done
			return nil, errors.New("completion fixture failed")
		}
		close(release) // Approval resolves strictly after trusted completion.
		<-done
		return []byte(`{}`), nil
	})}
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, complete, write}}, registry, requester)
	if got := effects.Load(); got != 0 {
		t.Fatalf("effect executed after trusted TurnComplete: effects=%d", got)
	}
}

type reviewGenerationStore struct {
	session.Service
	revisionMu *sync.RWMutex
	stale      *atomic.Bool
	updated    atomic.Bool
}

func (s *reviewGenerationStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.Journal != nil && req.Event.Journal.ToolExecution != nil {
		r := req.Event.Journal.ToolExecution
		if r.Parent != nil && r.Status == session.ToolExecutionPrepared && s.revisionMu.TryLock() {
			// Model a configuration writer at the first durable dispatch step.
			// A retained Validate guard must exclude this revision replacement.
			s.stale.Store(true)
			s.updated.Store(true)
			s.revisionMu.Unlock()
		}
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestReviewCodeModeGenerationGuardCoversDispatch(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var revisionMu sync.RWMutex
	var stale, staleEffect atomic.Bool
	interleaved := &reviewGenerationStore{Service: store, revisionMu: &revisionMu, stale: &stale}
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		staleEffect.Store(stale.Load())
		return tool.Result{}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "Write", []byte(`{}`))
		return []byte(`{}`), err
	})}
	llm := codeModeModel(false)
	spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Model: llm, Tools: []tool.Tool{declaration, write}, Revision: "review-generation", Validate: func(context.Context) (func(), error) {
			revisionMu.RLock()
			if stale.Load() {
				revisionMu.RUnlock()
				return nil, tool.NewError(tool.ErrorCodeStaleRevision, "generation replaced")
			}
			return revisionMu.RUnlock, nil
		}}, nil
	}}
	runCodeMode(t, interleaved, active, spec, nil, nil)
	if staleEffect.Load() {
		t.Fatalf("generation replaced between final validation and effect: revision_update=%v stale_effect=true", interleaved.updated.Load())
	}
}

func TestReviewCodeModeDrainRetainsSessionFence(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		name := "unfenced-sdk"
		if fenced {
			name = "canonical-fence"
		}
		t.Run(name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			started, release, hostDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			hostResult := make(chan error, 1)
			var oldEffects, nextEffects atomic.Int32
			write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				close(started)
				<-release // An ordinary Go Tool may ignore cancellation during an effect.
				oldEffects.Add(1)
				return tool.Result{}, nil
			}}
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				go func() { defer close(hostDone); _, err := host.Call(ctx, "Write", []byte(`{}`)); hostResult <- err }()
				<-ctx.Done() // The adapter itself stops promptly and does no more work.
				return nil, ctx.Err()
			})}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var firstFence session.SessionFence
			if fenced {
				firstFence, err = store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "review-host"})
				if err != nil {
					t.Fatal(err)
				}
				ctx = session.ContextWithRuntimeFence(ctx, firstFence)
			}
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "cancel while native effect is in flight", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, write}}})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			cancel()
			wait, stop := context.WithTimeout(t.Context(), 10*time.Second)
			defer stop()
			terminal := run.Handle.WaitCompletion(wait)
			if errors.Is(terminal, context.DeadlineExceeded) {
				// A corrected implementation may retain completion until this
				// producer actually stops. Quiesce it before releasing authority.
				close(release)
				<-hostDone
				_ = run.Handle.WaitCompletion(t.Context())
				if fenced {
					if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(firstFence)); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			// This is the existing Host contract: WaitCompletion proves quiescence and
			// authorizes exact fence release. The test does not force or bypass a fence.
			if fenced {
				if err := store.ReleaseSessionFence(wait, session.SessionFenceReleaseRequest(firstFence)); err != nil {
					t.Fatal(err)
				}
			}
			// Observe a fresh canonical Turn while the old producer is still blocked.
			nextDeclaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				_, err := host.Call(ctx, "NextWrite", []byte(`{}`))
				return []byte(`{}`), err
			})}
			nextWrite := tool.NamedTool{Def: tool.Definition{Name: "NextWrite", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { nextEffects.Add(1); return tool.Result{}, nil }}
			nextCtx := wait
			if fenced {
				nextFence, err := store.AcquireSessionFence(wait, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "review-host"})
				if err != nil {
					close(release)
					t.Fatal(err)
				}
				defer func() {
					_ = store.ReleaseSessionFence(context.WithoutCancel(wait), session.SessionFenceReleaseRequest(nextFence))
				}()
				nextCtx = session.ContextWithRuntimeFence(nextCtx, nextFence)
			}
			next, nextErr := rt.Run(nextCtx, agent.RunRequest{SessionRef: active.SessionRef, Input: "next turn", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{nextDeclaration, nextWrite}}})
			if nextErr == nil {
				nextErr = next.Handle.WaitCompletion(wait)
			}
			beforeRelease := nextEffects.Load()
			close(release)
			select {
			case <-hostDone:
			case <-wait.Done():
				t.Fatal("old Host did not finish after release")
			}
			t.Logf("late Host result: %v", <-hostResult)
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					r := event.Journal.ToolExecution
					if r.Parent != nil && r.ToolName == "Write" {
						t.Logf("original child identity=%s call_id=%s status=%s revision=%d visibility=%s", r.Identity, r.Key.ToolCallID, r.Status, r.Revision, event.Visibility)
					}
				}
				if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Parent != nil {
					t.Error("nested recovery contaminated canonical model protocol")
				}
			}
			if beforeRelease != 0 {
				t.Fatalf("Session fence released with old Host producer alive: first_terminal=%v next_terminal=%v next_effects=%d old_effects_after_release=%d", terminal, nextErr, beforeRelease, oldEffects.Load())
			}
		})
	}
}

// Redirect one synthetic sensitive filename to private test bytes. No user file
// is opened, read, or changed. All other filesystem behavior uses the real host.
type reviewMappedFS struct {
	sandbox.FileSystem
	sensitive, fixture string
}

func (f reviewMappedFS) Open(path string) (*os.File, error) {
	if path == f.sensitive {
		return os.Open(f.fixture)
	}
	return f.FileSystem.Open(path)
}

type reviewSourceRuntime struct {
	sandbox.Runtime
	fsys sandbox.FileSystem
}

func (r reviewSourceRuntime) FileSystem() sandbox.FileSystem                       { return r.fsys }
func (r reviewSourceRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem { return r.fsys }

func TestReviewCodeModeFileSourceUsesReadAuthorization(t *testing.T) {
	store, active, root := codeModeStore(t)
	base, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sensitive := filepath.Join(homeDir, ".ssh", "codemode-independent-review-fixture.js")
	fixture := filepath.Join(root, "safe-fixture.js")
	if err := os.WriteFile(fixture, []byte("synthetic sensitive bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	// Verify the existing path policy denies this exact filename for Read.
	readArgs, _ := json.Marshal(map[string]string{"path": sensitive})
	decision, err := presets.WorkspaceWriteMode().DecideTool(t.Context(), policy.ToolContext{Session: active, Tool: tool.Definition{Name: filesystem.ReadToolName}, Call: tool.Call{Name: filesystem.ReadToolName, Input: readArgs}})
	if err != nil || decision.Action != policy.ActionDeny {
		t.Fatalf("Read control: action=%s err=%v", decision.Action, err)
	}
	var executed atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Sandbox: reviewSourceRuntime{Runtime: base, fsys: reviewMappedFS{FileSystem: base.FileSystem(), sensitive: sensitive, fixture: fixture}}, Executor: codeExecutorFunc(func(_ context.Context, source codemode.Source, _ codemode.Host) (json.RawMessage, error) {
		executed.Store(source.Code == "synthetic sensitive bytes")
		return []byte(`{}`), nil
	})}
	fileArgs, _ := json.Marshal(map[string]string{"file": sensitive})
	llm := codeModeModel(false)
	llm.steps = func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: "js_repl", Args: string(fileArgs)}}
		}
		return nil
	}
	registry, err := presets.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: presets.ModeWorkspaceWrite})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read synthetic source", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if executed.Load() {
		t.Fatal("Code Mode executed file bytes denied by the existing Read path policy")
	}
}
