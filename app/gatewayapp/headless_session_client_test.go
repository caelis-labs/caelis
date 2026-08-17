package gatewayapp

import (
	"context"
	"iter"
	"slices"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestHeadlessSessionTurnMatchesInProcessAndHTTPClients(t *testing.T) {
	for _, transport := range []string{"in-process", "http"} {
		t.Run(transport, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			workspace := t.TempDir()
			stack, err := NewLocalStack(Config{
				StoreDir:     t.TempDir(),
				WorkspaceKey: "headless-workspace",
				WorkspaceCWD: workspace,
				SkillDirs:    []string{},
				Sandbox:      SandboxConfig{RequestedType: "host"},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stack.Close() })

			client := headlessSessionTestClient(t, stack, transport)
			created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
				WriteBase: appserver.WriteBase{
					OperationID: "create-headless-" + transport,
				},
				PreferredSessionID: "headless-" + transport,
				WorkspaceKey:       "headless-workspace",
				CWD:                workspace,
			})
			if err != nil {
				t.Fatal(err)
			}
			requests := make(chan agent.RunRequest, 1)
			contentParts := []model.ContentPart{
				{Type: model.ContentPartText, Text: "describe "},
				{Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1n", FileName: "shot.png"},
			}
			runtime, active, err := stack.sessionRuntimes.activateSession(ctx, created.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			gateway, err := kernelimpl.New(kernelimpl.Config{
				Sessions: stack.composition.sessions,
				Runtime: headlessSessionTestRuntime{
					response: "headless " + transport + " ok",
					requests: requests,
				},
				Resolver: headlessSessionTestResolver{},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime.instance.mu.Lock()
			runtime.instance.gateway = gateway
			runtime.instance.mu.Unlock()

			turns, err := appserver.NewSessionTurnClient(client)
			if err != nil {
				t.Fatal(err)
			}
			result, err := headless.RunSessionOnce(
				ctx,
				turns,
				appserver.SessionTurnStartRequest{
					SessionID:    active.SessionID,
					Input:        "return the fixed test response",
					ContentParts: contentParts,
				},
				headless.Options{},
			)
			if err != nil {
				t.Fatal(err)
			}
			want := "headless " + transport + " ok"
			if result.Output != want ||
				result.LifecycleState != "completed" ||
				result.Target.HandleID == "" ||
				result.Target.RunID == "" ||
				result.Target.TurnID == "" ||
				result.LastCursor == "" {
				t.Fatalf("Headless result = %#v, want output %q", result, want)
			}
			select {
			case request := <-requests:
				if request.Input != "return the fixed test response" ||
					!slices.Equal(request.ContentParts, contentParts) {
					t.Fatalf("Runtime request = %#v", request)
				}
			default:
				t.Fatal("Runtime did not receive the typed prompt content")
			}
		})
	}
}

func TestSessionTurnReconnectRepairsDeletedDormantModelBeforePromptCAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	initialID := stack.composition.lookup.DefaultID()
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: "ollama", Model: "deleted-before-prompt"})
	if err != nil {
		t.Fatal(err)
	}
	deletedID := profile.Backend.Provider.ModelConfigID
	active = mustCurrentSession(t, stack, active.SessionID)
	selected, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-deleted-before-prompt",
			SessionID:               active.SessionID,
			ExpectedRevision:        &active.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: deletedID,
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel() = %#v, %v", selected, err)
	}
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, deletedID); err != nil {
		t.Fatal(err)
	}

	requests := make(chan agent.RunRequest, 1)
	client, err := appserver.BindSessionClient(stack.ControlClient(), principal)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := appserver.NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the newly assembled model-backed Runtime only after reconnect has
	// repaired state, while retaining the exact product SessionTurn client path.
	reconnected, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.Subscription != nil {
		_ = reconnected.Subscription.Close()
	}
	if reconnected.State.Revision != selected.Revision+1 {
		t.Fatalf("Reconnect revision = %d, want repaired %d", reconnected.State.Revision, selected.Revision+1)
	}
	runtime := activateSessionRuntime(t, stack, active.SessionID)
	gateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.composition.sessions,
		Runtime:  headlessSessionTestRuntime{response: "recovered prompt ok", requests: requests},
		Resolver: headlessSessionTestResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.instance.mu.Lock()
	runtime.instance.gateway = gateway
	runtime.instance.mu.Unlock()
	result, err := headless.RunSessionOnce(ctx, turns, appserver.SessionTurnStartRequest{
		SessionID: active.SessionID, Input: "continue after recovery",
	}, headless.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "recovered prompt ok" {
		t.Fatalf("SessionTurn output = %q", result.Output)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernelimpl.CurrentModelAlias(state); got != initialID {
		t.Fatalf("Session model after prompt = %q, want fallback %q", got, initialID)
	}
}

func headlessSessionTestClient(
	t *testing.T,
	stack *Stack,
	transport string,
) appserver.SessionClient {
	t.Helper()
	if transport == "in-process" {
		client, err := appserver.BindSessionClient(
			stack.ControlClient(),
			appserver.Principal{ID: "local-user"},
		)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	const token = "0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(
		token,
		appserver.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.New(controlserver.HandlerConfig{
		Services:      gatewayTestAppServerServices(stack.ControlClient(), gatewayTestStatusService{}, stack.TaskStreams()),
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := testenv.NewHTTPServer(t, handler.Handler())
	client, err := httpclient.New(httpclient.Config{
		BaseURL:       server.URL,
		BearerToken:   token,
		EventBuffer:   32,
		HTTPClient:    server.Client(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type headlessSessionTestRuntime struct {
	response string
	requests chan<- agent.RunRequest
}

func (runtime headlessSessionTestRuntime) Run(
	_ context.Context,
	request agent.RunRequest,
) (agent.RunResult, error) {
	if runtime.requests != nil {
		runtime.requests <- request
	}
	message := model.NewTextMessage(model.RoleAssistant, runtime.response)
	return agent.RunResult{
		Session: session.Session{SessionRef: request.SessionRef},
		Handle: &headlessSessionTestRunner{
			ref: request.SessionRef,
			events: []*session.Event{{
				ID:         "assistant-fixed",
				SessionID:  request.SessionRef.SessionID,
				Type:       session.EventTypeAssistant,
				Visibility: session.VisibilityCanonical,
				Message:    &message,
				Text:       runtime.response,
			}},
		},
	}, nil
}

func (headlessSessionTestRuntime) RunState(
	context.Context,
	session.SessionRef,
) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type headlessSessionTestResolver struct{}

func (headlessSessionTestResolver) ResolveTurn(
	_ context.Context,
	intent kernelimpl.TurnIntent,
) (kernelimpl.ResolvedTurn, error) {
	return kernelimpl.ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef:   intent.SessionRef,
			Input:        intent.Input,
			DisplayInput: intent.DisplayInput,
			ContentParts: append([]model.ContentPart(nil), intent.ContentParts...),
		},
	}, nil
}

type headlessSessionTestRunner struct {
	ref    session.SessionRef
	events []*session.Event
}

func (*headlessSessionTestRunner) RunID() string { return "headless-test-runner" }

func (runner *headlessSessionTestRunner) Events() iter.Seq2[*session.Event, error] {
	events := append([]*session.Event(nil), runner.events...)
	return func(yield func(*session.Event, error) bool) {
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (*headlessSessionTestRunner) Submit(agent.Submission) error { return nil }

func (*headlessSessionTestRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (*headlessSessionTestRunner) Close() error { return nil }
