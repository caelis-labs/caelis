package agentsdk

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// GuardrailFailurePolicy controls infrastructure failures, not explicit
// rejection. A GuardrailRejectionError always rejects the run.
type GuardrailFailurePolicy string

const (
	GuardrailFailClosed GuardrailFailurePolicy = "fail_closed"
	GuardrailFailOpen   GuardrailFailurePolicy = "fail_open"
)

// GuardrailInput is the full mutable user-input view passed between ordered
// guardrails. Runtime deep-copies content before and after every call.
type GuardrailInput struct {
	SessionRef   session.SessionRef  `json:"session_ref"`
	Input        string              `json:"input,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
}

// Guardrail may reject or return the complete input for the next guardrail.
type Guardrail interface {
	Name() string
	ApplyGuardrail(context.Context, GuardrailInput) (GuardrailInput, error)
}

// GuardrailSpec fixes execution order, timeout, and infrastructure-failure
// policy at Runtime assembly.
type GuardrailSpec struct {
	Guardrail Guardrail
	Timeout   time.Duration
	OnFailure GuardrailFailurePolicy
}

// GuardrailRejectionError is an intentional policy rejection and is never
// converted to fail-open.
type GuardrailRejectionError struct {
	Guardrail string
	Reason    string
}

func (e *GuardrailRejectionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk: guardrail %q rejected input: %s", e.Guardrail, e.Reason)
}

func (e *GuardrailRejectionError) ErrorCode() errorcode.Code { return errorcode.PermissionDenied }
