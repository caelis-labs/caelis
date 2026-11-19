package agentbinding

import (
	"errors"
	"fmt"
)

// ErrRetiredHandle rejects retired handle mutations without saving configuration
// or scheduling work. Control owns this compatibility boundary until the minimum
// supported client and configuration versions exclude retired bindings.
var ErrRetiredHandle = errors.New("control/agentbinding: the requested handle is retired; no configuration was saved or work performed")

// IsRetired reports a reserved retired system handle. Persisted bindings are
// ignored, and the names cannot be reused as custom roles.
func IsRetired(handle Handle) bool {
	switch NormalizeHandle(handle) {
	case HandleToolSearch, HandleSteward, HandleMemoryVerifier:
		return true
	default:
		return false
	}
}
func retiredHandleError(handle Handle) error {
	if handle == HandleToolSearch {
		return fmt.Errorf("%w (ToolSearch is retired)", ErrRetiredHandle)
	}
	return fmt.Errorf("%w (Core Memory is retired)", ErrRetiredHandle)
}
