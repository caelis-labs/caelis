// Package sandbox defines backend-neutral filesystem and command execution
// contracts for Caelis runtimes and tools.
package sandbox

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

type Backend string

const (
	BackendHost     Backend = "host"
	BackendSeatbelt Backend = "seatbelt"
	BackendBwrap    Backend = "bwrap"
	BackendLandlock Backend = "landlock"
	BackendWindows  Backend = "windows"
	BackendCustom   Backend = "custom"
)

type Route string

const (
	RouteAuto    Route = ""
	RouteHost    Route = "host"
	RouteSandbox Route = "sandbox"
)

type Permission string

const (
	PermissionDefault        Permission = "default"
	PermissionWorkspaceWrite Permission = "workspace_write"
	PermissionFullAccess     Permission = "danger_full_access"
)

type Isolation string

const (
	IsolationHost      Isolation = "host"
	IsolationProcess   Isolation = "process"
	IsolationContainer Isolation = "container"
)

type Network string

const (
	NetworkInherit  Network = "inherit"
	NetworkEnabled  Network = "enabled"
	NetworkDisabled Network = "disabled"
)

type PathAccess string

const (
	PathReadOnly  PathAccess = "read_only"
	PathReadWrite PathAccess = "read_write"
	PathHidden    PathAccess = "hidden"
)

type PathRule struct {
	Path   string     `json:"path,omitempty"`
	Access PathAccess `json:"access,omitempty"`
}

type CapabilitySet struct {
	FileSystem     bool `json:"file_system,omitempty"`
	CommandExec    bool `json:"command_exec,omitempty"`
	AsyncSessions  bool `json:"async_sessions,omitempty"`
	TTY            bool `json:"tty,omitempty"`
	NetworkControl bool `json:"network_control,omitempty"`
	PathPolicy     bool `json:"path_policy,omitempty"`
	EnvPolicy      bool `json:"env_policy,omitempty"`
}

type Constraints struct {
	Route      Route      `json:"route,omitempty"`
	Backend    Backend    `json:"backend,omitempty"`
	Permission Permission `json:"permission,omitempty"`
	Isolation  Isolation  `json:"isolation,omitempty"`
	Network    Network    `json:"network,omitempty"`
	PathRules  []PathRule `json:"path_rules,omitempty"`
}

type Descriptor struct {
	Backend            Backend       `json:"backend,omitempty"`
	Isolation          Isolation     `json:"isolation,omitempty"`
	Capabilities       CapabilitySet `json:"capabilities,omitempty"`
	DefaultConstraints Constraints   `json:"default_constraints,omitempty"`
}

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

type SetupScope string

const (
	SetupGlobal    SetupScope = "global"
	SetupWorkspace SetupScope = "workspace"
)

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

type SetupStatus struct {
	Required bool              `json:"required,omitempty"`
	Error    string            `json:"error,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
	Counts   map[string]int    `json:"counts,omitempty"`
	Checks   []SetupCheck      `json:"checks,omitempty"`
}

type Status struct {
	RequestedBackend    Backend     `json:"requested_backend,omitempty"`
	ResolvedBackend     Backend     `json:"resolved_backend,omitempty"`
	FallbackToHost      bool        `json:"fallback_to_host,omitempty"`
	FallbackReason      string      `json:"fallback_reason,omitempty"`
	FallbackInstallHint string      `json:"fallback_install_hint,omitempty"`
	Setup               SetupStatus `json:"setup,omitempty"`
}

type FileSystem interface {
	Getwd() (string, error)
	UserHomeDir() (string, error)
	Open(path string) (*os.File, error)
	ReadDir(path string) ([]os.DirEntry, error)
	Stat(path string) (os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Glob(pattern string) ([]string, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
}

type OutputChunk struct {
	Stream string `json:"stream,omitempty"`
	Text   string `json:"text,omitempty"`
}

type CommandRequest struct {
	Command     string            `json:"command,omitempty"`
	Dir         string            `json:"dir,omitempty"`
	Timeout     time.Duration     `json:"timeout,omitempty"`
	IdleTimeout time.Duration     `json:"idle_timeout,omitempty"`
	TTY         bool              `json:"tty,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Stdin       []byte            `json:"stdin,omitempty"`
	Constraints Constraints       `json:"constraints,omitempty"`
	OnOutput    func(OutputChunk) `json:"-"`
}

