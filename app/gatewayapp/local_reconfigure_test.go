package gatewayapp

import (
	"context"
	"iter"
	"strings"
	"testing"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestStackRejectsReconfigureWhileActiveTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, session := newLocalStateTestStack(t)
	altAlias, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      sdkproviders.APIOllama,
		Model:    "alt-model",
	})
	if err != nil {
		t.Fatalf("Connect(alt-model) error = %v", err)
	}

	blocking := &blockingRuntime{session: session, release: make(chan struct{})}
	gw, err := appgateway.New(appgateway.Config{
		Sessions: stack.Sessions,
		Runtime:  blocking,
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatalf("gateway.New() error = %v", err)
	}
	stack.Gateway = gw

	handle, err := stack.Gateway.BeginTurn(ctx, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "hold active",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer handle.Handle.Close()
	if got := len(stack.Gateway.ActiveTurns()); got != 1 {
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
					API:      sdkproviders.APIOllama,
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
				_, err := stack.SetSessionMode(ctx, session.SessionRef, "plan")
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				state, err := stack.SessionRuntimeState(ctx, session.SessionRef)
				if err != nil {
					t.Fatalf("SessionRuntimeState() error = %v", err)
				}
				if state.SessionMode != "default" {
					t.Fatalf("SessionMode = %q, want unchanged default", state.SessionMode)
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

type blockingResolver struct{}

func (blockingResolver) ResolveTurn(context.Context, appgateway.TurnIntent) (appgateway.ResolvedTurn, error) {
	return appgateway.ResolvedTurn{RunRequest: sdkruntime.RunRequest{}}, nil
}

type blockingRuntime struct {
	session sdksession.Session
	release chan struct{}
}

func (r *blockingRuntime) Run(context.Context, sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	return sdkruntime.RunResult{
		Session: r.session,
		Handle:  blockingRunner{release: r.release},
	}, nil
}

func (r *blockingRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return sdkruntime.RunState{Status: sdkruntime.RunLifecycleStatusRunning}, nil
}

type blockingRunner struct {
	release <-chan struct{}
}

func (blockingRunner) RunID() string { return "run-blocking" }

func (r blockingRunner) Events() iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		<-r.release
	}
}

func (blockingRunner) Submit(sdkruntime.Submission) error { return nil }
func (blockingRunner) Cancel() bool                       { return true }
func (blockingRunner) Close() error                       { return nil }
