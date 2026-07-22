package controlserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	controlclient "github.com/caelis-labs/caelis/control/client"
)

func TestResolveNetworkConfigRequiresTLSOffLoopback(t *testing.T) {
	authenticator, err := BearerTokenAuthenticator(
		"0123456789abcdef0123456789abcdef0123456789abcdef",
		controlclient.Principal{ID: "owner"},
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
	err = ListenAndServe(context.Background(), nil, Config{
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

func TestResolveNetworkConfigRejectsAmbiguousOrIncompleteTrust(t *testing.T) {
	authenticator, err := BearerTokenAuthenticator(
		"0123456789abcdef0123456789abcdef0123456789abcdef",
		controlclient.Principal{ID: "owner"},
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
		Address: "127.0.0.1:7777", TokenFile: path, Principal: controlclient.Principal{ID: "owner"},
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

func TestBearerTokenAuthenticatorRejectsAmbiguousAuthorization(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := BearerTokenAuthenticator(token, controlclient.Principal{ID: "owner"})
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
