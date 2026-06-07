package sandbox

import "time"

// BackendName identifies a concrete sandbox backend implementation.
type BackendName string

const (
	BackendHost     BackendName = "host"
	BackendSeatbelt BackendName = "seatbelt"
	BackendBwrap    BackendName = "bwrap"
	BackendLandlock BackendName = "landlock"
	BackendWindows  BackendName = "windows"
	// BackendWindowsElevated is a legacy request alias normalized to
	// BackendWindows by routing code.
	BackendWindowsElevated BackendName = "windows-elevated"
)

// Route identifies whether a command should run on host or through a sandbox.
type Route string

const (
	RouteAuto    Route = ""
	RouteHost    Route = "host"
	RouteSandbox Route = "sandbox"
)

// Descriptor describes a sandbox backend's identity and capabilities.
type Descriptor struct {
	Name        string
	Description string
	Platform    string
	Features    []string
}

// Config holds configuration for creating a sandbox backend.
type Config struct {
	BackendName string
	RootDir     string
	Constraints Constraints
	Env         map[string]string
}

// RouteRequest describes the runtime context used to select a backend.
type RouteRequest struct {
	WorkspaceRoot    string
	RequestedBackend string
	Constraints      Constraints
	Metadata         map[string]any
	Available        []Descriptor
}

// Permission describes the sandbox permission level.
type Permission string

const (
	PermissionDefault   Permission = "default"
	PermissionEscalated Permission = "require_escalated"
)

// PathAccess describes the access level for a path rule.
type PathAccess string

const (
	PathAccessRead  PathAccess = "read"
	PathAccessWrite PathAccess = "write"
)

// PathRule describes a path access rule.
type PathRule struct {
	Path   string
	Access PathAccess
}

// Constraints define the boundaries for sandbox execution.
type Constraints struct {
	Paths      []PathRule
	Permission Permission
	Network    bool
}

// CommandRequest is the input to Backend.Run.
type CommandRequest struct {
	Command     string
	Args        []string
	Dir         string
	Env         map[string]string
	Stdin       []byte
	Timeout     int         // seconds
	Constraints Constraints // policy constraints for this command
}

// CommandResult is the output of Backend.Run.
type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	TimedOut bool
}

// Status describes the current state of a sandbox backend.
type Status struct {
	Running bool
	PID     int
	Details map[string]any
}

// FileSystem provides sandboxed file operations.
type FileSystem interface {
	Read(path string) ([]byte, error)
	Write(path string, data []byte) error
	List(path string) ([]string, error)
	Exists(path string) (bool, error)
	Delete(path string) error
	Stat(path string) (FileInfo, error)
}

// FileInfo describes a file or directory.
type FileInfo struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}
