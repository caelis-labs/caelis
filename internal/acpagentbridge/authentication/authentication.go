// Package authentication implements external ACP authentication recovery.
package authentication

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type agentAuthenticator interface {
	Authenticate(context.Context, string) error
}

type recoveryClient interface {
	agentAuthenticator
	Close(context.Context) error
	comparable
}

// RecoveryMode determines whether auth recovery may pause the initiating
// surface and restart the Agent process for terminal auth.
type RecoveryMode uint8

const (
	// RecoveryConfigured is used after onboarding. It retries only
	// persisted agent-managed methods and directs terminal methods back through
	// /connect, where an initiating surface can own the interactive process.
	RecoveryConfigured RecoveryMode = iota
	// RecoveryInteractive is used by /connect discovery and supports the
	// complete agent-managed or terminal out-of-band flow.
	RecoveryInteractive
)

// RecoveryConfig supplies the endpoint and interaction policy shared by
// authenticated ACP operations.
type RecoveryConfig struct {
	Mode           RecoveryMode
	Client         *client.Client
	Initialize     client.InitializeResponse
	Methods        []controlagents.AuthenticationMethod
	AgentID        string
	Connection     controlagents.Connection
	Authentication controlagents.Authentication
	Restart        func(context.Context) (*client.Client, client.InitializeResponse, error)
	CleanupTimeout time.Duration
}

// RecoveryResult returns the active client because terminal auth replaces the
// process and initialize response before retrying the operation.
type RecoveryResult[T any] struct {
	Client         *client.Client
	Initialize     client.InitializeResponse
	Value          T
	Authentication controlagents.Authentication
}

// RecoveryError identifies failed authentication coordination or repeated
// auth_required separately from an ordinary post-authentication operation
// error.
type RecoveryError struct {
	cause error
}

