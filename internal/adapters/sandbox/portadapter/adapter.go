// Package portadapter adapts the existing ports/sandbox backend assets to the
// core-native sandbox contract used by the new app stack.
package portadapter

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	coresandbox "github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/bwrap"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/landlock"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/seatbelt"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows"
	portsandbox "github.com/OnslaughtSnail/caelis/ports/sandbox"
)

type Factory struct {
	Backend coresandbox.Backend
}

func (f Factory) NewRuntime(ctx context.Context, cfg coresandbox.Config) (coresandbox.Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	backend := coresandbox.Backend(strings.TrimSpace(string(f.Backend)))
	if backend == "" {
		return nil, fmt.Errorf("sandbox/portadapter: backend is required")
	}
	portCfg := ConfigToPort(cfg)
	portCfg.RequestedBackend = portBackend(backend)
	var (
		rt  portsandbox.Runtime
		err error
	)
	switch backend {
	case coresandbox.BackendBwrap:
		rt, err = bwrap.New(portCfg)
	case coresandbox.BackendLandlock:
		rt, err = landlock.New(portCfg)
	case coresandbox.BackendSeatbelt:
		rt, err = seatbelt.New(portCfg)
	case coresandbox.BackendWindows:
		rt, err = windows.New(portCfg)
	default:
		return nil, fmt.Errorf("sandbox/portadapter: unsupported backend %q", backend)
	}
	if err != nil {
		return nil, err
	}
	return Wrap(rt), nil
}

func Wrap(rt portsandbox.Runtime) coresandbox.Runtime {
	if rt == nil {
		return nil
	}
	return &Runtime{rt: rt}
}

func ConfigToPort(cfg coresandbox.Config) portsandbox.Config {
	out := portsandbox.Config{
		CWD:                 strings.TrimSpace(cfg.CWD),
		RequestedBackend:    portBackend(cfg.RequestedBackend),
		BackendCandidates:   portBackends(cfg.BackendCandidates),
		FallbackInstallHint: strings.TrimSpace(cfg.FallbackInstallHint),
		HelperPath:          strings.TrimSpace(cfg.HelperPath),
		StateDir:            strings.TrimSpace(cfg.StateDir),
		ReadableRoots:       slices.Clone(cfg.ReadableRoots),
		WritableRoots:       slices.Clone(cfg.WritableRoots),
		ReadOnlySubpaths:    slices.Clone(cfg.ReadOnlySubpaths),
	}
	return portsandbox.NormalizeConfig(out)
}

type Runtime struct {
	rt portsandbox.Runtime
}

func (r *Runtime) Descriptor() coresandbox.Descriptor {
	if r == nil || r.rt == nil {
		return coresandbox.Descriptor{}
	}
	return descriptorFromPort(r.rt.Describe())
}

func (r *Runtime) Status() coresandbox.Status {
	if r == nil || r.rt == nil {
		return coresandbox.Status{}
	}
	return statusFromPort(r.rt.Status())
}

func (r *Runtime) FileSystem() coresandbox.FileSystem {
	if r == nil || r.rt == nil {
		return nil
	}
	return fileSystemFromPort(r.rt.FileSystem())
}

