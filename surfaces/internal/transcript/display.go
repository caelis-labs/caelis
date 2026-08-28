package transcript

import "github.com/caelis-labs/caelis/control/appserver/eventstream"

// ToolStatusStarted and ToolStatusRunning are runtime-facing intermediate
// states. Completed and failed intentionally reuse ACP schema values; the
// extra final states are local display normalization outputs.
const (
	ToolStatusStarted     = "started"
	ToolStatusRunning     = "running"
	ToolStatusCompleted   = eventstream.ToolStatusCompleted
	ToolStatusFailed      = eventstream.ToolStatusFailed
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
