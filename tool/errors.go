package tool

import "fmt"

// Pure error types — Error() methods are allowed in Phase 1 because
// they are required by the error interface and contain no business logic.

// ErrToolNotFound is returned when a tool name cannot be resolved.
type ErrToolNotFound struct {
	Name string
}

func (e *ErrToolNotFound) Error() string {
	return fmt.Sprintf("tool not found: %s", e.Name)
}

// ErrToolDenied is returned when policy denies a tool call.
type ErrToolDenied struct {
	Name   string
	Reason string
}

func (e *ErrToolDenied) Error() string {
	return fmt.Sprintf("tool denied: %s: %s", e.Name, e.Reason)
}
