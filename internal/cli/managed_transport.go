package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
)

// managedEndpoint is one authenticated discovery snapshot. It is replaced only
// after the new instance passes the same checks as an initial managed attach.
type managedEndpoint struct {
	record controlserver.DiscoveryRecord
	token  string
}

func attachManagedHostClient(ctx context.Context, options productClientOptions) (*httpclient.Client, controlserver.DiscoveryRecord, error) {
	endpoint, err := resolveManagedEndpoint(ctx, options)
	if err != nil {
		return nil, controlserver.DiscoveryRecord{}, err
	}
	client := http.DefaultClient
	if options.HTTPClient != nil {
		client = options.HTTPClient
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	following := *client
	following.Transport = &managedTransport{options: options, transport: transport, current: endpoint}
	options.HTTPClient = &following
	remote, err := newManagedHTTPClient(endpoint.record, endpoint.token, options,
		appserver.CurrentCompatibility(appserver.RequiredManagedHostCapabilities()...))
	return remote, endpoint.record, err
}

func resolveManagedEndpoint(ctx context.Context, options productClientOptions) (managedEndpoint, error) {
	inspection := inspectManagedHost(ctx, options)
	switch inspection.Probe.State {
	case servicelifecycle.ProbeMissing:
		return managedEndpoint{}, os.ErrNotExist
	case servicelifecycle.ProbeUnreachable:
		return managedEndpoint{}, inspection.Probe.Err
	}
	for _, capability := range appserver.RequiredManagedHostCapabilities() {
		if !slices.Contains(inspection.Record.Capabilities, capability) {
			return managedEndpoint{}, &managedCompatibilityError{
				cause: fmt.Errorf("discovery is missing capability %q", capability),
			}
		}
	}
	policy := appserver.CurrentCompatibility(appserver.RequiredManagedHostCapabilities()...)
	if err := policy.Accept(inspection.Info); err != nil {
		return managedEndpoint{}, &managedCompatibilityError{cause: err}
	}
	token := inspection.Token
	if options.ACPIngress {
		var err error
		token, err = controlserver.LoadBearerToken(controlserver.DefaultACPIngressTokenFile(options.StoreDir))
		if err != nil {
			return managedEndpoint{}, fmt.Errorf("cli: load ACP ingress credential: %w", err)
		}
	}
	remote, err := newManagedHTTPClient(inspection.Record, token, options, policy)
	if err != nil {
		return managedEndpoint{}, err
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	info, err := remote.Initialize(attemptCtx)
	if err != nil {
		return managedEndpoint{}, err
	}
	if err := validateManagedServerInfo(inspection.Record, info); err != nil {
		return managedEndpoint{}, err
	}
	return managedEndpoint{record: inspection.Record, token: token}, nil
}

// managedTransport follows the Store's published Host, never starts one, and
// never retries a dispatched request. In particular, a lost write response is
// not permission to repeat the command on the replacement Host. Session feed
// recovery remains with the consumer that owns the applied history boundary.
type managedTransport struct {
	options   productClientOptions
	transport http.RoundTripper
	mu        sync.Mutex
	current   managedEndpoint
}

func (t *managedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	endpoint, err := t.endpoint(request.Context())
	if err != nil {
		return nil, managedConnectionError(request.Context(), err)
	}
	origin, err := url.Parse(endpoint.record.Endpoint)
	if err != nil {
		return nil, err
	}
	forward := request.Clone(request.Context())
	forward.URL.Scheme, forward.URL.Host = origin.Scheme, origin.Host
	forward.Host = origin.Host
	forward.Header.Set("Authorization", "Bearer "+endpoint.token)
	response, err := t.transport.RoundTrip(forward)
	if err != nil {
		return nil, managedConnectionError(request.Context(), err)
	}
	return response, nil
}

func (t *managedTransport) endpoint(ctx context.Context) (managedEndpoint, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return managedEndpoint{}, err
	}
	record, _, err := loadManagedDiscovery(t.options.StoreDir)
	if err != nil {
		return managedEndpoint{}, err
	}
	if reflect.DeepEqual(record, t.current.record) {
		return t.current, nil
	}
	// Use the original HTTP client for this handshake, not this transport.
	// A replacement must not inherit the previous instance's authentication or
	// compatibility decision, even when it reuses the same listener address.
	next, err := resolveManagedEndpoint(ctx, t.options)
	if err != nil {
		return managedEndpoint{}, err
	}
	t.current = next
	return next, nil
}

func managedConnectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, os.ErrNotExist) || managedTransportRetryable(err) {
		return errorcode.Wrap(errorcode.Unavailable, "cli: local Control Host is unavailable; it may be restarting", err)
	}
	return err
}
