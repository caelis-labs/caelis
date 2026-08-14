package agents

import (
	"context"
	"errors"
	"maps"
	"strings"
)

type AuthenticationType string

const (
	AuthenticationAgent    AuthenticationType = "agent"
	AuthenticationTerminal AuthenticationType = "terminal"
)

// Authentication records the exact ACP method selected for a connection.
// Terminal methods are out-of-band; their ID is retained for diagnostics and
// reconnect guidance but is never sent to authenticate.
type Authentication struct {
	MethodID string             `json:"method_id,omitempty"`
	Type     AuthenticationType `json:"type,omitempty"`
}

// AuthenticationMethod is the Control-normalized view of one method declared
// by an external ACP Agent.
type AuthenticationMethod struct {
	ID          string
	Name        string
	Description string
	Type        AuthenticationType
	Args        []string
	Env         map[string]string
}

// AuthenticationSelectionRequest asks the initiating surface to choose among
// methods declared by one external Agent.
type AuthenticationSelectionRequest struct {
	AgentID string
	Methods []AuthenticationMethod
}

// TerminalAuthenticationRequest is the complete, Control-authorized
// interactive invocation. Command always comes from the configured launcher;
// the Agent can only append Args and override Env through its descriptor.
type TerminalAuthenticationRequest struct {
	AgentID  string
	MethodID string
	Name     string
	Command  string
	Args     []string
	Env      map[string]string
	WorkDir  string
}

var (
	ErrAuthenticationSelectionUnavailable = errors.New("ACP authentication method selection is unavailable")
	ErrTerminalAuthenticationUnavailable  = errors.New("interactive ACP terminal authentication is unavailable")
)

type authenticationSelector func(context.Context, AuthenticationSelectionRequest) (string, error)
type terminalAuthenticator func(context.Context, TerminalAuthenticationRequest) error
type authenticationSelectorContextKey struct{}
type terminalAuthenticatorContextKey struct{}

func NormalizeAuthentication(in Authentication) Authentication {
	out := Authentication{
		MethodID: strings.TrimSpace(in.MethodID),
		Type:     AuthenticationType(strings.ToLower(strings.TrimSpace(string(in.Type)))),
	}
	if out.MethodID == "" {
		return Authentication{}
	}
	if out.Type == "" {
		out.Type = AuthenticationAgent
	}
	return out
}

// ValidateAuthentication rejects unsupported persisted method types while
// allowing stable v1 selections that omitted type to normalize to agent.
func ValidateAuthentication(in Authentication) error {
	authentication := NormalizeAuthentication(in)
	if authentication.MethodID == "" {
		return nil
	}
	switch authentication.Type {
	case AuthenticationAgent, AuthenticationTerminal:
		return nil
	default:
		return errors.New("unsupported ACP authentication method type " + string(authentication.Type))
	}
}

func NormalizeAuthenticationMethod(in AuthenticationMethod) AuthenticationMethod {
	out := AuthenticationMethod{
		ID:          strings.TrimSpace(in.ID),
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
		Type:        AuthenticationType(strings.ToLower(strings.TrimSpace(string(in.Type)))),
		Args:        append([]string(nil), in.Args...),
		Env:         maps.Clone(in.Env),
	}
	if out.Type == "" {
		out.Type = AuthenticationAgent
	}
	if out.Type != AuthenticationTerminal {
		out.Args = nil
		out.Env = nil
	}
	return out
}

// CloneAuthenticationMethods returns detached, normalized authentication
// descriptors suitable for retaining in Control or bridge runtime state.
func CloneAuthenticationMethods(in []AuthenticationMethod) []AuthenticationMethod {
	if len(in) == 0 {
		return nil
	}
	out := make([]AuthenticationMethod, 0, len(in))
	for _, method := range in {
		out = append(out, NormalizeAuthenticationMethod(method))
	}
	return out
}

// WithAuthenticationSelection installs the initiating surface's method
// selector. The callback must honor cancellation.
func WithAuthenticationSelection(
	ctx context.Context,
	selectMethod func(context.Context, AuthenticationSelectionRequest) (string, error),
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if selectMethod == nil {
		return ctx
	}
	return context.WithValue(ctx, authenticationSelectorContextKey{}, authenticationSelector(selectMethod))
}

// RequestAuthenticationSelection asks the initiating surface to select one
// declared method and returns its exact ID.
func RequestAuthenticationSelection(ctx context.Context, request AuthenticationSelectionRequest) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	selector, _ := ctx.Value(authenticationSelectorContextKey{}).(authenticationSelector)
	if selector == nil {
		return "", ErrAuthenticationSelectionUnavailable
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.Methods = CloneAuthenticationMethods(request.Methods)
	return selector(ctx, request)
}

// WithTerminalAuthentication installs an interactive terminal runner supplied
// by the initiating surface.
func WithTerminalAuthentication(
	ctx context.Context,
	authenticate func(context.Context, TerminalAuthenticationRequest) error,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if authenticate == nil {
		return ctx
	}
	return context.WithValue(ctx, terminalAuthenticatorContextKey{}, terminalAuthenticator(authenticate))
}

func TerminalAuthenticationAvailable(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	handler, _ := ctx.Value(terminalAuthenticatorContextKey{}).(terminalAuthenticator)
	return handler != nil
}

// RequestTerminalAuthentication runs one Control-authorized terminal flow.
func RequestTerminalAuthentication(ctx context.Context, request TerminalAuthenticationRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	handler, _ := ctx.Value(terminalAuthenticatorContextKey{}).(terminalAuthenticator)
	if handler == nil {
		return ErrTerminalAuthenticationUnavailable
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.MethodID = strings.TrimSpace(request.MethodID)
	request.Name = strings.TrimSpace(request.Name)
	request.Command = strings.TrimSpace(request.Command)
	request.Args = append([]string(nil), request.Args...)
	request.Env = maps.Clone(request.Env)
	request.WorkDir = strings.TrimSpace(request.WorkDir)
	return handler(ctx, request)
}
