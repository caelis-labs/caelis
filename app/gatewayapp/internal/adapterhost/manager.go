// Package adapterhost owns Host-managed adapter processes, generations, and
// one-use channel grants. It remains provider-neutral.
package adapterhost

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
)

const grantLifetime = 30 * time.Second

// Backend is one provider implementation bound to a Host-owned process.
type Backend interface {
	ServeACP(context.Context, controladapterhost.ChannelContext, io.Reader, io.Writer) error
	Done() <-chan struct{}
	Err() error
	Close() error
}

// Registration is one statically composed hosted adapter.
type Registration struct {
	ID         string
	Name       string
	Command    string
	Args       []string
	NewBackend func(context.Context, io.Reader, io.Writer) (Backend, error)
}

type channelGrant struct {
	adapterID   string
	principalID string
	context     controladapterhost.ChannelContext
	expiresAt   time.Time
}

type runningBackend struct {
	command  *exec.Cmd
	process  *processTree
	backend  Backend
	waitDone chan error
}

// Manager owns all hosted-adapter process generations for one Caelis Host.
type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu            sync.Mutex
	registrations map[string]Registration
	running       map[string]*runningBackend
	starting      map[string]chan struct{}
	startErrors   map[string]error
	grants        map[string]channelGrant
	closed        bool
}

// NewManager constructs a lazy process supervisor from a static registration set.
func NewManager(registrations ...Registration) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		ctx: ctx, cancel: cancel, registrations: make(map[string]Registration),
		running: make(map[string]*runningBackend), starting: make(map[string]chan struct{}),
		startErrors: make(map[string]error), grants: make(map[string]channelGrant),
	}
	for _, raw := range registrations {
		registration := normalizeRegistration(raw)
		if registration.ID == "" || registration.Name == "" || registration.Command == "" || registration.NewBackend == nil {
			cancel()
			return nil, errors.New("gatewayapp adapterhost: incomplete registration")
		}
		if _, exists := m.registrations[registration.ID]; exists {
			cancel()
			return nil, fmt.Errorf("gatewayapp adapterhost: duplicate adapter %q", registration.ID)
		}
		m.registrations[registration.ID] = registration
	}
	return m, nil
}

func normalizeRegistration(in Registration) Registration {
	in.ID = strings.ToLower(strings.TrimSpace(in.ID))
	in.Name = strings.TrimSpace(in.Name)
	in.Command = strings.TrimSpace(in.Command)
	in.Args = append([]string(nil), in.Args...)
	return in
}

// Inspect reports registration, executable, and current backend status without
// starting a process.
func (m *Manager) Inspect(_ context.Context, adapterID string) (controladapterhost.Descriptor, error) {
	adapterID = strings.ToLower(strings.TrimSpace(adapterID))
	m.mu.Lock()
	registration, ok := m.registrations[adapterID]
	running := m.running[adapterID]
	lastErr := m.startErrors[adapterID]
	closed := m.closed
	m.mu.Unlock()
	if !ok {
		return controladapterhost.Descriptor{}, fmt.Errorf("gatewayapp adapterhost: unknown adapter %q", adapterID)
	}
	descriptor := controladapterhost.Descriptor{ID: registration.ID, Name: registration.Name, BackendState: "stopped"}
	if closed {
		descriptor.Diagnostic = "Host adapter service is shutting down"
		return descriptor, nil
	}
	if !commandAvailable(registration.Command) {
		descriptor.Diagnostic = fmt.Sprintf("install %s so %q is available on PATH", registration.Name, registration.Command)
		return descriptor, nil
	}
	descriptor.Available = true
	if running != nil {
		descriptor.BackendState = "running"
	} else if lastErr != nil {
		descriptor.BackendState = "failed"
		descriptor.Diagnostic = lastErr.Error()
	}
	return descriptor, nil
}

func commandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

