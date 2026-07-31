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
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/httpclient"
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
			created, err := client.CreateSession(ctx, controlclient.CreateSessionRequest{
				WriteBase: controlclient.WriteBase{
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
				Sessions: stack.Sessions,
				Runtime: headlessSessionTestRuntime{
					response: "headless " + transport + " ok",
					requests: requests,
				},
				Resolver: headlessSessionTestResolver{},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime.stack.mu.Lock()
			runtime.stack.gateway = gateway
			runtime.stack.mu.Unlock()

			turns, err := controlclient.NewSessionTurnClient(client)
			if err != nil {
				t.Fatal(err)
			}
			result, err := headless.RunSessionOnce(
				ctx,
				turns,
				controlclient.SessionTurnStartRequest{
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

func headlessSessionTestClient(
	t *testing.T,
	stack *Stack,
	transport string,
) controlclient.SessionClient {
	t.Helper()
	if transport == "in-process" {
		client, err := controlclient.BindSessionClient(
			stack.ControlClient(),
			controlclient.Principal{ID: "local-user"},
		)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	const token = "0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(
		token,
		controlclient.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.New(controlserver.HandlerConfig{
		Service:       stack.ControlClient(),
		TaskStreams:   stack.TaskStreams(),
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := testenv.NewHTTPServer(t, handler.Handler())
	client, err := httpclient.New(httpclient.Config{
		BaseURL:     server.URL,
		BearerToken: token,
		EventBuffer: 32,
		HTTPClient:  server.Client(),
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