func (r *Runtime) Run(ctx context.Context, req coresandbox.CommandRequest) (coresandbox.CommandResult, error) {
	if r == nil || r.rt == nil {
		return coresandbox.CommandResult{}, fmt.Errorf("sandbox/portadapter: runtime is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := r.rt.Run(ctx, commandRequestToPort(req))
	return commandResultFromPort(result), err
}

func (r *Runtime) Start(ctx context.Context, req coresandbox.CommandRequest) (coresandbox.Session, error) {
	if r == nil || r.rt == nil {
		return nil, fmt.Errorf("sandbox/portadapter: runtime is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := r.rt.Start(ctx, commandRequestToPort(req))
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("sandbox/portadapter: backend returned a nil session")
	}
	return &wrappedSession{session: session}, nil
}

func (r *Runtime) Open(ctx context.Context, ref coresandbox.SessionRef) (coresandbox.Session, error) {
	if r == nil || r.rt == nil {
		return nil, fmt.Errorf("sandbox/portadapter: runtime is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	portRef := sessionRefToPort(ref)
	if portRef.Backend == "" {
		portRef.Backend = portBackend(r.Descriptor().Backend)
	}
	session, err := r.rt.OpenSessionRef(portRef)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("sandbox/portadapter: backend returned a nil session")
	}
	return &wrappedSession{session: session}, nil
}

func (r *Runtime) Prepare(ctx context.Context) error {
	if r == nil || r.rt == nil {
		return nil
	}
	preparer, ok := r.rt.(portsandbox.PreparableRuntime)
	if !ok {
		return nil
	}
	return preparer.Prepare(progressContext(ctx))
}

func (r *Runtime) Repair(ctx context.Context) error {
	if r == nil || r.rt == nil {
		return nil
	}
	if repairer, ok := r.rt.(portsandbox.RepairableRuntime); ok {
		return repairer.Repair(progressContext(ctx))
	}
	if preparer, ok := r.rt.(portsandbox.PreparableRuntime); ok {
		return preparer.Prepare(progressContext(ctx))
	}
	return nil
}

func (r *Runtime) Preflight(ctx context.Context, opts coresandbox.PreflightOptions) error {
	if r == nil || r.rt == nil {
		return nil
	}
	preflight, ok := r.rt.(portsandbox.PreflightRuntime)
	if !ok {
		return nil
	}
	return preflight.Preflight(progressContext(ctx), portsandbox.PreflightOptions{
		AllowNonElevatedRepair: opts.AllowNonElevatedRepair,
	})
}

func (r *Runtime) Reset(ctx context.Context) error {
	if r == nil || r.rt == nil {
		return nil
	}
	resetter, ok := r.rt.(portsandbox.ResettableRuntime)
	if !ok {
		return nil
	}
	return resetter.Reset(progressContext(ctx))
}

func (r *Runtime) Close() error {
	if r == nil || r.rt == nil {
		return nil
	}
	return r.rt.Close()
}

type fileSystem struct {
	fs portsandbox.FileSystem
}

func fileSystemFromPort(in portsandbox.FileSystem) coresandbox.FileSystem {
	if in == nil {
		return nil
	}
	return fileSystem{fs: in}
}

func (f fileSystem) Getwd() (string, error)                     { return f.fs.Getwd() }
func (f fileSystem) UserHomeDir() (string, error)               { return f.fs.UserHomeDir() }
func (f fileSystem) Open(path string) (*os.File, error)         { return f.fs.Open(path) }
func (f fileSystem) ReadDir(path string) ([]os.DirEntry, error) { return f.fs.ReadDir(path) }
func (f fileSystem) Stat(path string) (os.FileInfo, error)      { return f.fs.Stat(path) }
func (f fileSystem) ReadFile(path string) ([]byte, error)       { return f.fs.ReadFile(path) }
func (f fileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return f.fs.WriteFile(path, data, perm)
}
func (f fileSystem) MkdirAll(path string, perm os.FileMode) error {
	mkdirer, ok := f.fs.(interface {
		MkdirAll(string, os.FileMode) error
	})
	if !ok {
		return fmt.Errorf("sandbox/portadapter: filesystem does not support recursive directory creation")
	}
	return mkdirer.MkdirAll(path, perm)
}
func (f fileSystem) Glob(pattern string) ([]string, error) { return f.fs.Glob(pattern) }
func (f fileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return f.fs.WalkDir(root, fn)
}

type wrappedSession struct {
	session portsandbox.Session
}

func (s *wrappedSession) Ref() coresandbox.SessionRef {
	if s == nil || s.session == nil {
		return coresandbox.SessionRef{}
	}
	return sessionRefFromPort(s.session.Ref())
}

func (s *wrappedSession) Snapshot(ctx context.Context) (coresandbox.SessionSnapshot, error) {
	if s == nil || s.session == nil {
		return coresandbox.SessionSnapshot{}, fmt.Errorf("sandbox/portadapter: session is unavailable")
	}
	status, err := s.session.Status(ctx)
	if err != nil {
		return coresandbox.SessionSnapshot{}, err
	}
	return sessionSnapshotFromPortStatus(status), nil
}

func (s *wrappedSession) Read(ctx context.Context, cursor coresandbox.OutputCursor) (coresandbox.OutputSnapshot, error) {
	if s == nil || s.session == nil {
		return coresandbox.OutputSnapshot{}, fmt.Errorf("sandbox/portadapter: session is unavailable")
	}
	stdout, stderr, stdoutCursor, stderrCursor, err := s.session.ReadOutput(ctx, cursor.Stdout, cursor.Stderr)
	if err != nil {
		return coresandbox.OutputSnapshot{}, err
	}
	return coresandbox.OutputSnapshot{
		Stdout: string(stdout),
		Stderr: string(stderr),
		Cursor: coresandbox.OutputCursor{Stdout: stdoutCursor, Stderr: stderrCursor},
	}, nil
}

func (s *wrappedSession) Write(ctx context.Context, input []byte) error {
	if s == nil || s.session == nil {
		return fmt.Errorf("sandbox/portadapter: session is unavailable")
	}
	return s.session.WriteInput(ctx, slices.Clone(input))
}

func (s *wrappedSession) Cancel(ctx context.Context) error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Terminate(ctx)
}

func (s *wrappedSession) Wait(ctx context.Context) (coresandbox.CommandResult, error) {
	if s == nil || s.session == nil {
		return coresandbox.CommandResult{}, fmt.Errorf("sandbox/portadapter: session is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		status, err := s.session.Status(ctx)
		if err != nil {
			return coresandbox.CommandResult{}, err
		}
		if !status.Running {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return coresandbox.CommandResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	result, err := s.session.Result(ctx)
	return commandResultFromPort(result), err
}

func (s *wrappedSession) Close() error {
	return nil
}

func progressContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return portsandbox.ContextWithPrepareProgress(ctx, func(progress portsandbox.PrepareProgress) {
		coresandbox.ReportPrepareProgress(ctx, prepareProgressFromPort(progress))
	})
}

func commandRequestToPort(req coresandbox.CommandRequest) portsandbox.CommandRequest {
	out := portsandbox.CommandRequest{
		Command:     strings.TrimSpace(req.Command),
		Dir:         strings.TrimSpace(req.Dir),
		Timeout:     req.Timeout,
		IdleTimeout: req.IdleTimeout,
		TTY:         req.TTY,
		Env:         maps.Clone(req.Env),
		Stdin:       slices.Clone(req.Stdin),
		Constraints: constraintsToPort(req.Constraints),
	}
	out.Permission = out.Constraints.Permission
	out.RouteHint = out.Constraints.Route
	out.Backend = out.Constraints.Backend
	if req.OnOutput != nil {
		out.OnOutput = func(chunk portsandbox.OutputChunk) {
			req.OnOutput(coresandbox.OutputChunk{
				Stream: strings.TrimSpace(chunk.Stream),
				Text:   chunk.Text,
			})
		}
	}
	return out
}

func commandResultFromPort(in portsandbox.CommandResult) coresandbox.CommandResult {
	return coresandbox.CommandResult{
		Stdout:   in.Stdout,
		Stderr:   in.Stderr,
		Error:    strings.TrimSpace(in.Error),
		ExitCode: in.ExitCode,
		Route:    coreRoute(in.Route),
		Backend:  coreBackend(in.Backend),
	}
}

func descriptorFromPort(in portsandbox.Descriptor) coresandbox.Descriptor {
	return coresandbox.Descriptor{
		Backend:            coreBackend(in.Backend),
		Isolation:          coreIsolation(in.Isolation),
		Capabilities:       capabilitySetFromPort(in.Capabilities),
		DefaultConstraints: constraintsFromPort(in.DefaultConstraints),
	}
}

func capabilitySetFromPort(in portsandbox.CapabilitySet) coresandbox.CapabilitySet {
	return coresandbox.CapabilitySet{
		FileSystem:     in.FileSystem,
		CommandExec:    in.CommandExec,
		AsyncSessions:  in.AsyncSessions,
		TTY:            in.TTY,
		NetworkControl: in.NetworkControl,
		PathPolicy:     in.PathPolicy,
		EnvPolicy:      in.EnvPolicy,
	}
}

func statusFromPort(in portsandbox.Status) coresandbox.Status {
	return coresandbox.Status{
		RequestedBackend:    coreBackend(in.RequestedBackend),
		ResolvedBackend:     coreBackend(in.ResolvedBackend),
		FallbackToHost:      in.FallbackToHost,
		FallbackReason:      strings.TrimSpace(in.FallbackReason),
		FallbackInstallHint: strings.TrimSpace(in.FallbackInstallHint),
		Setup:               setupStatusFromPort(in.Setup),
	}
}

func setupStatusFromPort(in portsandbox.SetupStatus) coresandbox.SetupStatus {
	out := coresandbox.SetupStatus{
		Required: in.Required,
		Error:    strings.TrimSpace(in.Error),
		Details:  maps.Clone(in.Details),
		Counts:   maps.Clone(in.Counts),
		Checks:   make([]coresandbox.SetupCheck, 0, len(in.Checks)),
	}
	for _, check := range in.Checks {
		out.Checks = append(out.Checks, coresandbox.SetupCheck{
			Name:      strings.TrimSpace(check.Name),
			Scope:     coreSetupScope(check.Scope),
			Current:   check.Current,
			Required:  check.Required,
			Reason:    strings.TrimSpace(check.Reason),
			Error:     strings.TrimSpace(check.Error),
			Version:   check.Version,
			Root:      strings.TrimSpace(check.Root),
			UpdatedAt: check.UpdatedAt,
			Details:   maps.Clone(check.Details),
			Counts:    maps.Clone(check.Counts),
		})
	}
	return out
}

func prepareProgressFromPort(in portsandbox.PrepareProgress) coresandbox.PrepareProgress {
	return coresandbox.PrepareProgress{
		Phase:   strings.TrimSpace(in.Phase),
		Message: strings.TrimSpace(in.Message),
		Step:    in.Step,
		Total:   in.Total,
		Done:    in.Done,
		Debug:   in.Debug,
	}
}

func constraintsToPort(in coresandbox.Constraints) portsandbox.Constraints {
	out := portsandbox.Constraints{
		Route:      portRoute(in.Route),
		Backend:    portBackend(in.Backend),
		Permission: portPermission(in.Permission),
		Isolation:  portIsolation(in.Isolation),
		Network:    portNetwork(in.Network),
		PathRules:  make([]portsandbox.PathRule, 0, len(in.PathRules)),
	}
	for _, rule := range in.PathRules {
		out.PathRules = append(out.PathRules, portsandbox.PathRule{
			Path:   strings.TrimSpace(rule.Path),
			Access: portsandbox.PathAccess(strings.TrimSpace(string(rule.Access))),
		})
	}
	return portsandbox.NormalizeConstraints(out)
}

func constraintsFromPort(in portsandbox.Constraints) coresandbox.Constraints {
	out := coresandbox.Constraints{
		Route:      coreRoute(in.Route),
		Backend:    coreBackend(in.Backend),
		Permission: corePermission(in.Permission),
		Isolation:  coreIsolation(in.Isolation),
		Network:    coreNetwork(in.Network),
		PathRules:  make([]coresandbox.PathRule, 0, len(in.PathRules)),
	}
	for _, rule := range in.PathRules {
		out.PathRules = append(out.PathRules, coresandbox.PathRule{
			Path:   strings.TrimSpace(rule.Path),
			Access: coresandbox.PathAccess(strings.TrimSpace(string(rule.Access))),
		})
	}
	return coresandbox.NormalizeConstraints(out)
}

func sessionRefToPort(in coresandbox.SessionRef) portsandbox.SessionRef {
	return portsandbox.SessionRef{
		Backend:   portBackend(in.Backend),
		SessionID: strings.TrimSpace(in.ID),
	}
}

func sessionRefFromPort(in portsandbox.SessionRef) coresandbox.SessionRef {
	return coresandbox.SessionRef{
		ID:      strings.TrimSpace(in.SessionID),
		Backend: coreBackend(in.Backend),
	}
}

func terminalRefFromPort(in portsandbox.TerminalRef) coresandbox.TerminalRef {
	return coresandbox.TerminalRef{
		ID:        strings.TrimSpace(in.TerminalID),
		SessionID: strings.TrimSpace(in.SessionID),
	}
}

func sessionSnapshotFromPortStatus(status portsandbox.SessionStatus) coresandbox.SessionSnapshot {
	state := coresandbox.SessionCompleted
	if status.Running {
		state = coresandbox.SessionRunning
	} else if status.ExitCode != 0 {
		state = coresandbox.SessionFailed
	}
	return coresandbox.SessionSnapshot{
		Ref:           sessionRefFromPort(status.SessionRef),
		State:         state,
		Running:       status.Running,
		SupportsInput: status.SupportsInput,
		ExitCode:      status.ExitCode,
		StartedAt:     status.StartedAt,
		UpdatedAt:     status.UpdatedAt,
		Terminal:      terminalRefFromPort(status.Terminal),
	}
}

func portBackends(values []coresandbox.Backend) []portsandbox.Backend {
	if len(values) == 0 {
		return nil
	}
	out := make([]portsandbox.Backend, 0, len(values))
	for _, value := range values {
		out = append(out, portBackend(value))
	}
	return out
}

func portBackend(value coresandbox.Backend) portsandbox.Backend {
	return portsandbox.Backend(strings.TrimSpace(string(value)))
}

func coreBackend(value portsandbox.Backend) coresandbox.Backend {
	return coresandbox.Backend(strings.TrimSpace(string(value)))
}

func portRoute(value coresandbox.Route) portsandbox.Route {
	return portsandbox.Route(strings.TrimSpace(string(value)))
}

func coreRoute(value portsandbox.Route) coresandbox.Route {
	return coresandbox.Route(strings.TrimSpace(string(value)))
}

func portPermission(value coresandbox.Permission) portsandbox.Permission {
	return portsandbox.Permission(strings.TrimSpace(string(value)))
}

func corePermission(value portsandbox.Permission) coresandbox.Permission {
	return coresandbox.Permission(strings.TrimSpace(string(value)))
}

func portIsolation(value coresandbox.Isolation) portsandbox.Isolation {
	return portsandbox.Isolation(strings.TrimSpace(string(value)))
}

func coreIsolation(value portsandbox.Isolation) coresandbox.Isolation {
	return coresandbox.Isolation(strings.TrimSpace(string(value)))
}

func portNetwork(value coresandbox.Network) portsandbox.Network {
	return portsandbox.Network(strings.TrimSpace(string(value)))
}

func coreNetwork(value portsandbox.Network) coresandbox.Network {
	return coresandbox.Network(strings.TrimSpace(string(value)))
}

func coreSetupScope(value portsandbox.SetupScope) coresandbox.SetupScope {
	switch value {
	case portsandbox.SetupScopeGlobal:
		return coresandbox.SetupGlobal
	case portsandbox.SetupScopeWorkspace:
		return coresandbox.SetupWorkspace
	default:
		return coresandbox.SetupScope(strings.TrimSpace(string(value)))
	}
}

var _ coresandbox.Runtime = (*Runtime)(nil)
var _ coresandbox.PreparableRuntime = (*Runtime)(nil)
var _ coresandbox.RepairableRuntime = (*Runtime)(nil)
var _ coresandbox.PreflightRuntime = (*Runtime)(nil)
var _ coresandbox.ResettableRuntime = (*Runtime)(nil)
var _ coresandbox.Session = (*wrappedSession)(nil)
