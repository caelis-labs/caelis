package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/appserver"
)

type attachOnlyClient struct {
	appserver.SessionClient
	inspect func(appserver.StateRequest) (appserver.SessionState, error)
}

func (c attachOnlyClient) InspectSession(_ context.Context, req appserver.StateRequest) (appserver.SessionState, error) {
	return c.inspect(req)
}

func TestAttachRequiresEndpointAndExistingSessionWithoutPrompt(t *testing.T) {
	t.Setenv("CAELIS_CONTROL_URL", "")
	t.Setenv("CAELIS_SESSION_ID", "")
	t.Setenv("CAELIS_CONTROL_EMBEDDED", "")
	for _, args := range [][]string{
		{"attach"},
		{"attach", "--session", "worker"},
		{"attach", "--control-url", "http://127.0.0.1:7777"},
		{"attach", "--control-url", "http://127.0.0.1:7777", "--session", "worker", "-p", "must not dispatch"},
		{"attach", "--control-url", "http://127.0.0.1:7777", "--session", "worker", "--embedded"},
	} {
		err := runWithProductClientOpener(t.Context(), args, strings.NewReader("must not become a prompt"), io.Discard, io.Discard, func(context.Context, gatewayapp.Config, productClientOptions) (*productClients, error) {
			t.Fatal("invalid attach opened a Host")
			return nil, nil
		})
		if err == nil || !strings.Contains(err.Error(), "attach requires") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	missing := errors.New("fixture: Session not found")
	calls := 0
	err := runWithProductClientOpener(t.Context(), []string{"attach", "--control-url", "http://127.0.0.1:7777", "--session", "worker"}, strings.NewReader("must not become a prompt"), io.Discard, io.Discard, func(_ context.Context, _ gatewayapp.Config, opts productClientOptions) (*productClients, error) {
		if opts.Mode != productClientModeRemote {
			t.Fatal("attach attempted managed or embedded startup")
		}
		return &productClients{Mode: productClientModeRemote, Clients: appserver.AppServerClients{Sessions: attachOnlyClient{inspect: func(req appserver.StateRequest) (appserver.SessionState, error) {
			calls++
			if req.SessionID != "worker" {
				t.Fatal(req)
			}
			return appserver.SessionState{}, missing
		}}}}, nil
	})
	if !errors.Is(err, missing) || calls != 1 {
		t.Fatalf("missing attach: calls=%d err=%v", calls, err)
	}
}
