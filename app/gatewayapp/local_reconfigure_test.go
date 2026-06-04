package gatewayapp

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestStackRejectsReconfigureWhileActiveTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, session := newLocalStateTestStack(t)
	altAlias, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "alt-model",
	})
	if err != nil {
		t.Fatalf("Connect(alt-model) error = %v", err)
	}

	blocking := &blockingRuntime{session: session, release: make(chan struct{})}
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  blocking,
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatalf("kernel.New() error = %v", err)
	}
	stack.gateway = gw

	handle, err := stack.currentGateway().BeginTurn(ctx, gateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "hold active",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer handle.Handle.Close()
	if got := len(stack.currentGateway().ActiveTurns()); got != 1 {
		t.Fatalf("ActiveTurns() len = %d, want 1", got)
	}

	tests := []struct {
		name string
		run  func() error
		want func(*testing.T)
	}{
		{
			name: "connect",
			run: func() error {
				_, err := stack.Connect(ModelConfig{
					Provider: "ollama",
					API:      providers.APIOllama,
					Model:    "blocked-model",
				})
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				if stack.lookup.HasAlias("ollama/blocked-model") {
					t.Fatal("Connect() mutated lookup while active turn was running")
				}
			},
		},
		{
			name: "use model",
			run: func() error {
				return stack.UseModel(ctx, session.SessionRef, altAlias)
			},
			want: func(t *testing.T) {
				t.Helper()
				state, err := stack.SessionRuntimeState(ctx, session.SessionRef)
				if err != nil {
					t.Fatalf("SessionRuntimeState() error = %v", err)
				}
				if state.ModelAlias != "" {
					t.Fatalf("ModelAlias = %q, want unchanged empty state", state.ModelAlias)
				}
			},
		},
		{
			name: "delete model",
			run: func() error {
				return stack.DeleteModel(ctx, session.SessionRef, altAlias)
			},
			want: func(t *testing.T) {
				t.Helper()
				if !stack.lookup.HasAlias(altAlias) {
					t.Fatalf("DeleteModel() removed %q while active turn was running", altAlias)
				}
			},
		},
		{
			name: "set session mode",
			run: func() error {
				_, err := stack.SetSessionMode(ctx, session.SessionRef, "manual")
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				state, err := stack.SessionRuntimeState(ctx, session.SessionRef)
				if err != nil {
					t.Fatalf("SessionRuntimeState() error = %v", err)
				}
				if state.SessionMode != "auto-review" {
					t.Fatalf("SessionMode = %q, want unchanged auto-review", state.SessionMode)
				}
			},
		},
		{
			name: "set sandbox backend",
			run: func() error {
				_, err := stack.SetSandboxBackend(ctx, "auto")
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				if got := stack.SandboxStatus().RequestedBackend; got != "host" {
					t.Fatalf("SandboxStatus().RequestedBackend = %q, want unchanged host", got)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatalf("%s error = nil, want active-turn rejection", tt.name)
			}
			if !strings.Contains(err.Error(), "active") {
				t.Fatalf("%s error = %v, want readable active-turn rejection", tt.name, err)
			}
			tt.want(t)
		})
	}

	close(blocking.release)
	for range handle.Handle.Events() {
	}
}

func TestStackConnectRollsBackOnConfigSaveFailure(t *testing.T) {
	t.Parallel()

	stack, _ := newLocalStateTestStack(t)
	beforeDefault := stack.DefaultModelID()
	stack.mu.RLock()
	beforeRuntime := stack.runtime
	stack.mu.RUnlock()
	poisonConfigStorePath(t, stack)

	_, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "save-failed-model",
	})
	if err == nil {
		t.Fatal("Connect() error = nil, want config save failure")
	}
	if stack.lookup.HasAlias("ollama/save-failed-model") {
		t.Fatal("Connect() left failed model in lookup")
	}
	if got := stack.DefaultModelID(); got != beforeDefault {
		t.Fatalf("DefaultModelID() = %q, want %q", got, beforeDefault)
	}
	stack.mu.RLock()
	afterRuntime := stack.runtime
	stack.mu.RUnlock()
	if afterRuntime.Model.ID != beforeRuntime.Model.ID {
		t.Fatalf("runtime model = %q, want %q", afterRuntime.Model.ID, beforeRuntime.Model.ID)
	}
}

func TestStackSetSandboxBackendRollsBackOnConfigSaveFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	before := stack.SandboxStatus()
	beforeGateway := stack.CurrentGateway()
	poisonConfigStorePath(t, stack)

	_, err := stack.SetSandboxBackend(ctx, "auto")
	if err == nil {
		t.Fatal("SetSandboxBackend() error = nil, want config save failure")
	}
	after := stack.SandboxStatus()
	if after.RequestedBackend != before.RequestedBackend || after.ResolvedBackend != before.ResolvedBackend {
		t.Fatalf("SandboxStatus() = %+v, want rollback to %+v", after, before)
	}
	if afterGateway := stack.CurrentGateway(); afterGateway != beforeGateway {
		t.Fatalf("CurrentGateway() changed on save failure: before=%p after=%p", beforeGateway, afterGateway)
	}
}

func poisonConfigStorePath(t *testing.T, stack *Stack) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	stack.store.path = filepath.Join(blocker, "config.json")
}

type blockingResolver struct{}

func (blockingResolver) ResolveTurn(context.Context, gateway.TurnIntent) (gateway.ResolvedTurn, error) {
	return gateway.ResolvedTurn{RunRequest: agent.RunRequest{}}, nil
}

type blockingRuntime struct {
	session session.Session
	release chan struct{}
}

func (r *blockingRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{
		Session: r.session,
		Handle:  blockingRunner{release: r.release},
	}, nil
}

func (r *blockingRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{Status: agent.RunLifecycleStatusRunning}, nil
}

type blockingRunner struct {
	release <-chan struct{}
}

func (blockingRunner) RunID() string { return "run-blocking" }

func (r blockingRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		<-r.release
	}
}

func (blockingRunner) Submit(agent.Submission) error { return nil }
func (blockingRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (blockingRunner) Close() error { return nil }
