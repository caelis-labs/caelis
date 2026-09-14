package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestManagedTransportFollowsReplacementBeforeDispatch(t *testing.T) {
	for _, ingress := range []bool{false, true} {
		t.Run(map[bool]string{false: "product", true: "ACP ingress"}[ingress], func(t *testing.T) {
			options, hosts := managedTransportFixture(t)
			options.ACPIngress = ingress
			hosts[0].publish(t, options.StoreDir)
			client, _, err := attachManagedHostClient(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.InspectSession(t.Context(), appserver.StateRequest{SessionID: "session"}); err != nil {
				t.Fatal(err)
			}
			hosts[1].publish(t, options.StoreDir)
			var requests sync.WaitGroup
			for range 8 {
				requests.Add(1)
				go func() {
					defer requests.Done()
					state, err := client.InspectSession(t.Context(), appserver.StateRequest{SessionID: "session"})
					if err != nil || state.Title != hosts[1].info.BuildID {
						t.Errorf("InspectSession() = %#v, %v, want replacement Host", state, err)
					}
				}()
			}
			requests.Wait()
			if got := hosts[0].requests.Load(); got != 1 {
				t.Fatalf("requests to old Host = %d, want 1", got)
			}
			if got := hosts[1].initializes.Load(); got != 2 {
				t.Fatalf("replacement handshakes = %d, want inspection + principal attach once", got)
			}
			if got := hosts[1].ingressRequests.Load(); ingress && got != 8 || !ingress && got != 0 {
				t.Fatalf("replacement ingress requests = %d, ingress=%v", got, ingress)
			}
		})
	}
}

func TestManagedTransportRejectsReplacementBeforeDispatch(t *testing.T) {
	for _, kind := range []string{"capability", "identity", "scope", "protocol"} {
		t.Run(kind, func(t *testing.T) {
			options, hosts := managedTransportFixture(t)
			hosts[0].publish(t, options.StoreDir)
			client, _, err := attachManagedHostClient(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "capability":
				hosts[1].info.Capabilities = []string{appserver.CapabilityHostReadiness}
				hosts[1].record.Capabilities = hosts[1].info.Capabilities
			case "identity":
				hosts[1].info.InstanceID = uuid.NewString()
			case "scope":
				hosts[1].record.PrincipalID = "different-user"
			case "protocol":
				hosts[1].info.APIVersion = "unsupported"
				hosts[1].record.APIVersion = hosts[1].info.APIVersion
			}
			hosts[1].publish(t, options.StoreDir)
			_, err = client.InspectSession(t.Context(), appserver.StateRequest{SessionID: "session"})
			if err == nil || errorcode.Is(err, errorcode.Unavailable) {
				t.Fatalf("InspectSession() error = %v, want permanent attach failure", err)
			}
			if got := hosts[1].requests.Load(); got != 0 {
				t.Fatalf("dispatched %d requests to unvalidated replacement", got)
			}
		})
	}
}

func TestManagedTransportDoesNotStartHostDuringDiscoveryGap(t *testing.T) {
	options, hosts := managedTransportFixture(t)
	options.LaunchLocalService = func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
		t.Fatal("reconnection must not launch the old client's binary")
		return servicelifecycle.LaunchedProcess{}, nil
	}
	hosts[0].publish(t, options.StoreDir)
	client, _, err := attachManagedHostClient(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := controlserver.RemoveDiscoveryRecord(controlserver.DefaultDiscoveryFile(options.StoreDir), hosts[0].record.InstanceID); err != nil {
		t.Fatal(err)
	}
	_, err = client.InspectSession(t.Context(), appserver.StateRequest{SessionID: "session"})
	if !errorcode.Is(err, errorcode.Unavailable) {
		t.Fatalf("discovery gap error = %v, want Unavailable", err)
	}
	hosts[1].publish(t, options.StoreDir)
	if _, err := client.InspectSession(t.Context(), appserver.StateRequest{SessionID: "session"}); err != nil {
		t.Fatalf("read after publication: %v", err)
	}
}

func TestManagedTransportDoesNotReplayLostWriteOnReplacement(t *testing.T) {
	options, hosts := managedTransportFixture(t)
	underlying := options.HTTPClient.Transport
	var writes atomic.Int32
	options.HTTPClient.Transport = managedTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			writes.Add(1)
			hosts[1].publish(t, options.StoreDir)
			return nil, &net.OpError{Op: "read", Err: errors.New("connection reset after accepting command")}
		}
		return underlying.RoundTrip(request)
	})
	hosts[0].publish(t, options.StoreDir)
	client, _, err := attachManagedHostClient(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Prompt(t.Context(), appserver.PromptRequest{
		WriteBase: appserver.WriteBase{SessionID: "session", OperationID: "one-command"}, Input: "run once",
	})
	if err == nil {
		t.Fatal("lost write response reported success")
	}
	if _, err := client.InspectSession(t.Context(), appserver.StateRequest{SessionID: "session"}); err != nil {
		t.Fatalf("follow-up observation: %v", err)
	}
	if writes.Load() != 1 {
		t.Fatalf("write attempts = %d, want exactly one", writes.Load())
	}
}

func TestManagedConnectionErrorPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := managedConnectionError(ctx, os.ErrNotExist); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

type managedTestRoundTripper func(*http.Request) (*http.Response, error)

func (f managedTestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type managedTransportHost struct {
	info            appserver.ServerInfo
	record          controlserver.DiscoveryRecord
	token           string
	ingressToken    string
	initializes     atomic.Int32
	requests        atomic.Int32
	ingressRequests atomic.Int32
}

func managedTransportFixture(t *testing.T) (productClientOptions, [2]*managedTransportHost) {
	t.Helper()
	storeDir := t.TempDir()
	for _, path := range []string{controlserver.DefaultTokenFile(storeDir), controlserver.DefaultACPIngressTokenFile(storeDir)} {
		if _, err := controlserver.LoadOrCreateBearerToken(path); err != nil {
			t.Fatal(err)
		}
	}
	var hosts [2]*managedTransportHost
	byAddress := make(map[string]*managedTransportHost)
	for i, address := range []string{"127.0.0.1:32101", "127.0.0.1:32102"} {
		policy := appserver.CurrentCompatibility()
		info := appserver.ServerInfo{
			ProtocolVersion: policy.ProtocolVersions[0], EnvelopeVersion: appserver.EnvelopeVersion, APIVersion: appserver.HTTPAPIVersion,
			ServerID: appserver.ServerIdentity, InstanceID: uuid.NewString(), DistributionVersion: "v1.2.3", BuildID: address,
			BuildKind: "release", Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
		}
		info.DistributionVersion = []string{"v1.2.3", "v1.2.4"}[i]
		host := &managedTransportHost{
			info:         info,
			token:        base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat([]string{"a", "b"}[i], 32))),
			ingressToken: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat([]string{"c", "d"}[i], 32))),
		}
		host.record = controlserver.DiscoveryRecord{
			SchemaVersion: controlserver.DiscoverySchemaVersion, ServerID: info.ServerID, InstanceID: info.InstanceID,
			AppName: "caelis-test", PrincipalID: "local-user", PID: 100 + i, Endpoint: "http://" + address,
			ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
			DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
			Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: time.Now().UTC(),
		}
		hosts[i], byAddress[address] = host, host
	}
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := byAddress[r.URL.Host]
		if host == nil {
			http.Error(w, "unknown endpoint", http.StatusBadGateway)
			return
		}
		auth := r.Header.Get("Authorization")
		if r.URL.Path != "/readyz" && auth != "Bearer "+host.token && auth != "Bearer "+host.ingressToken {
			http.Error(w, "wrong instance credential", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/readyz":
			_ = json.NewEncoder(w).Encode(appserver.HostStatus{ServerID: host.info.ServerID, InstanceID: host.info.InstanceID, Ready: true})
		case "/api/control/v1/initialize":
			host.initializes.Add(1)
			_ = json.NewEncoder(w).Encode(host.info)
		default:
			host.requests.Add(1)
			if auth == "Bearer "+host.ingressToken {
				host.ingressRequests.Add(1)
			}
			body, err := wirev1.Marshal(appserver.SessionState{
				ProtocolVersion: host.info.ProtocolVersion, EnvelopeVersion: host.info.EnvelopeVersion, APIVersion: host.info.APIVersion,
				SessionID: "session", Title: host.info.BuildID,
			})
			if err != nil {
				t.Errorf("encode Session state: %v", err)
				http.Error(w, "encode Session state", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(body)
		}
	}))
	return productClientOptions{AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir, HTTPClient: server.Client()}, hosts
}

func (h *managedTransportHost) publish(t *testing.T, storeDir string) {
	t.Helper()
	for path, token := range map[string]string{
		controlserver.DefaultTokenFile(storeDir): h.token, controlserver.DefaultACPIngressTokenFile(storeDir): h.ingressToken,
	} {
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), h.record); err != nil {
		t.Fatal(err)
	}
}