type CommandResult struct {
	Stdout   string  `json:"stdout,omitempty"`
	Stderr   string  `json:"stderr,omitempty"`
	Error    string  `json:"error,omitempty"`
	ExitCode int     `json:"exit_code,omitempty"`
	Route    Route   `json:"route,omitempty"`
	Backend  Backend `json:"backend,omitempty"`
}

type SessionRef struct {
	ID      string  `json:"id,omitempty"`
	Backend Backend `json:"backend,omitempty"`
}

type SessionState string

const (
	SessionRunning   SessionState = "running"
	SessionCompleted SessionState = "completed"
	SessionFailed    SessionState = "failed"
	SessionCancelled SessionState = "cancelled"
)

type TerminalRef struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type OutputCursor struct {
	Stdout int64 `json:"stdout,omitempty"`
	Stderr int64 `json:"stderr,omitempty"`
}

type OutputSnapshot struct {
	Stdout             string       `json:"stdout,omitempty"`
	Stderr             string       `json:"stderr,omitempty"`
	Cursor             OutputCursor `json:"cursor,omitempty"`
	StdoutDroppedBytes int64        `json:"stdout_dropped_bytes,omitempty"`
	StderrDroppedBytes int64        `json:"stderr_dropped_bytes,omitempty"`
}

type SessionSnapshot struct {
	Ref           SessionRef   `json:"ref,omitempty"`
	Command       string       `json:"command,omitempty"`
	Dir           string       `json:"dir,omitempty"`
	State         SessionState `json:"state,omitempty"`
	Running       bool         `json:"running,omitempty"`
	SupportsInput bool         `json:"supports_input,omitempty"`
	ExitCode      int          `json:"exit_code,omitempty"`
	Error         string       `json:"error,omitempty"`
	StartedAt     time.Time    `json:"started_at,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at,omitempty"`
	Terminal      TerminalRef  `json:"terminal,omitempty"`
}

type SessionListQuery struct {
	Limit int `json:"limit,omitempty"`
}

type Session interface {
	Ref() SessionRef
	Snapshot(context.Context) (SessionSnapshot, error)
	Read(context.Context, OutputCursor) (OutputSnapshot, error)
	Write(context.Context, []byte) error
	Cancel(context.Context) error
	Wait(context.Context) (CommandResult, error)
	Close() error
}

type SessionLister interface {
	ListSessions(context.Context, SessionListQuery) ([]SessionSnapshot, error)
}

type Runtime interface {
	Descriptor() Descriptor
	Status() Status
	FileSystem() FileSystem
	Run(context.Context, CommandRequest) (CommandResult, error)
	Start(context.Context, CommandRequest) (Session, error)
	Open(context.Context, SessionRef) (Session, error)
	Close() error
}

type BackendFactory interface {
	NewRuntime(context.Context, Config) (Runtime, error)
}

func NormalizeConstraints(in Constraints) Constraints {
	out := in
	out.PathRules = slices.Clone(in.PathRules)
	for i := range out.PathRules {
		out.PathRules[i].Path = strings.TrimSpace(out.PathRules[i].Path)
	}
	return out
}

func CloneDescriptor(in Descriptor) Descriptor {
	out := in
	out.DefaultConstraints = NormalizeConstraints(in.DefaultConstraints)
	return out
}

func CloneStatus(in Status) Status {
	out := in
	out.Setup = CloneSetupStatus(in.Setup)
	return out
}

func CloneSetupStatus(in SetupStatus) SetupStatus {
	out := in
	out.Details = maps.Clone(in.Details)
	out.Counts = maps.Clone(in.Counts)
	out.Checks = slices.Clone(in.Checks)
	for i := range out.Checks {
		out.Checks[i].Details = maps.Clone(out.Checks[i].Details)
		out.Checks[i].Counts = maps.Clone(out.Checks[i].Counts)
	}
	return out
}
