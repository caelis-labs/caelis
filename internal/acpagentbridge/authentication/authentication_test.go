package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

type recordingAuthenticator struct {
	methods []string
}

func (r *recordingAuthenticator) Authenticate(_ context.Context, methodID string) error {
	r.methods = append(r.methods, methodID)
	return nil
}

type recordingRecoveryClient struct {
	authenticated []string
	closed        int
	closeErr      error
}

func (r *recordingRecoveryClient) Authenticate(_ context.Context, methodID string) error {
	r.authenticated = append(r.authenticated, methodID)
	return nil
}

func (r *recordingRecoveryClient) Close(context.Context) error {
	r.closed++
	return r.closeErr
}

func TestSelectUsesSurfaceChoiceForMultipleDeclaredMethods(t *testing.T) {
	t.Parallel()

	initialize := client.InitializeResponse{AuthMethods: []json.RawMessage{
		json.RawMessage(`{"id":"browser","name":"Browser"}`),
		json.RawMessage(`{"id":"terminal","name":"Terminal","type":"terminal","args":["login"]}`),
	}}
	ctx := controlagents.WithAuthenticationSelection(context.Background(), func(
		_ context.Context,
		request controlagents.AuthenticationSelectionRequest,
	) (string, error) {
		if len(request.Methods) != 2 || request.Methods[1].Type != controlagents.AuthenticationTerminal {
			t.Fatalf("selection request = %#v", request)
		}
		return "terminal", nil
	})
	method, err := Select(ctx, "codex", controlagents.Authentication{}, Methods(initialize))
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if method.ID != "terminal" || method.Type != controlagents.AuthenticationTerminal {
		t.Fatalf("method = %#v", method)
	}
}

func TestAuthenticateAgentCallsStableAuthenticate(t *testing.T) {
	t.Parallel()

	acpClient := &recordingAuthenticator{}
	err := AuthenticateAgent(
		context.Background(),
		acpClient,
		"codex",
		controlagents.AuthenticationMethod{
			ID: "browser", Name: "Browser", Type: controlagents.AuthenticationAgent,
		},
	)
	if err != nil {
		t.Fatalf("AuthenticateAgent() error = %v", err)
	}
	if !reflect.DeepEqual(acpClient.methods, []string{"browser"}) {
		t.Fatalf("authenticate methods = %#v", acpClient.methods)
	}
}

func TestTerminalRequestAppendsArgumentsAndOverridesEnvironment(t *testing.T) {
	t.Parallel()

	request, err := TerminalRequest(
		controlagents.Connection{
			ID: "codex",
			Launcher: controlagents.Launcher{
				Command: "npx", Args: []string{"-y", "codex-acp"},
				Env: map[string]string{"BASE": "1", "OVERRIDE": "base"},
			},
		},
		controlagents.AuthenticationMethod{
			ID: "terminal", Name: "Terminal", Type: controlagents.AuthenticationTerminal,
			Args: []string{"login"}, Env: map[string]string{"OVERRIDE": "auth"},
		},
	)
	if err != nil {
		t.Fatalf("TerminalRequest() error = %v", err)
	}
	if !reflect.DeepEqual(request.Args, []string{"-y", "codex-acp", "login"}) {
		t.Fatalf("args = %#v", request.Args)
	}
	if !reflect.DeepEqual(request.Env, map[string]string{"BASE": "1", "OVERRIDE": "auth"}) {
		t.Fatalf("env = %#v", request.Env)
	}
}

func TestRecoverOperationConfiguredTerminalDirectsReconnectWithoutAuthenticate(t *testing.T) {
	t.Parallel()

	acpClient := &recordingRecoveryClient{}
	calls := 0
	_, err := recoverOperation(context.Background(), recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:   RecoveryConfigured,
		Client: acpClient,
		Authentication: controlagents.Authentication{
			MethodID: "terminal-login",
			Type:     controlagents.AuthenticationTerminal,
		},
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		calls++
		return "", authRequiredError()
	})
	if err == nil || !IsRecoveryError(err) || !strings.Contains(err.Error(), "run /connect") {
		t.Fatalf("recoverOperation() error = %v, want terminal recovery guidance", err)
	}
	if calls != 1 || len(acpClient.authenticated) != 0 {
		t.Fatalf("calls = %d, authenticate = %#v; want one call and no authenticate", calls, acpClient.authenticated)
	}
}

