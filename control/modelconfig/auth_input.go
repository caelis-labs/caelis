package modelconfig

import (
	"context"
	"errors"
	"strings"
)

// ErrAuthInputUnavailable indicates that the calling surface did not install
// an interactive input handler for a provider authentication flow.
var ErrAuthInputUnavailable = errors.New("interactive provider authentication input is unavailable")

// AuthInputRequest describes presentation-neutral input needed to finish a
// provider authentication flow. Secret requests must not be retained in
// composer history, transcripts, logs, or durable Session state.
type AuthInputRequest struct {
	Provider string
	Prompt   string
	Secret   bool
}

type authInputRequester func(context.Context, AuthInputRequest) (string, error)
type authInputContextKey struct{}

// WithAuthInput installs a synchronous interactive input requester on ctx.
// The requester must honor cancellation from the supplied context.
func WithAuthInput(ctx context.Context, request func(context.Context, AuthInputRequest) (string, error)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if request == nil {
		return ctx
	}
	return context.WithValue(ctx, authInputContextKey{}, authInputRequester(request))
}

// RequestAuthInput requests one presentation-neutral authentication value
// from the initiating surface.
func RequestAuthInput(ctx context.Context, request AuthInputRequest) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	handler, _ := ctx.Value(authInputContextKey{}).(authInputRequester)
	if handler == nil {
		return "", ErrAuthInputUnavailable
	}
	request.Provider = strings.TrimSpace(request.Provider)
	request.Prompt = strings.TrimSpace(request.Prompt)
	return handler(ctx, request)
}