// IssueGrant creates one short-lived credential restricted to one route and
// one authorized workspace projection.
func (m *Manager) IssueGrant(_ context.Context, principalID, adapterID string, request controladapterhost.GrantRequest) (controladapterhost.Grant, error) {
	principalID = strings.TrimSpace(principalID)
	adapterID = strings.ToLower(strings.TrimSpace(adapterID))
	if principalID == "" || strings.TrimSpace(request.ConnectionID) == "" {
		return controladapterhost.Grant{}, errors.New("gatewayapp adapterhost: principal and connection identity are required")
	}
	m.mu.Lock()
	_, registered := m.registrations[adapterID]
	closed := m.closed
	m.mu.Unlock()
	if !registered {
		return controladapterhost.Grant{}, fmt.Errorf("gatewayapp adapterhost: unknown adapter %q", adapterID)
	}
	if closed {
		return controladapterhost.Grant{}, errors.New("gatewayapp adapterhost: service is shutting down")
	}
	roots, err := normalizeGrantRoots(request.CWD, request.AllowedRoots)
	if err != nil {
		return controladapterhost.Grant{}, err
	}
	writable, err := normalizeGrantRoots("", request.WritableRoots)
	if err != nil {
		return controladapterhost.Grant{}, err
	}
	token, err := randomToken()
	if err != nil {
		return controladapterhost.Grant{}, err
	}
	expiresAt := time.Now().UTC().Add(grantLifetime)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return controladapterhost.Grant{}, errors.New("gatewayapp adapterhost: service is shutting down")
	}
	for value, grant := range m.grants {
		if time.Now().After(grant.expiresAt) {
			delete(m.grants, value)
		}
	}
	m.grants[token] = channelGrant{
		adapterID: adapterID, principalID: principalID, expiresAt: expiresAt,
		context: controladapterhost.ChannelContext{
			PrincipalID: principalID, ConnectionID: strings.TrimSpace(request.ConnectionID),
			AllowedRoots: roots, WritableRoots: writable,
		},
	}
	m.mu.Unlock()
	return controladapterhost.Grant{Token: token, ExpiresAt: expiresAt}, nil
}

// ServeChannel consumes one grant and binds the channel to the shared backend.
func (m *Manager) ServeChannel(ctx context.Context, adapterID, token string, input io.Reader, output io.Writer) error {
	adapterID = strings.ToLower(strings.TrimSpace(adapterID))
	token = strings.TrimSpace(token)
	m.mu.Lock()
	grant, ok := m.grants[token]
	if ok {
		delete(m.grants, token)
	}
	m.mu.Unlock()
	if !ok || grant.adapterID != adapterID || time.Now().After(grant.expiresAt) {
		return errors.New("gatewayapp adapterhost: invalid or expired channel grant")
	}
	backend, err := m.ensureBackend(ctx, adapterID)
	if err != nil {
		return err
	}
	return backend.ServeACP(ctx, grant.context, input, output)
}

func (m *Manager) ensureBackend(ctx context.Context, adapterID string) (Backend, error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, errors.New("gatewayapp adapterhost: service is shutting down")
		}
		if running := m.running[adapterID]; running != nil {
			backend := running.backend
			m.mu.Unlock()
			return backend, nil
		}
		if wait := m.starting[adapterID]; wait != nil {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			case <-wait:
				continue
			}
		}
		registration, ok := m.registrations[adapterID]
		if !ok {
			m.mu.Unlock()
			return nil, fmt.Errorf("gatewayapp adapterhost: unknown adapter %q", adapterID)
		}
		wait := make(chan struct{})
		m.starting[adapterID] = wait
		m.mu.Unlock()

		running, err := m.startBackend(registration)
		m.mu.Lock()
		delete(m.starting, adapterID)
		if err != nil {
			m.startErrors[adapterID] = err
		} else if m.closed {
			err = errors.New("gatewayapp adapterhost: service shut down during backend start")
		} else {
			delete(m.startErrors, adapterID)
			m.running[adapterID] = running
		}
		close(wait)
		m.mu.Unlock()
		if err != nil {
			if running != nil {
				_ = running.backend.Close()
				_ = killProcess(running.command, running.process)
				_ = running.command.Wait()
				_ = running.process.Close()
			}
			return nil, err
		}
		go m.observeExit(adapterID, running)
		return running.backend, nil
	}
}