func TestRecoverOperationConfiguredAgentAuthenticatesAndRetriesOnce(t *testing.T) {
	t.Parallel()

	acpClient := &recordingRecoveryClient{}
	calls := 0
	recovered, err := recoverOperation(context.Background(), recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:    RecoveryConfigured,
		Client:  acpClient,
		Methods: agentAuthenticationMethods(),
		Authentication: controlagents.Authentication{
			MethodID: "agent-login",
			Type:     controlagents.AuthenticationAgent,
		},
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		calls++
		if calls == 1 {
			return "", authRequiredError()
		}
		return "authenticated", nil
	})
	if err != nil {
		t.Fatalf("recoverOperation() error = %v", err)
	}
	if recovered.Value != "authenticated" || calls != 2 || !reflect.DeepEqual(acpClient.authenticated, []string{"agent-login"}) {
		t.Fatalf("recovered = %#v, calls = %d, authenticate = %#v", recovered, calls, acpClient.authenticated)
	}
}

func TestRecoverOperationProbeReturnsDeclaredChallengeWithoutSelection(t *testing.T) {
	t.Parallel()

	selectorCalls := 0
	ctx := controlagents.WithAuthenticationSelection(context.Background(), func(
		context.Context,
		controlagents.AuthenticationSelectionRequest,
	) (string, error) {
		selectorCalls++
		return "agent-login", nil
	})
	acpClient := &recordingRecoveryClient{}
	calls := 0
	methods := append(agentAuthenticationMethods(), terminalAuthenticationMethods()...)
	recovered, err := recoverOperation(ctx, recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:    RecoveryProbe,
		Client:  acpClient,
		Methods: methods,
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		calls++
		return "", authRequiredError()
	})
	if err != nil {
		t.Fatalf("recoverOperation() error = %v", err)
	}
	if !recovered.NeedsAuthentication || !reflect.DeepEqual(recovered.AuthenticationMethods, methods) {
		t.Fatalf("recovered challenge = %#v, want declared methods", recovered)
	}
	if calls != 1 || selectorCalls != 0 || len(acpClient.authenticated) != 0 {
		t.Fatalf(
			"calls = %d, selector = %d, authenticate = %#v; want probe only",
			calls,
			selectorCalls,
			acpClient.authenticated,
		)
	}
}

func TestRecoverOperationProbeExecutesOnlySelectedAgentMethod(t *testing.T) {
	t.Parallel()

	selectorCalls := 0
	ctx := controlagents.WithAuthenticationSelection(context.Background(), func(
		context.Context,
		controlagents.AuthenticationSelectionRequest,
	) (string, error) {
		selectorCalls++
		return "terminal-login", nil
	})
	acpClient := &recordingRecoveryClient{}
	calls := 0
	recovered, err := recoverOperation(ctx, recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:    RecoveryProbe,
		Client:  acpClient,
		Methods: append(agentAuthenticationMethods(), terminalAuthenticationMethods()...),
		Authentication: controlagents.Authentication{
			MethodID: "agent-login",
		},
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		calls++
		if calls == 1 {
			return "", authRequiredError()
		}
		return "authenticated", nil
	})
	if err != nil {
		t.Fatalf("recoverOperation() error = %v", err)
	}
	if recovered.Value != "authenticated" || recovered.Authentication != (controlagents.Authentication{
		MethodID: "agent-login",
		Type:     controlagents.AuthenticationAgent,
	}) {
		t.Fatalf("recovered = %#v", recovered)
	}
	if calls != 2 || selectorCalls != 0 || !reflect.DeepEqual(acpClient.authenticated, []string{"agent-login"}) {
		t.Fatalf(
			"calls = %d, selector = %d, authenticate = %#v; want one exact recovery",
			calls,
			selectorCalls,
			acpClient.authenticated,
		)
	}
}

