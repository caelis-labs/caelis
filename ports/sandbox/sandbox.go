package sandbox

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// FileSystem defines file operations shared by tool runtimes and ACP bridges.
type FileSystem interface {
	Getwd() (string, error)
	UserHomeDir() (string, error)
	Open(path string) (*os.File, error)
	ReadDir(path string) ([]os.DirEntry, error)
	Stat(path string) (os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Glob(pattern string) ([]string, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
}

// Permission identifies one requested sandbox permission level.
type Permission string

const (
	PermissionDefault        Permission = "default"
	PermissionWorkspaceWrite Permission = "workspace_write"
	PermissionFullAccess     Permission = "danger_full_access"
)

// Backend identifies one concrete sandbox backend implementation.
type Backend string

const (
	BackendHost     Backend = "host"
	BackendSeatbelt Backend = "seatbelt"
	BackendBwrap    Backend = "bwrap"
	BackendLandlock Backend = "landlock"
	BackendWindows  Backend = "windows"
	// BackendWindowsElevated is a legacy alias normalized to BackendWindows.
	BackendWindowsElevated Backend = "windows-elevated"
	BackendCustom          Backend = "custom"
)

// Route identifies one preferred execution route.
type Route string

const (
	RouteAuto    Route = ""
	RouteHost    Route = "host"
	RouteSandbox Route = "sandbox"
)

// Isolation identifies the isolation family provided by one backend.
type Isolation string

const (
	IsolationHost      Isolation = "host"
	IsolationProcess   Isolation = "process"
	IsolationContainer Isolation = "container"
)

// Network identifies the desired network policy for one execution request.
type Network string

const (
	NetworkInherit  Network = "inherit"
	NetworkEnabled  Network = "enabled"
	NetworkDisabled Network = "disabled"
)

// PathAccess identifies one path policy grant.
type PathAccess string

const (
	PathAccessReadOnly  PathAccess = "read_only"
	PathAccessReadWrite PathAccess = "read_write"
	PathAccessHidden    PathAccess = "hidden"
)

// PathRule is one abstract filesystem rule inspired by common sandbox backends
// such as seatbelt and bwrap.
type PathRule struct {
	Path   string     `json:"path,omitempty"`
	Access PathAccess `json:"access,omitempty"`
}

// CapabilitySet identifies which sandbox capability families one backend
// exposes. These are capability facts, not product policy decisions.
type CapabilitySet struct {
	FileSystem     bool `json:"file_system,omitempty"`
	CommandExec    bool `json:"command_exec,omitempty"`
	AsyncSessions  bool `json:"async_sessions,omitempty"`
	TTY            bool `json:"tty,omitempty"`
	NetworkControl bool `json:"network_control,omitempty"`
	PathPolicy     bool `json:"path_policy,omitempty"`
	EnvPolicy      bool `json:"env_policy,omitempty"`
}

// Constraints is the backend-neutral execution contract consumed by runtime,
// tools, and future policy.
type Constraints struct {
	Route      Route      `json:"route,omitempty"`
	Backend    Backend    `json:"backend,omitempty"`
	Permission Permission `json:"permission,omitempty"`
	Isolation  Isolation  `json:"isolation,omitempty"`
	Network    Network    `json:"network,omitempty"`
	PathRules  []PathRule `json:"path_rules,omitempty"`
}

// Descriptor describes one concrete sandbox backend and its default contract.
type Descriptor struct {
	Backend            Backend       `json:"backend,omitempty"`
	Isolation          Isolation     `json:"isolation,omitempty"`
	Capabilities       CapabilitySet `json:"capabilities,omitempty"`
	DefaultConstraints Constraints   `json:"default_constraints,omitempty"`
}

// Config configures one composed sandbox runtime.
type Config struct {
	CWD                 string    `json:"cwd,omitempty"`
	RequestedBackend    Backend   `json:"requested_backend,omitempty"`
	BackendCandidates   []Backend `json:"backend_candidates,omitempty"`
	FallbackInstallHint string    `json:"fallback_install_hint,omitempty"`
	HelperPath          string    `json:"helper_path,omitempty"`
	StateDir            string    `json:"state_dir,omitempty"`
	ReadableRoots       []string  `json:"readable_roots,omitempty"`
	WritableRoots       []string  `json:"writable_roots,omitempty"`
	ReadOnlySubpaths    []string  `json:"read_only_subpaths,omitempty"`
}

// Status reports backend selection and fallback state for one runtime.
type Status struct {
	RequestedBackend    Backend     `json:"requested_backend,omitempty"`
	ResolvedBackend     Backend     `json:"resolved_backend,omitempty"`
	FallbackToHost      bool        `json:"fallback_to_host,omitempty"`
	FallbackReason      string      `json:"fallback_reason,omitempty"`
	FallbackInstallHint string      `json:"fallback_install_hint,omitempty"`
	Setup               SetupStatus `json:"setup,omitempty"`
}

// SetupScope identifies the lifecycle scope of one backend setup check.
type SetupScope string

const (
	SetupScopeGlobal    SetupScope = "global"
	SetupScopeWorkspace SetupScope = "workspace"
)

// SetupStatus reports backend setup readiness without exposing platform-specific
// fields in the stable sandbox contract. Implementations can attach
// backend-specific values through Details and Counts.
type SetupStatus struct {
	Required bool              `json:"required,omitempty"`
	Error    string            `json:"error,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
	Counts   map[string]int    `json:"counts,omitempty"`
	Checks   []SetupCheck      `json:"checks,omitempty"`
}

// SetupCheck reports one scoped setup requirement such as global backend
// infrastructure or per-workspace authorization.
type SetupCheck struct {
	Name      string            `json:"name,omitempty"`
	Scope     SetupScope        `json:"scope,omitempty"`
	Current   bool              `json:"current,omitempty"`
	Required  bool              `json:"required,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Error     string            `json:"error,omitempty"`
	Version   int               `json:"version,omitempty"`
	Root      string            `json:"root,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
	Counts    map[string]int    `json:"counts,omitempty"`
}

// PrepareProgress is an optional, best-effort progress update emitted during a
// user-triggered sandbox setup step.
type PrepareProgress struct {
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	Step    int    `json:"step,omitempty"`
	Total   int    `json:"total,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Debug   bool   `json:"debug,omitempty"`
}

// PrepareProgressFunc receives best-effort setup progress. Implementations must
// return quickly; slow callbacks can delay setup.
type PrepareProgressFunc func(PrepareProgress)

type prepareProgressContextKey struct{}

// ContextWithPrepareProgress attaches a setup progress callback to ctx.
func ContextWithPrepareProgress(ctx context.Context, fn PrepareProgressFunc) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, prepareProgressContextKey{}, fn)
}

