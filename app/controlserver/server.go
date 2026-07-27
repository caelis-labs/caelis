// Package controlserver assembles and runs the HTTP Control client surface.
package controlserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
	"github.com/caelis-labs/caelis/surfaces/appserver"
)

// Dependencies contains only the Control contracts exposed over HTTP.
// Product assembly remains outside the listener package.
type Dependencies struct {
	Service     controlclient.Service
	TaskStreams taskstream.Service
}

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

func Handler(deps Dependencies, config Config) (http.Handler, error) {
	if deps.Service == nil {
		return nil, errors.New("controlserver: Control client service is required")
	}
	if config.Authenticator == nil {
		return nil, errors.New("controlserver: authenticator is required for an HTTP handler")
	}
	server, err := appserver.New(appserver.Config{
		Service: deps.Service, TaskStreams: deps.TaskStreams, Authenticator: config.Authenticator,
		AllowedHosts: append([]string(nil), config.AllowedHosts...), Heartbeat: config.Heartbeat,
	})
	if err != nil {
		return nil, err
	}
	return server.Handler(), nil
}

func ListenAndServe(ctx context.Context, deps Dependencies, config Config) error {
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
	handler, err := Handler(deps, resolved)
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
		config.Authenticator, loadErr = appserver.BearerTokenAuthenticator(token, config.Principal)
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