func TestRecoverOperationProbeTerminalRequiresExplicitRunnerBeforeEffect(t *testing.T) {
	t.Parallel()

	acpClient := &recordingRecoveryClient{}
	restartCalls := 0
	calls := 0
	_, err := recoverOperation(context.Background(), recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:       RecoveryProbe,
		Client:     acpClient,
		Methods:    terminalAuthenticationMethods(),
		Connection: controlagents.Connection{ID: "codex", Launcher: controlagents.Launcher{Command: "codex-acp"}},
		Authentication: controlagents.Authentication{
			MethodID: "terminal-login",
		},
		Restart: func(context.Context) (*recordingRecoveryClient, client.InitializeResponse, error) {
			restartCalls++
			return &recordingRecoveryClient{}, client.InitializeResponse{}, nil
		},
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		calls++
		return "", authRequiredError()
	})
	var unavailable *TerminalUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, controlagents.ErrTerminalAuthenticationUnavailable) {
		t.Fatalf("recoverOperation() error = %v, want typed terminal unavailable", err)
	}
	if unavailable.MethodID != "terminal-login" || calls != 0 || acpClient.closed != 0 || restartCalls != 0 {
		t.Fatalf(
			"unavailable = %#v, calls = %d, closes = %d, restarts = %d; want no terminal effect",
			unavailable,
			calls,
			acpClient.closed,
			restartCalls,
		)
	}
}

func TestRecoverOperationProbeTerminalStopsOnUnknownClientCleanup(t *testing.T) {
	t.Parallel()

	cleanupErr := errors.New("client cleanup not acknowledged")
	acpClient := &recordingRecoveryClient{closeErr: cleanupErr}
	terminalCalls := 0
	restartCalls := 0
	ctx := controlagents.WithTerminalAuthentication(context.Background(), func(
		context.Context,
		controlagents.TerminalAuthenticationRequest,
	) error {
		terminalCalls++
		return nil
	})
	recovered, err := recoverOperation(ctx, recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:       RecoveryProbe,
		Client:     acpClient,
		Methods:    terminalAuthenticationMethods(),
		Connection: controlagents.Connection{ID: "codex", Launcher: controlagents.Launcher{Command: "codex-acp"}},
		Authentication: controlagents.Authentication{
			MethodID: "terminal-login",
		},
		Restart: func(context.Context) (*recordingRecoveryClient, client.InitializeResponse, error) {
			restartCalls++
			return &recordingRecoveryClient{}, client.InitializeResponse{}, nil
		},
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		return "", authRequiredError()
	})
	if !errors.Is(err, cleanupErr) || !IsRecoveryError(err) || !recovered.CleanupUnknown {
		t.Fatalf("recoverOperation() = %#v, %v; want unknown cleanup recovery error", recovered, err)
	}
	if recovered.Client != nil || acpClient.closed != 1 || terminalCalls != 0 || restartCalls != 0 {
		t.Fatalf(
			"client = %#v, closes = %d, terminal = %d, restart = %d; want stop before terminal effect",
			recovered.Client,
			acpClient.closed,
			terminalCalls,
			restartCalls,
		)
	}
}

func TestRecoverOperationExplicitTerminalClosesRestartsAndRetries(t *testing.T) {
	t.Parallel()

	firstClient := &recordingRecoveryClient{}
	restartedClient := &recordingRecoveryClient{}
	terminalCalls := 0
	restartCalls := 0
	ctx := controlagents.WithTerminalAuthentication(context.Background(), func(
		context.Context,
		controlagents.TerminalAuthenticationRequest,
	) error {
		terminalCalls++
		return nil
	})
	recovered, err := recoverOperation(ctx, recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:    RecoveryProbe,
		Client:  firstClient,
		Methods: terminalAuthenticationMethods(),
		Authentication: controlagents.Authentication{
			MethodID: "terminal-login",
		},
		Connection: controlagents.Connection{ID: "codex", Launcher: controlagents.Launcher{Command: "codex-acp"}},
		Restart: func(context.Context) (*recordingRecoveryClient, client.InitializeResponse, error) {
			restartCalls++
			return restartedClient, client.InitializeResponse{ProtocolVersion: 1}, nil
		},
	}, func(_ context.Context, activeClient *recordingRecoveryClient) (string, error) {
		if activeClient == firstClient {
			return "", authRequiredError()
		}
		if activeClient != restartedClient {
			t.Fatalf("unexpected active client %p", activeClient)
		}
		return "reconnected", nil
	})
	if err != nil {
		t.Fatalf("recoverOperation() error = %v", err)
	}
	if recovered.Value != "reconnected" || recovered.Client != restartedClient {
		t.Fatalf("recovered = %#v", recovered)
	}
	if firstClient.closed != 1 || restartedClient.closed != 0 || terminalCalls != 1 || restartCalls != 1 {
		t.Fatalf(
			"first closes = %d, restarted closes = %d, terminal = %d, restart = %d",
			firstClient.closed,
			restartedClient.closed,
			terminalCalls,
			restartCalls,
		)
	}
}