func (e *RecoveryError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *RecoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// IsRecoveryError reports whether an operation reached auth_required and then
// failed authentication coordination or remained authentication-gated.
func IsRecoveryError(err error) bool {
	var recoveryErr *RecoveryError
	return errors.As(err, &recoveryErr)
}

// IsRequired reports the stable ACP authentication-required error code.
func IsRequired(err error) bool {
	code, ok := jsonrpc.ErrorCode(err)
	return ok && code == client.ErrorCodeAuthRequired
}

// Methods converts tolerant wire descriptors into the Control-owned auth
// selection shape.
func Methods(initialize client.InitializeResponse) []controlagents.AuthenticationMethod {
	wireMethods := schema.DecodeAuthMethods(initialize.AuthMethods)
	out := make([]controlagents.AuthenticationMethod, 0, len(wireMethods))
	for _, method := range wireMethods {
		out = append(out, MethodFromWire(method))
	}
	return controlagents.CloneAuthenticationMethods(out)
}

// MethodFromWire makes the schema-to-Control authentication vocabulary
// conversion explicit at the bridge boundary.
func MethodFromWire(method schema.AuthMethod) controlagents.AuthenticationMethod {
	return controlagents.NormalizeAuthenticationMethod(controlagents.AuthenticationMethod{
		ID:          method.ID,
		Name:        method.Name,
		Description: method.Description,
		Type:        controlagents.AuthenticationType(method.Type),
		Args:        append([]string(nil), method.Args...),
		Env:         maps.Clone(method.Env),
	})
}

// Select resolves the exact declared method. A persisted selection wins; one
// unambiguous method is automatic; otherwise the initiating surface chooses.
func Select(
	ctx context.Context,
	agentID string,
	configured controlagents.Authentication,
	methods []controlagents.AuthenticationMethod,
) (controlagents.AuthenticationMethod, error) {
	methods = controlagents.CloneAuthenticationMethods(methods)
	if len(methods) == 0 {
		return controlagents.AuthenticationMethod{}, fmt.Errorf(
			"internal/acpagentbridge/authentication: ACP Agent %q requires authentication but advertised no supported methods",
			strings.TrimSpace(agentID),
		)
	}
	configured = controlagents.NormalizeAuthentication(configured)
	if configured.MethodID != "" {
		method, ok := lookup(methods, configured.MethodID)
		if !ok {
			return controlagents.AuthenticationMethod{}, fmt.Errorf(
				"internal/acpagentbridge/authentication: ACP Agent %q no longer advertises authentication method %q",
				strings.TrimSpace(agentID),
				configured.MethodID,
			)
		}
		if method.Type != configured.Type {
			return controlagents.AuthenticationMethod{}, fmt.Errorf(
				"internal/acpagentbridge/authentication: ACP Agent %q changed authentication method %q from %q to %q",
				strings.TrimSpace(agentID),
				configured.MethodID,
				configured.Type,
				method.Type,
			)
		}
		return method, nil
	}
	if len(methods) == 1 {
		return methods[0], nil
	}
	selectedID, err := controlagents.RequestAuthenticationSelection(ctx, controlagents.AuthenticationSelectionRequest{
		AgentID: strings.TrimSpace(agentID),
		Methods: methods,
	})
	if err != nil {
		return controlagents.AuthenticationMethod{}, fmt.Errorf(
			"internal/acpagentbridge/authentication: choose authentication method for ACP Agent %q: %w",
			strings.TrimSpace(agentID),
			err,
		)
	}
	method, ok := lookup(methods, selectedID)
	if !ok {
		return controlagents.AuthenticationMethod{}, fmt.Errorf(
			"internal/acpagentbridge/authentication: ACP Agent %q did not advertise selected authentication method %q",
			strings.TrimSpace(agentID),
			strings.TrimSpace(selectedID),
		)
	}
	return method, nil
}

// AuthenticateAgent executes stable ACP v1 in-band authentication.
func AuthenticateAgent(
	ctx context.Context,
	acpClient agentAuthenticator,
	agentID string,
	method controlagents.AuthenticationMethod,
) error {
	method = controlagents.NormalizeAuthenticationMethod(method)
	if method.Type != controlagents.AuthenticationAgent {
		return fmt.Errorf(
			"internal/acpagentbridge/authentication: authentication method %q for ACP Agent %q is %q, not agent-managed",
			method.ID,
			strings.TrimSpace(agentID),
			method.Type,
		)
	}
	if acpClient == nil {
		return fmt.Errorf("internal/acpagentbridge/authentication: ACP client is required")
	}
	if err := acpClient.Authenticate(ctx, method.ID); err != nil {
		return fmt.Errorf(
			"internal/acpagentbridge/authentication: authenticate ACP Agent %q with method %q: %w",
			strings.TrimSpace(agentID),
			method.ID,
			err,
		)
	}
	return nil
}

// TerminalRequest constructs the Preview terminal flow from the configured
// launcher. The Agent cannot replace the configured executable.
func TerminalRequest(
	connection controlagents.Connection,
	method controlagents.AuthenticationMethod,
) (controlagents.TerminalAuthenticationRequest, error) {
	connection = controlagents.NormalizeConnection(connection)
	method = controlagents.NormalizeAuthenticationMethod(method)
	if method.Type != controlagents.AuthenticationTerminal {
		return controlagents.TerminalAuthenticationRequest{}, fmt.Errorf(
			"internal/acpagentbridge/authentication: authentication method %q is not terminal",
			method.ID,
		)
	}
	env := maps.Clone(connection.Launcher.Env)
	if env == nil && len(method.Env) > 0 {
		env = map[string]string{}
	}
	maps.Copy(env, method.Env)
	return controlagents.TerminalAuthenticationRequest{
		AgentID:  connection.ID,
		MethodID: method.ID,
		Name:     method.Name,
		Command:  connection.Launcher.Command,
		Args:     append(append([]string(nil), connection.Launcher.Args...), method.Args...),
		Env:      env,
		WorkDir:  connection.Launcher.WorkDir,
	}, nil
}

// OpenNewSession creates one ACP Session through the shared declared
// auth_required recovery path.
func OpenNewSession(
	ctx context.Context,
	config RecoveryConfig,
	cwd string,
	meta map[string]any,
) (RecoveryResult[client.NewSessionResponse], error) {
	return recoverCall(ctx, config, func(openCtx context.Context, activeClient *client.Client) (client.NewSessionResponse, error) {
		return activeClient.NewSession(openCtx, strings.TrimSpace(cwd), meta)
	})
}

// ResumeSession resumes one ACP Session through the shared declared
// auth_required recovery path.
func ResumeSession(
	ctx context.Context,
	config RecoveryConfig,
	sessionID string,
	cwd string,
) (RecoveryResult[client.ResumeSessionResponse], error) {
	sessionID = strings.TrimSpace(sessionID)
	return recoverCall(ctx, config, func(openCtx context.Context, activeClient *client.Client) (client.ResumeSessionResponse, error) {
		return activeClient.ResumeSession(openCtx, sessionID, strings.TrimSpace(cwd), nil)
	})
}

// RecoverConfiguredCall executes one authenticated runtime operation and
// retries it at most once after a persisted agent-managed authentication
// method succeeds. Persisted terminal methods direct the user through
// interactive /connect instead.
func RecoverConfiguredCall[T any](
	ctx context.Context,
	acpClient *client.Client,
	methods []controlagents.AuthenticationMethod,
	agentID string,
	configured controlagents.Authentication,
	call func(context.Context, *client.Client) (T, error),
) (T, error) {
	recovered, err := recoverCall(ctx, RecoveryConfig{
		Mode:           RecoveryConfigured,
		Client:         acpClient,
		Methods:        methods,
		AgentID:        agentID,
		Authentication: configured,
	}, call)
	return recovered.Value, err
}

// recoverCall owns one authenticated operation plus its optional
// auth_required recovery. The operation is intentionally retried at most once
// after successful authentication; repeated auth_required errors are returned
// to the caller.
func recoverCall[T any](
	ctx context.Context,
	config RecoveryConfig,
	call func(context.Context, *client.Client) (T, error),
) (RecoveryResult[T], error) {
	recovered, err := recoverOperation(ctx, recoveryOperationConfig[*client.Client]{
		Mode:           config.Mode,
		Client:         config.Client,
		Initialize:     config.Initialize,
		Methods:        controlagents.CloneAuthenticationMethods(config.Methods),
		AgentID:        config.AgentID,
		Connection:     config.Connection,
		Authentication: config.Authentication,
		Restart:        config.Restart,
		CleanupTimeout: config.CleanupTimeout,
	}, call)
	return RecoveryResult[T](recovered), err
}

type recoveryOperationConfig[C recoveryClient] struct {
	Mode           RecoveryMode
	Client         C
	Initialize     client.InitializeResponse
	Methods        []controlagents.AuthenticationMethod
	AgentID        string
	Connection     controlagents.Connection
	Authentication controlagents.Authentication
	Restart        func(context.Context) (C, client.InitializeResponse, error)
	CleanupTimeout time.Duration
}

type recoveryOperationResult[C recoveryClient, T any] struct {
	Client         C
	Initialize     client.InitializeResponse
	Value          T
	Authentication controlagents.Authentication
}

func recoverOperation[C recoveryClient, T any](
	ctx context.Context,
	config recoveryOperationConfig[C],
	call func(context.Context, C) (T, error),
) (recoveryOperationResult[C, T], error) {
	result := recoveryOperationResult[C, T]{
		Client:         config.Client,
		Initialize:     config.Initialize,
		Authentication: controlagents.NormalizeAuthentication(config.Authentication),
	}
	if ctx == nil {
		return result, fmt.Errorf("internal/acpagentbridge/authentication: context is required")
	}
	var zeroClient C
	if config.Client == zeroClient {
		return result, fmt.Errorf("internal/acpagentbridge/authentication: ACP client is required")
	}
	if call == nil {
		return result, fmt.Errorf("internal/acpagentbridge/authentication: authenticated operation is required")
	}
	value, err := call(ctx, config.Client)
	if err == nil {
		result.Value = value
		return result, nil
	}
	if !IsRequired(err) {
		return result, err
	}

	agentID := strings.TrimSpace(config.AgentID)
	if agentID == "" {
		agentID = strings.TrimSpace(config.Connection.ID)
	}
	configured := controlagents.NormalizeAuthentication(config.Authentication)
	if config.Mode == RecoveryConfigured &&
		configured.MethodID != "" &&
		configured.Type == controlagents.AuthenticationTerminal {
		result.Authentication = configured
		return result, recoveryError(terminalReconnectRequiredError(agentID, configured.MethodID))
	}
	method, err := Select(ctx, agentID, config.Authentication, config.Methods)
	if err != nil {
		return result, recoveryError(err)
	}
	result.Authentication = controlagents.NormalizeAuthentication(controlagents.Authentication{
		MethodID: method.ID,
		Type:     method.Type,
	})

	switch method.Type {
	case controlagents.AuthenticationAgent:
		if err := AuthenticateAgent(ctx, config.Client, agentID, method); err != nil {
			return result, recoveryError(err)
		}
		value, err = call(ctx, config.Client)
		if err != nil {
			retryErr := fmt.Errorf(
				"internal/acpagentbridge/authentication: retry authenticated operation for ACP Agent %q after method %q: %w",
				agentID,
				method.ID,
				err,
			)
			if IsRequired(err) {
				return result, recoveryError(retryErr)
			}
			return result, retryErr
		}
		result.Value = value
		return result, nil

	case controlagents.AuthenticationTerminal:
		if config.Mode != RecoveryInteractive {
			return result, recoveryError(terminalReconnectRequiredError(agentID, method.ID))
		}
		if config.Restart == nil {
			return result, recoveryError(fmt.Errorf(
				"internal/acpagentbridge/authentication: restart is required for terminal authentication method %q",
				method.ID,
			))
		}
		connection := config.Connection
		if strings.TrimSpace(connection.ID) == "" {
			connection.ID = agentID
		}
		terminalRequest, err := TerminalRequest(connection, method)
		if err != nil {
			return result, recoveryError(err)
		}
		_ = acpcleanup.CloseClientWithin(ctx, config.Client, config.CleanupTimeout)
		result.Client = zeroClient
		if err := controlagents.RequestTerminalAuthentication(ctx, terminalRequest); err != nil {
			return result, recoveryError(fmt.Errorf(
				"internal/acpagentbridge/authentication: terminal authentication for ACP Agent %q: %w",
				agentID,
				err,
			))
		}
		restarted, initialize, err := config.Restart(ctx)
		if err != nil {
			return result, recoveryError(fmt.Errorf(
				"internal/acpagentbridge/authentication: restart ACP Agent %q after terminal authentication: %w",
				agentID,
				err,
			))
		}
		result.Client = restarted
		result.Initialize = initialize
		value, err = call(ctx, restarted)
		if err != nil {
			_ = acpcleanup.CloseClientWithin(ctx, restarted, config.CleanupTimeout)
			result.Client = zeroClient
			return result, recoveryError(fmt.Errorf(
				"internal/acpagentbridge/authentication: retry authenticated operation for ACP Agent %q after terminal method %q: %w",
				agentID,
				method.ID,
				err,
			))
		}
		result.Value = value
		return result, nil

	default:
		return result, recoveryError(fmt.Errorf(
			"internal/acpagentbridge/authentication: unsupported authentication method type %q for ACP Agent %q",
			method.Type,
			agentID,
		))
	}
}

func recoveryError(err error) error {
	if err == nil {
		return nil
	}
	return &RecoveryError{cause: err}
}

func terminalReconnectRequiredError(agentID string, methodID string) error {
	return fmt.Errorf(
		"internal/acpagentbridge/authentication: ACP Agent %q requires terminal authentication method %q; run /connect again to complete its interactive login",
		strings.TrimSpace(agentID),
		strings.TrimSpace(methodID),
	)
}

func lookup(methods []controlagents.AuthenticationMethod, id string) (controlagents.AuthenticationMethod, bool) {
	id = strings.TrimSpace(id)
	for _, method := range methods {
		if strings.TrimSpace(method.ID) == id {
			return controlagents.NormalizeAuthenticationMethod(method), true
		}
	}
	return controlagents.AuthenticationMethod{}, false
}
