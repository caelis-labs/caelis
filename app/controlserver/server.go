// Package controlserver assembles and runs the HTTP Control client surface.
package controlserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/surfaces/appserver"
)

type Config struct {
	Address       string
	Authenticator appserver.Authenticator
	Principal     controlclient.Principal
	TokenFile     string
	AllowedHosts  []string
	TLSCertFile   string
	TLSKeyFile    string
	Heartbeat     time.Duration
}

// BearerTokenAuthenticator constructs the production static-token boundary
// for Control servers. The principal is trusted configuration,
// never client-supplied request data.
func BearerTokenAuthenticator(token string, principal controlclient.Principal) (appserver.Authenticator, error) {
	token = strings.TrimSpace(token)
	principal.ID = strings.TrimSpace(principal.ID)
	if len(token) < sha256.Size || principal.ID == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("controlserver: bearer token and principal are required")
	}
	expected := sha256.Sum256([]byte(token))
	return appserver.AuthenticatorFunc(func(request *http.Request) (controlclient.Principal, error) {
		values := request.Header.Values("Authorization")
		if len(values) != 1 || strings.Contains(values[0], ",") {
			return controlclient.Principal{}, errorcode.New(errorcode.Unauthenticated, "controlserver: bearer authentication failed")
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return controlclient.Principal{}, errorcode.New(errorcode.Unauthenticated, "controlserver: bearer authentication failed")
		}
		provided := sha256.Sum256([]byte(parts[1]))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			return controlclient.Principal{}, errorcode.New(errorcode.Unauthenticated, "controlserver: bearer authentication failed")
		}
		return principal, nil
	}), nil
}

func Handler(stack *gatewayapp.Stack, config Config) (http.Handler, error) {
	if stack == nil {
		return nil, errors.New("controlserver: gateway stack is required")
	}
	if config.Authenticator == nil {
		return nil, errors.New("controlserver: authenticator is required for an HTTP handler")
	}
	server, err := appserver.New(appserver.Config{
		Service: stack.ControlClient(), Authenticator: config.Authenticator,
		AllowedHosts: append([]string(nil), config.AllowedHosts...), Heartbeat: config.Heartbeat,
	})
	if err != nil {
		return nil, err
	}
	return server.Handler(), nil
}

func ListenAndServe(ctx context.Context, stack *gatewayapp.Stack, config Config) error {
	resolved, useTLS, err := resolveNetworkConfig(config)
	if err != nil {
		return err
	}
	var certificate tls.Certificate
	if useTLS {
		certificate, err = tls.LoadX509KeyPair(resolved.TLSCertFile, resolved.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("controlserver: load TLS certificate: %w", err)
		}
	}
	handler, err := Handler(stack, resolved)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", resolved.Address)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	if useTLS {
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}
	done := make(chan error, 1)
	go func() {
		if useTLS {
			done <- server.ServeTLS(listener, "", "")
			return
		}
		done <- server.Serve(listener)
	}()
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

func resolveNetworkConfig(config Config) (Config, bool, error) {
	config.Address = strings.TrimSpace(config.Address)
	if config.Address == "" {
		config.Address = "127.0.0.1:7777"
	}
	host, _, err := net.SplitHostPort(config.Address)
	if err != nil {
		return Config{}, false, fmt.Errorf("controlserver: invalid listen address: %w", err)
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	wildcard := host == "" || host == "0.0.0.0" || host == "::"

	config.TLSCertFile = strings.TrimSpace(config.TLSCertFile)
	config.TLSKeyFile = strings.TrimSpace(config.TLSKeyFile)
	if (config.TLSCertFile == "") != (config.TLSKeyFile == "") {
		return Config{}, false, errors.New("controlserver: TLS certificate and key must be configured together")
	}
	useTLS := config.TLSCertFile != ""
	if !loopback && !useTLS {
		return Config{}, false, errors.New("controlserver: non-loopback listener requires TLS")
	}

	if config.Authenticator != nil && strings.TrimSpace(config.TokenFile) != "" {
		return Config{}, false, errors.New("controlserver: configure either an authenticator or a token file, not both")
	}
	if config.Authenticator == nil {
		tokenFile := strings.TrimSpace(config.TokenFile)
		if tokenFile == "" {
			return Config{}, false, errors.New("controlserver: bearer token file is required")
		}
		token, loadErr := LoadOrCreateBearerToken(tokenFile)
		if loadErr != nil {
			return Config{}, false, loadErr
		}
		config.Authenticator, loadErr = BearerTokenAuthenticator(token, config.Principal)
		if loadErr != nil {
			return Config{}, false, loadErr
		}
	}
	if len(config.AllowedHosts) == 0 {
		if wildcard {
			return Config{}, false, errors.New("controlserver: wildcard listener requires an explicit Host allowlist")
		}
		config.AllowedHosts = []string{host}
		if loopback {
			config.AllowedHosts = append(config.AllowedHosts, "localhost", "127.0.0.1", "::1")
		}
	}
	return config, useTLS, nil
}
