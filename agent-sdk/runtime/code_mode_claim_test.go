package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestCodeModeClaimGuardExcludesReplacementWithoutCoveringApprovalOrIO(t *testing.T) {
	files, active, _ := codeModeStore(t)
	var revision sync.RWMutex
	var stale atomic.Bool
	store := &reviewGenerationStore{Service: files, revisionMu: &revision, stale: &stale}
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if input.Tool.Name == "Write" {
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	var approved, executed atomic.Bool
	requester := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if !revision.TryLock() {
			return agent.ApprovalResponse{}, errors.New("configuration guard held during approval")
		}
		revision.Unlock()
		approved.Store(true)
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		if !revision.TryLock() {
			return tool.Result{}, errors.New("configuration guard held during external tool I/O")
		}
		revision.Unlock()
		executed.Store(true)
		return tool.Result{}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "Write", []byte(`{}`))
		return []byte(`{}`), err
	})}
	llm := codeModeModel(false)
	spec := agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration, write}}
	spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Model: llm, Tools: spec.Tools, Validate: func(context.Context) (func(), error) {
			revision.RLock()
			return revision.RUnlock, nil
		}}, nil
	}
	runCodeMode(t, store, active, spec, registry, requester)
	if !approved.Load() || !executed.Load() || store.updated.Load() {
		t.Fatalf("approval=%v execution=%v replacement_during_claim=%v", approved.Load(), executed.Load(), store.updated.Load())
	}
}

func TestCodeModeTurnCompletePendingApprovalStillEmitsMedia(t *testing.T) {
	store, active, _ := codeModeStore(t)
	asked, release := make(chan struct{}), make(chan struct{})
	var effects atomic.Int32
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if input.Tool.Name == "Write" {
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	requester := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		close(asked)
		<-release
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	image := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}, "image/png", "fixture.png")
	complete := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{TurnComplete: true, Content: []model.Part{image}}, nil
	}}
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeOutputExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (codemode.Output, error) {
		done := make(chan error, 1)
		go func() { _, err := host.Call(ctx, "Write", []byte(`{}`)); done <- err }()
		<-asked
		reply, err := host.Call(ctx, "Complete", []byte(`{}`))
		close(release)
		blocked := <-done
		if err != nil || !reply.TurnComplete || len(reply.Parts) != 1 || !errors.Is(blocked, errCodeModeComplete) {
			return codemode.Output{}, errors.New("completion did not fence the pending approval")
		}
		return codemode.Output{Reply: true, Parts: reply.Parts}, nil
	})}
	llm := codeModeModel(false)
	live, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{llm}, Tools: []tool.Tool{declaration, complete, write}}, registry, requester)
	emitted := false
	for _, event := range live {
		if session.IsCanonicalHistoryEvent(event) && event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == "js_repl" {
			if !event.Tool.TurnComplete || event.Message == nil || event.Message.Parts[0].ToolResult.IsError {
				t.Fatalf("completion disposition lost: %+v", event.Tool)
			}
			for _, part := range event.Message.Parts[0].ToolResult.Content {
				if part.Media != nil && part.Media.Source.Data == image.Media.Source.Data {
					emitted = true
				}
			}
		}
	}
	if effects.Load() != 0 || !emitted || len(llm.requests) != 1 {
		t.Fatalf("effects=%d emitted=%v model_requests=%d", effects.Load(), emitted, len(llm.requests))
	}
}

func TestCodeModeRunnerJoinsUnresponsiveExecutor(t *testing.T) {
	store, active, _ := codeModeStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, _ codemode.Host) (json.RawMessage, error) {
		close(started)
		<-release
		return nil, ctx.Err()
	})}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "cancel unresponsive adapter", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	wait, stop := context.WithTimeout(t.Context(), 250*time.Millisecond)
	err = run.Handle.WaitCompletion(wait)
	stop()
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Runner released an active adapter: %v", err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("terminal=%v", err)
	}
}
