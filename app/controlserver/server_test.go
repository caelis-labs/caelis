package controlserver

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestResolveNetworkConfigRequiresTLSOffLoopback(t *testing.T) {
	authenticator, err := BearerTokenAuthenticator(
		"0123456789abcdef0123456789abcdef0123456789abcdef",
		appserver.Principal{ID: "owner"},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = resolveNetworkConfig(Config{
		Address: "0.0.0.0:7777", Authenticator: authenticator,
		AllowedHosts: []string{"control.example.test"},
	})
	if err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatalf("resolveNetworkConfig() error = %v, want TLS requirement", err)
	}
	err = ListenAndServe(context.Background(), Dependencies{}, Config{
		Address: "0.0.0.0:7777", Authenticator: authenticator,
		AllowedHosts: []string{"control.example.test"},
	})
	if err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatalf("ListenAndServe() error = %v, want pre-listen TLS requirement", err)
	}

	resolved, useTLS, err := resolveNetworkConfig(Config{
		Address: "127.0.0.1:7777", Authenticator: authenticator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if useTLS || len(resolved.AllowedHosts) == 0 {
		t.Fatalf("resolved loopback config = %#v, TLS = %v", resolved, useTLS)
	}

	_, useTLS, err = resolveNetworkConfig(Config{
		Address: "0.0.0.0:7777", Authenticator: authenticator,
		AllowedHosts: []string{"control.example.test"}, TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
	})
	if err != nil || !useTLS {
		t.Fatalf("resolveNetworkConfig(TLS) = TLS %v, error %v", useTLS, err)
	}
}

func TestListenAndServeQuiescesHostBeforeReturning(t *testing.T) {
	authenticator, err := BearerTokenAuthenticator(
		"0123456789abcdef0123456789abcdef0123456789abcdef",
		appserver.Principal{ID: "owner"},
	)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &recordingLifecycle{}
	instanceID := "388591ac-a5b8-448f-910d-08a70cbf6db4"
	var listening ListenerInfo
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	listener := testenv.NewMemoryListener("127.0.0.1:1455")
	err = listenAndServe(ctx, Dependencies{
		Services: testAppServerServices(&fakeService{}, staticStatusService{}), Lifecycle: lifecycle,
	}, Config{
		Address: "127.0.0.1:0", Authenticator: authenticator,
		DrainTimeout: time.Second,
		ServerInfo: appserver.ServerInfo{
			ServerID: appserver.ServerIdentity, InstanceID: instanceID,
			Capabilities: appserver.RequiredManagedHostCapabilities(),
		},
		OnListening: func(info ListenerInfo) error {
			listening = info
			return nil
		},
	}, func(string, string) (net.Listener, error) {
		return listener, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.calls.Load() != 1 {
		t.Fatalf("Host Quiesce calls = %d, want 1", lifecycle.calls.Load())
	}
	if listening.Endpoint != "http://127.0.0.1:1455" || listening.ServerInfo.InstanceID != instanceID {
		t.Fatalf("listener publication = %#v", listening)
	}
}

func TestAuthenticatedHostShutdownQuiescesInMemoryServer(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := BearerTokenAuthenticator(token, appserver.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := "3fc6e876-463b-4505-802d-deef4fcb40cf"
	listener := testenv.NewMemoryListener("127.0.0.1:1456")
	client := testenv.NewMemoryHTTPClient(listener)
	lifecycle := &recordingLifecycle{}
	listening := make(chan ListenerInfo, 1)
	done := make(chan error, 1)
	go func() {
		done <- listenAndServe(context.Background(), Dependencies{
			Services:  testAppServerServices(&fakeService{}, staticStatusService{}),
			Lifecycle: lifecycle,
		}, Config{
			Address: "127.0.0.1:0", Authenticator: authenticator, DrainTimeout: time.Second,
			ServerInfo: appserver.ServerInfo{
				ServerID: appserver.ServerIdentity, InstanceID: instanceID,
				Capabilities: appserver.RequiredManagedHostCapabilities(),
			},
			OnListening: func(info ListenerInfo) error {
				listening <- info
				return nil
			},
		}, func(string, string) (net.Listener, error) { return listener, nil })
	}()
	info := <-listening
	remote, err := httpclient.New(httpclient.Config{
		BaseURL: info.Endpoint, BearerToken: token, HTTPClient: client,
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	acknowledged, err := remote.ShutdownHost(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.InstanceID != instanceID || acknowledged.Ready {
		t.Fatalf("shutdown acknowledgement = %#v", acknowledged)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authenticated Host shutdown did not stop the server")
	}
	if lifecycle.calls.Load() != 1 {
		t.Fatalf("Host Quiesce calls = %d, want 1", lifecycle.calls.Load())
	}
}

func TestResolveNetworkConfigRejectsAmbiguousOrIncompleteTrust(t *testing.T) {
	authenticator, err := BearerTokenAuthenticator(
		"0123456789abcdef0123456789abcdef0123456789abcdef",
		appserver.Principal{ID: "owner"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, config := range map[string]Config{
		"half TLS": {
			Address: "127.0.0.1:7777", Authenticator: authenticator, TLSCertFile: "cert.pem",
		},
		"wildcard without hosts": {
			Address: "0.0.0.0:7777", Authenticator: authenticator, TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		},
		"auth and token file": {
			Address: "127.0.0.1:7777", Authenticator: authenticator, TokenFile: "token",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resolveNetworkConfig(config); err == nil {
				t.Fatalf("resolveNetworkConfig(%#v) accepted", config)
			}
		})
	}
}

func TestResolveNetworkConfigBuildsLoopbackAuthenticatorFromTokenFile(t *testing.T) {
	path := DefaultTokenFile(t.TempDir())
	resolved, useTLS, err := resolveNetworkConfig(Config{
		Address: "127.0.0.1:7777", TokenFile: path, Principal: appserver.Principal{ID: "owner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if useTLS || resolved.Authenticator == nil {
		t.Fatalf("resolved config = %#v, TLS = %v", resolved, useTLS)
	}
	token, err := LoadOrCreateBearerToken(path)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:7777/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	principal, err := resolved.Authenticator.Authenticate(request)
	if err != nil || principal.ID != "owner" {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
}

func TestResolvedBearerAuthenticatorRejectsAmbiguousAuthorization(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := BearerTokenAuthenticator(token, appserver.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][]string{
		"missing":    nil,
		"duplicate":  {"Bearer " + token, "Bearer " + token},
		"combined":   {"Bearer " + token + ", Bearer " + token},
		"wrong type": {"Basic " + token},
	} {
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range values {
				request.Header.Add("Authorization", value)
			}
			if _, err := authenticator.Authenticate(request); err == nil {
				t.Fatalf("Authorization %q accepted", values)
			}
		})
	}
}

type recordingLifecycle struct {
	calls atomic.Int32
}

func (l *recordingLifecycle) Quiesce(context.Context) error {
	l.calls.Add(1)
	return nil
}
