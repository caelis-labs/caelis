package sandbox

import "context"

// Backend is the contract for a sandbox execution environment.
type Backend interface {
	// Name returns the backend identifier.
	Name() string

	// Describe returns the backend's capabilities.
	Describe(context.Context) (Descriptor, error)

	// Run executes a command in the sandbox.
	Run(context.Context, CommandRequest) (CommandResult, error)

	// FileSystem returns a sandboxed filesystem accessor.
	FileSystem(context.Context, Constraints) (FileSystem, error)

	// Status returns the current backend status.
	Status(context.Context) (Status, error)

	// Close releases sandbox resources.
	Close() error
}

// Factory creates sandbox backends.
type Factory interface {
	// Create creates a new sandbox backend with the given config.
	Create(context.Context, Config) (Backend, error)

	// Available returns available backend descriptors.
	Available(context.Context) ([]Descriptor, error)
}