func TestRecoverOperationRepeatedAuthRequiredDoesNotLoop(t *testing.T) {
	t.Parallel()

	acpClient := &recordingRecoveryClient{}
	calls := 0
	_, err := recoverOperation(context.Background(), recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:    RecoveryConfigured,
		Client:  acpClient,
		Methods: agentAuthenticationMethods(),
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		calls++
		return "", authRequiredError()
	})
	if err == nil || !IsRecoveryError(err) {
		t.Fatalf("recoverOperation() error = %v, want RecoveryError", err)
	}
	if calls != 2 || len(acpClient.authenticated) != 1 {
		t.Fatalf("calls = %d, authenticate = %#v; want one retry", calls, acpClient.authenticated)
	}
}

func TestRecoverOperationPostAuthenticationBusinessFailureIsNotRecoveryError(t *testing.T) {
	t.Parallel()

	businessErr := errors.New("session no longer exists")
	acpClient := &recordingRecoveryClient{}
	calls := 0
	_, err := recoverOperation(context.Background(), recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:    RecoveryConfigured,
		Client:  acpClient,
		Methods: agentAuthenticationMethods(),
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		calls++
		if calls == 1 {
			return "", authRequiredError()
		}
		return "", businessErr
	})
	if !errors.Is(err, businessErr) || IsRecoveryError(err) {
		t.Fatalf("recoverOperation() error = %v, want ordinary post-auth operation error", err)
	}
}

func TestRecoverOperationNonAuthenticationErrorPassesThrough(t *testing.T) {
	t.Parallel()

	operationErr := errors.New("operation failed")
	acpClient := &recordingRecoveryClient{}
	_, err := recoverOperation(context.Background(), recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:   RecoveryConfigured,
		Client: acpClient,
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		return "", operationErr
	})
	if !errors.Is(err, operationErr) || IsRecoveryError(err) {
		t.Fatalf("recoverOperation() error = %v, want original non-recovery error", err)
	}
}

func TestRecoverOperationExplicitTerminalRequiresRestart(t *testing.T) {
	t.Parallel()

	acpClient := &recordingRecoveryClient{}
	ctx := controlagents.WithTerminalAuthentication(context.Background(), func(
		context.Context,
		controlagents.TerminalAuthenticationRequest,
	) error {
		return nil
	})
	_, err := recoverOperation(ctx, recoveryOperationConfig[*recordingRecoveryClient]{
		Mode:    RecoveryProbe,
		Client:  acpClient,
		Methods: terminalAuthenticationMethods(),
		Authentication: controlagents.Authentication{
			MethodID: "terminal-login",
		},
	}, func(context.Context, *recordingRecoveryClient) (string, error) {
		return "", authRequiredError()
	})
	if err == nil || !IsRecoveryError(err) || !strings.Contains(err.Error(), "restart is required") {
		t.Fatalf("recoverOperation() error = %v, want missing restart RecoveryError", err)
	}
	if acpClient.closed != 0 {
		t.Fatalf("client closed before validating restart: %d", acpClient.closed)
	}
}

func authRequiredError() error {
	return &acpsdk.RequestError{
		Code:    client.ErrorCodeAuthRequired,
		Message: "Authentication required",
	}
}

func agentAuthenticationMethods() []controlagents.AuthenticationMethod {
	return []controlagents.AuthenticationMethod{{
		ID:   "agent-login",
		Name: "Agent login",
		Type: controlagents.AuthenticationAgent,
	}}
}

func terminalAuthenticationMethods() []controlagents.AuthenticationMethod {
	return []controlagents.AuthenticationMethod{{
		ID:   "terminal-login",
		Name: "Terminal login",
		Type: controlagents.AuthenticationTerminal,
	}}
}
