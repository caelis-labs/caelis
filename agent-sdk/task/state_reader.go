package task

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// StateSnapshot is a detached observation of the Task fields needed to discover
// communication input boundaries. It does not authorize input or effects.
// Result, specification, and unrelated producer metadata are deliberately absent.
type StateSnapshot struct {
	TaskID           string
	Revision         uint64
	State            State
	SupportsSteering bool
}

// StateReader is an optional lightweight Task observation capability. An empty
// taskIDs slice reads all Tasks in the Session; otherwise only the named Task
// IDs in that Session are returned. Missing Tasks are omitted. Each call reads
// current durable values; callers must still use the execution owner's ordinary
// membership, generation, and input-admission checks before dispatching.
type StateReader interface {
	ReadTaskStates(context.Context, session.SessionRef, []string) ([]StateSnapshot, error)
}
