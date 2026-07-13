// Package controlserver assembles and runs the HTTP Control client surface.
package controlserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/surfaces/appserver"
)

type Config struct {
	Address        string
	Authenticator  appserver.Authenticator
	LocalPrincipal controlclient.Principal
	Heartbeat      time.Duration
}

// BearerTokenAuthenticator constructs the production static-token boundary
// for non-loopback Control servers. The principal is trusted configuration,
// never client-supplied request data.
func BearerTokenAuthenticator(token string, principal controlclient.Principal) (appserver.Authenticator, error) {
	token = strings.TrimSpace(token)
	principal.ID = strings.TrimSpace(principal.ID)
	if token == "" || principal.ID == "" {
		return nil, errors.New("controlserver: bearer token and principal are required")
	}
	return appserver.AuthenticatorFunc(func(request *http.Request) (controlclient.Principal, error) {
		provided := strings.TrimSpace(request.Header.Get("Authorization"))
		if len(provided) < len("Bearer ") || !strings.EqualFold(provided[:len("Bearer ")], "Bearer ") {
			return controlclient.Principal{}, errors.New("controlserver: bearer authentication failed")
		}
		provided = strings.TrimSpace(provided[len("Bearer "):])
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			return controlclient.Principal{}, errors.New("controlserver: bearer authentication failed")
		}
		return principal, nil
	}), nil
}

func Handler(stack *gatewayapp.Stack, config Config) (http.Handler, error) {
	if stack == nil {
		return nil, errors.New("controlserver: gateway stack is required")
	}
	server, err := appserver.New(appserver.Config{Service: stack.ControlClient(), Authenticator: config.Authenticator, LocalPrincipal: config.LocalPrincipal, Heartbeat: config.Heartbeat})
	if err != nil {
		return nil, err
	}
	return server.Handler(), nil
}

func ListenAndServe(ctx context.Context, stack *gatewayapp.Stack, config Config) error {
	if config.Address == "" {
		config.Address = "127.0.0.1:7777"
	}
	if err := appserver.ValidateListener(config.Address, config.Authenticator); err != nil {
		return err
	}
	handler, err := Handler(stack, config)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return ctx.Err()
	}
}
