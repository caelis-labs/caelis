package transcript

import "github.com/caelis-labs/caelis/protocol/acp/schema"

// ToolStatusStarted and ToolStatusRunning are runtime-facing intermediate
// states. Completed and failed intentionally reuse ACP schema values; the
// extra final states are local display normalization outputs.
const (
	ToolStatusStarted     = "started"
	ToolStatusRunning     = "running"
	ToolStatusCompleted   = schema.ToolStatusCompleted
	ToolStatusFailed      = schema.ToolStatusFailed
	ToolStatusInterrupted = "interrupted"
	ToolStatusCancelled   = "cancelled"
)

type ToolOutputFallbackInput struct {
	ToolName  string
	ToolKind  string
	RawOutput map[string]any
	Meta      map[string]any
	Status    string
	Error     bool
}
