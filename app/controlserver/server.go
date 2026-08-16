// Package controlserver assembles and runs the HTTP adapter around Control.
package controlserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// Dependencies contains only the Control contracts exposed over HTTP.
// Product assembly remains outside the listener package.
type Dependencies struct {
	Services  appserver.AppServerServices
	Lifecycle interface {
		Quiesce(context.Context) error
	}
}

// Config configures the Control Host listener and shutdown lifecycle.
type Config struct {
	Address       string
	Authenticator Authenticator
	Principal     appserver.Principal
	TokenFile     string
	AllowedHosts  []string
	TLSCertFile   string
	TLSKeyFile    string
	Heartbeat     time.Duration
	DrainTimeout  time.Duration
	ServerInfo    appserver.ServerInfo
	OnListening   func(ListenerInfo) error
	Ready         func() bool
	Shutdown      func()
}

// ListenerInfo describes the bound listener after recovery and before clients
// are admitted. It is suitable for publishing credential-free discovery
// metadata.
type ListenerInfo struct {
	Endpoint   string
	Address    string
	ServerInfo appserver.ServerInfo
}

func Handler(deps Dependencies, config Config) (http.Handler, error) {
	if err := deps.Services.Validate(); err != nil {
		return nil, fmt.Errorf("controlserver: invalid AppServer services: %w", err)
	}
	if config.Authenticator == nil {
		return nil, errors.New("controlserver: authenticator is required for an HTTP handler")
	}
	server, err := New(HandlerConfig{
		Services: deps.Services, Authenticator: config.Authenticator,
		AllowedHosts: append([]string(nil), config.AllowedHosts...), Heartbeat: config.Heartbeat,
		ServerInfo: config.ServerInfo, Ready: config.Ready, Shutdown: config.Shutdown,
	})
	if err != nil {
		return nil, err
	}
	return server.Handler(), nil
}

func ListenAndServe(ctx context.Context, deps Dependencies, config Config) error {
	return listenAndServe(ctx, deps, config, net.Listen)
}

func listenAndServe(
	ctx context.Context,
	deps Dependencies,
	config Config,
	listen func(string, string) (net.Listener, error),
) error {
	resolved, useTLS, err := resolveNetworkConfig(config)
	if err != nil {
		return err
	}
	if listen == nil {
		return errors.New("controlserver: listener factory is required")
	}
	var certificate tls.Certificate
	if useTLS {
		certificate, err = tls.LoadX509KeyPair(resolved.TLSCertFile, resolved.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("controlserver: load TLS certificate: %w", err)
		}
	}
	readiness := &atomic.Bool{}
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	resolved.Ready = readiness.Load
	resolved.Shutdown = stopServing
	handler, err := Handler(deps, resolved)
	if err != nil {
		return err
	}
	if deps.Lifecycle == nil {
		return errors.New("controlserver: Host lifecycle is required")
	}
	listener, err := listen("tcp", resolved.Address)
	if err != nil {
		return err
	}
	endpoint, err := listenerEndpoint(listener.Addr(), useTLS)
	if err != nil {
		_ = listener.Close()
		return err
	}
	readiness.Store(true)
	defer readiness.Store(false)
	if resolved.OnListening != nil {
		if err := resolved.OnListening(ListenerInfo{
			Endpoint: endpoint, Address: listener.Addr().String(),
			ServerInfo: normalizeServerInfo(resolved.ServerInfo),
		}); err != nil {
			readiness.Store(false)
			_ = listener.Close()
			return fmt.Errorf("controlserver: publish listener readiness: %w", err)
		}
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server.BaseContext = func(net.Listener) context.Context { return requestCtx }
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
		readiness.Store(false)
		drainErr := quiesceHost(deps.Lifecycle, resolved.DrainTimeout)
		if errors.Is(err, http.ErrServerClosed) {
			return drainErr
		}
		if drainErr != nil {
			return errors.Join(err, drainErr)
		}
		return err
	case <-serveCtx.Done():
		readiness.Store(false)
		drainErr := quiesceHost(deps.Lifecycle, resolved.DrainTimeout)
		cancelRequests()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout(resolved.DrainTimeout))
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return errors.Join(drainErr, err)
		}
		if drainErr != nil {
			return drainErr
		}
		if errors.Is(serveCtx.Err(), context.Canceled) {
			return nil
		}
		return serveCtx.Err()
	}
}

func listenerEndpoint(address net.Addr, useTLS bool) (string, error) {
	if address == nil || strings.TrimSpace(address.String()) == "" {
		return "", errors.New("controlserver: bound listener address is unavailable")
	}
	host, port, err := net.SplitHostPort(address.String())
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return "", fmt.Errorf("controlserver: invalid bound listener address %q", address.String())
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

func quiesceHost(lifecycle interface {
	Quiesce(context.Context) error
}, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout(timeout))
	defer cancel()
	return lifecycle.Quiesce(ctx)
}

func drainTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 5 * time.Second
	}
	return timeout
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
	if len(config.ServerInfo.Transports) == 0 {
		transport := "http"
		if useTLS {
			transport = "https"
		}
		config.ServerInfo.Transports = []string{transport}
	}
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