// ReportPrepareProgress emits one setup progress update if ctx carries a
// progress callback.
func ReportPrepareProgress(ctx context.Context, progress PrepareProgress) {
	if ctx == nil {
		return
	}
	fn, ok := ctx.Value(prepareProgressContextKey{}).(PrepareProgressFunc)
	if !ok || fn == nil {
		return
	}
	progress.Phase = strings.TrimSpace(progress.Phase)
	progress.Message = strings.TrimSpace(progress.Message)
	fn(progress)
}

// OutputChunk is one stdout/stderr streaming fragment.
type OutputChunk struct {
	Stream string `json:"stream,omitempty"`
	Text   string `json:"text,omitempty"`
}

// CommandRequest is one command execution request.
type CommandRequest struct {
	Command     string            `json:"command,omitempty"`
	Dir         string            `json:"dir,omitempty"`
	Timeout     time.Duration     `json:"timeout,omitempty"`
	IdleTimeout time.Duration     `json:"idle_timeout,omitempty"`
	TTY         bool              `json:"tty,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Stdin       []byte            `json:"stdin,omitempty"`

	// Legacy compatibility fields. New callers should prefer Constraints.
	Permission Permission `json:"permission,omitempty"`
	RouteHint  Route      `json:"route_hint,omitempty"`
	Backend    Backend    `json:"backend,omitempty"`

	Constraints Constraints       `json:"constraints,omitempty"`
	OnOutput    func(OutputChunk) `json:"-"`
}

// CommandResult is one finished command execution result.
type CommandResult struct {
	Stdout   string  `json:"stdout,omitempty"`
	Stderr   string  `json:"stderr,omitempty"`
	Error    string  `json:"error,omitempty"`
	ExitCode int     `json:"exit_code,omitempty"`
	Route    Route   `json:"route,omitempty"`
	Backend  Backend `json:"backend,omitempty"`
}

// SessionRef identifies one async execution session.
type SessionRef struct {
	Backend   Backend `json:"backend,omitempty"`
	SessionID string  `json:"session_id,omitempty"`
}

// TerminalRef identifies one terminal-like output stream owned by one async
// command session.
type TerminalRef struct {
	Backend    Backend `json:"backend,omitempty"`
	SessionID  string  `json:"session_id,omitempty"`
	TerminalID string  `json:"terminal_id,omitempty"`
}

// SessionStatus reports one async execution session state.
type SessionStatus struct {
	SessionRef
	Terminal      TerminalRef `json:"terminal,omitempty"`
	Running       bool        `json:"running,omitempty"`
	SupportsInput bool        `json:"supports_input,omitempty"`
	ExitCode      int         `json:"exit_code,omitempty"`
	StartedAt     time.Time   `json:"started_at,omitempty"`
	UpdatedAt     time.Time   `json:"updated_at,omitempty"`
}

// Session represents one async command session.
type Session interface {
	Ref() SessionRef
	Terminal() TerminalRef
	WriteInput(ctx context.Context, input []byte) error
	ReadOutput(ctx context.Context, stdoutMarker, stderrMarker int64) (stdout, stderr []byte, newStdoutMarker, newStderrMarker int64, err error)
	Status(ctx context.Context) (SessionStatus, error)
	Wait(ctx context.Context, timeout time.Duration) (SessionStatus, error)
	Result(ctx context.Context) (CommandResult, error)
	Terminate(ctx context.Context) error
}

// Runner executes one command synchronously.
type Runner interface {
	Run(context.Context, CommandRequest) (CommandResult, error)
}

// AsyncRunner executes commands as resumable sessions.
type AsyncRunner interface {
	Runner
	Start(context.Context, CommandRequest) (Session, error)
	OpenSession(string) (Session, error)
}

// Runtime is the stable execution boundary consumed by future tool and ACP
// implementations.
type Runtime interface {
	Describe() Descriptor
	FileSystem() FileSystem
	FileSystemFor(Constraints) FileSystem
	Run(context.Context, CommandRequest) (CommandResult, error)
	Start(context.Context, CommandRequest) (Session, error)
	OpenSession(string) (Session, error)
	OpenSessionRef(SessionRef) (Session, error)
	SupportedBackends() []Backend
	Status() Status
	Close() error
}

// SelectionStatusRuntime can report backend selection without performing
// backend-specific readiness checks.
type SelectionStatusRuntime interface {
	SelectionStatus() Status
}

func SelectionStatus(runtime Runtime) Status {
	if runtime == nil {
		return Status{}
	}
	if provider, ok := runtime.(SelectionStatusRuntime); ok {
		return CloneStatus(provider.SelectionStatus())
	}
	return CloneStatus(runtime.Status())
}

// PreparableRuntime is implemented by backends that need an explicit
// user-triggered setup step before normal sandboxed execution can run.
type PreparableRuntime interface {
	Prepare(context.Context) error
}

// RepairableRuntime is implemented by backends that can run an explicit
// user-triggered repair step. Implementations may request elevation only from
// this path, never from normal command execution or background preflight.
type RepairableRuntime interface {
	Repair(context.Context) error
}

// PreflightOptions controls best-effort setup checks that must not request
// elevation. Preflight can repair state only when the current user already has
// enough permissions to do so.
type PreflightOptions struct {
	AllowNonElevatedRepair bool `json:"allow_non_elevated_repair,omitempty"`
}

// PreflightRuntime is implemented by backends that can inspect or lightly repair
// setup state without prompting for elevation.
type PreflightRuntime interface {
	Preflight(context.Context, PreflightOptions) error
}

// ResettableRuntime is implemented by backends that can remove local sandbox
// infrastructure through an explicit user-triggered maintenance command.
type ResettableRuntime interface {
	Reset(context.Context) error
}

// BackendFactory builds one concrete backend runtime.
type BackendFactory interface {
	Backend() Backend
	Build(Config) (Runtime, error)
}

var (
	backendFactoriesMu      sync.RWMutex
	backendFactories        = map[Backend]BackendFactory{}
	backendFactoryOrder     []Backend
	backendRegistrationErrs []error
)

// FuncRunner adapts one function into one Runner.
type FuncRunner func(context.Context, CommandRequest) (CommandResult, error)

func (f FuncRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	if f == nil {
		return CommandResult{}, nil
	}
	return CloneResult(f(ctx, CloneRequest(req)))
}

// CloneRequest returns one isolated copy of one command request.
func CloneRequest(in CommandRequest) CommandRequest {
	out := in
	out.Command = strings.TrimSpace(in.Command)
	out.Dir = strings.TrimSpace(in.Dir)
	out.Env = maps.Clone(in.Env)
	out.Stdin = append([]byte(nil), in.Stdin...)
	out.Backend = Backend(strings.TrimSpace(string(in.Backend)))
	out.Constraints = NormalizeConstraints(in.Constraints)
	return out
}

// CloneResult returns one isolated copy of one command result.
func CloneResult(in CommandResult, err error) (CommandResult, error) {
	out := in
	out.Stdout = in.Stdout
	out.Stderr = in.Stderr
	out.Backend = Backend(strings.TrimSpace(string(in.Backend)))
	return out, err
}

// CloneStatus returns one isolated copy of one runtime status.
func CloneStatus(in Status) Status {
	out := in
	out.RequestedBackend = Backend(strings.TrimSpace(string(in.RequestedBackend)))
	out.ResolvedBackend = Backend(strings.TrimSpace(string(in.ResolvedBackend)))
	out.FallbackReason = strings.TrimSpace(in.FallbackReason)
	out.FallbackInstallHint = strings.TrimSpace(in.FallbackInstallHint)
	out.Setup = CloneSetupStatus(in.Setup)
	return out
}

// CloneSetupStatus returns one isolated copy of one setup status.
func CloneSetupStatus(in SetupStatus) SetupStatus {
	out := in
	out.Error = strings.TrimSpace(in.Error)
	out.Details = cloneTrimmedStringMap(in.Details)
	out.Counts = maps.Clone(in.Counts)
	if len(in.Checks) > 0 {
		out.Checks = make([]SetupCheck, len(in.Checks))
		for i, check := range in.Checks {
			out.Checks[i] = CloneSetupCheck(check)
		}
	}
	return out
}

// CloneSetupCheck returns one isolated copy of one setup check.
func CloneSetupCheck(in SetupCheck) SetupCheck {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Scope = SetupScope(strings.TrimSpace(string(in.Scope)))
	out.Reason = strings.TrimSpace(in.Reason)
	out.Error = strings.TrimSpace(in.Error)
	out.Root = strings.TrimSpace(in.Root)
	out.Details = cloneTrimmedStringMap(in.Details)
	out.Counts = maps.Clone(in.Counts)
	return out
}

// Check returns the first setup check with the requested name.
func (s SetupStatus) Check(name string) (SetupCheck, bool) {
	name = strings.TrimSpace(name)
	for _, check := range s.Checks {
		if strings.TrimSpace(check.Name) == name {
			return CloneSetupCheck(check), true
		}
	}
	return SetupCheck{}, false
}

func cloneTrimmedStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeBackendCandidates(values []Backend) []Backend {
	if len(values) == 0 {
		return nil
	}
	out := make([]Backend, 0, len(values))
	seen := map[Backend]struct{}{}
	for _, value := range values {
		backend := Backend(strings.TrimSpace(string(value)))
		if backend == "" || backend == BackendHost {
			continue
		}
		if _, ok := seen[backend]; ok {
			continue
		}
		seen[backend] = struct{}{}
		out = append(out, backend)
	}
	return out
}

// CloneSessionRef returns one normalized copy of one session ref.
func CloneSessionRef(in SessionRef) SessionRef {
	return SessionRef{
		Backend:   Backend(strings.TrimSpace(string(in.Backend))),
		SessionID: strings.TrimSpace(in.SessionID),
	}
}

// CloneTerminalRef returns one normalized copy of one terminal ref.
func CloneTerminalRef(in TerminalRef) TerminalRef {
	return TerminalRef{
		Backend:    Backend(strings.TrimSpace(string(in.Backend))),
		SessionID:  strings.TrimSpace(in.SessionID),
		TerminalID: strings.TrimSpace(in.TerminalID),
	}
}

// CloneSessionStatus returns one normalized copy of one session status.
func CloneSessionStatus(in SessionStatus) SessionStatus {
	out := in
	out.SessionRef = CloneSessionRef(in.SessionRef)
	out.Terminal = CloneTerminalRef(in.Terminal)
	return out
}

// ClonePathRules returns one normalized path-rule slice copy.
func ClonePathRules(in []PathRule) []PathRule {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	for i := range out {
		out[i].Path = strings.TrimSpace(out[i].Path)
		out[i].Access = PathAccess(strings.TrimSpace(string(out[i].Access)))
	}
	return out
}

// NormalizeConstraints returns one normalized copy of one backend-neutral
// sandbox constraint set.
func NormalizeConstraints(in Constraints) Constraints {
	out := in
	out.Route = Route(strings.TrimSpace(string(in.Route)))
	out.Backend = Backend(strings.TrimSpace(string(in.Backend)))
	out.Permission = Permission(strings.TrimSpace(string(in.Permission)))
	out.Isolation = Isolation(strings.TrimSpace(string(in.Isolation)))
	out.Network = Network(strings.TrimSpace(string(in.Network)))
	out.PathRules = ClonePathRules(in.PathRules)
	return out
}

// CloneDescriptor returns one normalized descriptor copy.
func CloneDescriptor(in Descriptor) Descriptor {
	out := in
	out.Backend = Backend(strings.TrimSpace(string(in.Backend)))
	out.Isolation = Isolation(strings.TrimSpace(string(in.Isolation)))
	out.DefaultConstraints = NormalizeConstraints(in.DefaultConstraints)
	return out
}

// EffectiveConstraints merges legacy request fields into the normalized
// backend-neutral constraints contract.
func EffectiveConstraints(req CommandRequest) Constraints {
	out := NormalizeConstraints(req.Constraints)
	if out.Route == "" {
		out.Route = Route(strings.TrimSpace(string(req.RouteHint)))
	}
	if out.Backend == "" {
		out.Backend = Backend(strings.TrimSpace(string(req.Backend)))
	}
	if out.Permission == "" {
		out.Permission = Permission(strings.TrimSpace(string(req.Permission)))
	}
	return out
}
