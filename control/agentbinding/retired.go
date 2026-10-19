package agentbinding

import "errors"

// ErrRetiredHandle rejects legacy Memory mutations without saving configuration
// or scheduling work. Control owns this compatibility boundary until the minimum
// supported client and configuration versions exclude Memory bindings.
var ErrRetiredHandle = errors.New("control/agentbinding: Core Memory is retired; no configuration was saved or work performed")

// IsRetired reports a reserved legacy Memory handle. Persisted bindings are
// ignored, and the names cannot be reused as custom roles.
func IsRetired(handle Handle) bool {
	switch NormalizeHandle(handle) {
	case HandleSteward, HandleMemoryVerifier:
		return true
	default:
		return false
	}
}