func (m *Manager) startBackend(registration Registration) (*runningBackend, error) {
	resolved, err := exec.LookPath(registration.Command)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp adapterhost: %s executable %q is unavailable: %w", registration.Name, registration.Command, err)
	}
	command := exec.Command(resolved, registration.Args...)
	process, err := prepareProcess(command)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp adapterhost: prepare %s process tree: %w", registration.Name, err)
	}
	started := false
	defer func() {
		if !started {
			_ = process.Close()
		}
	}()
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("gatewayapp adapterhost: start %s: %w", registration.Name, err)
	}
	if err := process.Started(command); err != nil {
		_ = command.Process.Kill()
		_ = killProcess(command, process)
		_ = command.Wait()
		return nil, fmt.Errorf("gatewayapp adapterhost: supervise %s process tree: %w", registration.Name, err)
	}
	stderrTail := &boundedTail{limit: 4096}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderrTail, stderr)
		close(stderrDone)
	}()
	backend, err := registration.NewBackend(m.ctx, stdout, stdin)
	if err != nil {
		_ = killProcess(command, process)
		_ = command.Wait()
		<-stderrDone
		if diagnostic := strings.TrimSpace(stderrTail.String()); diagnostic != "" {
			return nil, fmt.Errorf("%w; provider stderr: %s", err, diagnostic)
		}
		return nil, err
	}
	started = true
	return &runningBackend{command: command, process: process, backend: backend, waitDone: make(chan error, 1)}, nil
}

func (m *Manager) observeExit(adapterID string, running *runningBackend) {
	err := running.command.Wait()
	closeErr := running.process.Close()
	if err == nil {
		err = closeErr
	}
	running.waitDone <- err
	close(running.waitDone)
	_ = running.backend.Close()
	m.mu.Lock()
	if m.running[adapterID] == running {
		delete(m.running, adapterID)
		if !m.closed {
			if err == nil {
				err = errors.New("backend process exited")
			}
			m.startErrors[adapterID] = err
		}
	}
	m.mu.Unlock()
}

// Quiesce rejects new channels, closes backends, terminates processes, and
// waits for the sole process waiters.
func (m *Manager) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.grants = make(map[string]channelGrant)
	running := make([]*runningBackend, 0, len(m.running))
	for _, backend := range m.running {
		running = append(running, backend)
	}
	m.mu.Unlock()
	m.cancel()
	var errs []error
	for _, backend := range running {
		if err := backend.backend.Close(); err != nil {
			errs = append(errs, err)
		}
		if err := signalProcess(ctx, backend.command, backend.process); err != nil {
			errs = append(errs, err)
		}
	}
	pending := append([]*runningBackend(nil), running...)
	for len(pending) > 0 {
		backend := pending[0]
		select {
		case <-ctx.Done():
			errs = append(errs, context.Cause(ctx))
			for _, remaining := range pending {
				if err := killProcess(remaining.command, remaining.process); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		case <-backend.waitDone:
			pending = pending[1:]
		}
	}
	return errors.Join(errs...)
}

// Close applies a bounded quiesce.
func (m *Manager) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.Quiesce(ctx)
}

func normalizeGrantRoots(cwd string, roots []string) ([]string, error) {
	values := append([]string(nil), roots...)
	if strings.TrimSpace(cwd) != "" {
		values = append([]string{cwd}, values...)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if !filepath.IsAbs(raw) {
			return nil, fmt.Errorf("gatewayapp adapterhost: workspace root %q must be absolute", raw)
		}
		path := filepath.Clean(raw)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	if strings.TrimSpace(cwd) != "" && len(out) == 0 {
		return nil, errors.New("gatewayapp adapterhost: workspace authorization is required")
	}
	return out, nil
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("gatewayapp adapterhost: generate channel grant: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

type boundedTail struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *boundedTail) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(value)
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return written, nil
}

func (b *boundedTail) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, string(b.data))
}

var _ controladapterhost.Service = (*Manager)(nil)
