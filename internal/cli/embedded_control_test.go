package cli

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func roundTripEmbeddedControlFactory(t testing.TB) func() (embeddedControlEndpoint, error) {
	t.Helper()
	return func() (embeddedControlEndpoint, error) {
		return &roundTripEmbeddedControlEndpoint{t: t}, nil
	}
}

func roundTripProductClientOpener(t testing.TB) productClientOpener {
	t.Helper()
	return func(ctx context.Context, cfg gatewayapp.Config, options productClientOptions) (*productClients, error) {
		options.EmbeddedControlEndpoint = roundTripEmbeddedControlFactory(t)
		return openProductClients(ctx, cfg, options)
	}
}

func runWithRoundTripEmbeddedControl(
	t testing.TB,
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	t.Helper()
	return runWithProductClientOpener(ctx, args, stdin, stdout, stderr, roundTripProductClientOpener(t))
}

type roundTripEmbeddedControlEndpoint struct {
	t      testing.TB
	server *testenv.HTTPServer
}

func (*roundTripEmbeddedControlEndpoint) BaseURL() string {
	return "http://127.0.0.1:1455"
}

func (e *roundTripEmbeddedControlEndpoint) Start(handler http.Handler) error {
	e.server = testenv.NewHTTPServer(e.t, handler)
	return nil
}

func (e *roundTripEmbeddedControlEndpoint) HTTPClient() *http.Client {
	if e == nil || e.server == nil {
		return nil
	}
	return e.server.Client()
}

func (e *roundTripEmbeddedControlEndpoint) Close() error {
	if e != nil && e.server != nil {
		e.server.Close()
		e.server = nil
	}
	return nil
}
