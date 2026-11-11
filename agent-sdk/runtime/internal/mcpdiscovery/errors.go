package mcpdiscovery

import (
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// WithEntryHint adds recovery guidance at the invoking entry point. Manager
// errors describe catalog state without assuming the caller's model interface.
func WithEntryHint(err error, entry string) error {
	if !errorcode.Is(err, tool.ErrorCodeStaleRevision) {
		return err
	}
	hint := "Refresh the catalog if needed, then use " + entry + " to rediscover before calling."
	var existing *tool.ToolError
	if errors.As(err, &existing) && existing.Hint == hint {
		return err
	}
	out := tool.WrapError(tool.ErrorCodeStaleRevision, err, err.Error()+"; "+hint)
	out.Hint = hint
	return out
}
