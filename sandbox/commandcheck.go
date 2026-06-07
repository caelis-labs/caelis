package sandbox

import (
	"fmt"
	"os"
)

// CheckCommandConstraints validates a command's working directory against
// constraints. Resolves empty dir to the actual cwd.
// Returns nil if allowed, or an error describing the denial.
// Fails closed: if cwd cannot be resolved, denies the command.
func CheckCommandConstraints(dir string, constraints Constraints) error {
	if len(constraints.Paths) == 0 {
		return nil
	}
	workDir := dir
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("sandbox: cannot resolve working directory: %w", err)
		}
		workDir = cwd
	}
	if err := CheckPathAccess(workDir, PathAccessRead, constraints); err != nil {
		return fmt.Errorf("sandbox: command working directory denied: %w", err)
	}
	return nil
}
