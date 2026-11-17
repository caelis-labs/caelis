package session

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// ApprovalSchemaVersion versions approval request and resolution facts.
const ApprovalSchemaVersion = 1

// ApprovalRecordStatus identifies an approval request or decision state.
type ApprovalRecordStatus string

const (
	ApprovalRecordPending   ApprovalRecordStatus = "pending"
	ApprovalRecordResolved  ApprovalRecordStatus = "resolved"
	ApprovalRecordCancelled ApprovalRecordStatus = "cancelled"
)

// ApprovalRecord is the durable handoff between a waiting run and an approval
// resolver. It contains only semantic request/decision data. ItemID identifies
// the Runtime-admitted tool step when available; legacy approval facts omit it.
type ApprovalRecord struct {
	// Authorization is approval-domain evidence, independent of Run/Turn state.
	Authorization               *ApprovalAuthorization `json:"authorization,omitempty"`
	PolicyRuleID                string                 `json:"policy_rule_id,omitempty"`
	PolicyConfigurationRevision uint64                 `json:"policy_configuration_revision,omitempty"`
	Schema                      int                    `json:"schema"`
	RequestID                   string                 `json:"request_id"`
	SessionID                   string                 `json:"session_id"`
	RunID                       string                 `json:"run_id"`
	TurnID                      string                 `json:"turn_id"`
	ToolCallID                  string                 `json:"tool_call_id"`
	ToolName                    string                 `json:"tool_name"`
	ItemID                      string                 `json:"item_id,omitempty"`
	Revision                    uint64                 `json:"revision"`
	Status                      ApprovalRecordStatus   `json:"status"`
	Input                       json.RawMessage        `json:"input,omitempty"`
	Approval                    *ProtocolApproval      `json:"approval,omitempty"`
	// Presentation is the owner-redacted approval snapshot used by display
	// replay. Input and Approval retain the original private execution evidence.
	Presentation *ProtocolApproval `json:"presentation,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	Outcome      string            `json:"outcome,omitempty"`
	OptionID     string            `json:"option_id,omitempty"`
	Approved     bool              `json:"approved,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	ReviewText   string            `json:"review_text,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// ApprovalRecordTransitionError rejects an invalid approval decision update.
type ApprovalRecordTransitionError struct {
	RequestID string
	From      ApprovalRecordStatus
	To        ApprovalRecordStatus
	Detail    string
}

func (e *ApprovalRecordTransitionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/session: approval record %q transition %q -> %q: %s", e.RequestID, e.From, e.To, strings.TrimSpace(e.Detail))
}

func (e *ApprovalRecordTransitionError) ErrorCode() errorcode.Code {
	return errorcode.FailedPrecondition
}

// CloneApprovalRecord returns an isolated approval fact.
func CloneApprovalRecord(in ApprovalRecord) ApprovalRecord {
	out := in
	out.Authorization = CloneApprovalAuthorization(in.Authorization)
	out.RequestID = strings.TrimSpace(in.RequestID)
	out.SessionID = strings.TrimSpace(in.SessionID)
	out.RunID = strings.TrimSpace(in.RunID)
	out.TurnID = strings.TrimSpace(in.TurnID)
	out.ToolCallID = strings.TrimSpace(in.ToolCallID)
	out.ToolName = strings.TrimSpace(in.ToolName)
	out.ItemID = strings.TrimSpace(in.ItemID)
	out.Status = ApprovalRecordStatus(strings.TrimSpace(string(in.Status)))
	out.Input = append(json.RawMessage(nil), in.Input...)
	if in.Approval != nil {
		approval := CloneProtocolApproval(*in.Approval)
		out.Approval = &approval
		if in.Approval.Context != nil {
			out.ToolCallID, out.ToolName = in.ToolCallID, in.ToolName
		}
	}
	if in.Presentation != nil {
		presentation := CloneProtocolApproval(*in.Presentation)
		out.Presentation = &presentation
	}
	out.Metadata = CloneState(in.Metadata)
	out.Outcome = strings.TrimSpace(in.Outcome)
	out.OptionID = strings.TrimSpace(in.OptionID)
	out.Reason = strings.TrimSpace(in.Reason)
	out.ReviewText = strings.TrimSpace(in.ReviewText)
	return out
}

// ValidateApprovalRecordTransition validates one durable approval decision update.
func ValidateApprovalRecordTransition(previous ApprovalRecord, next ApprovalRecord) error {
	previous = CloneApprovalRecord(previous)
	next = CloneApprovalRecord(next)
	invalid := func(detail string) error {
		return &ApprovalRecordTransitionError{RequestID: next.RequestID, From: previous.Status, To: next.Status, Detail: detail}
	}
	if next.Schema != ApprovalSchemaVersion || next.RequestID == "" || next.SessionID == "" || next.RunID == "" || next.TurnID == "" {
		return invalid("invalid schema or identity")
	}
	if err := next.Authorization.Validate(next.Approval); err != nil {
		return invalid(err.Error())
	}
	if previous.Status == "" {
		if next.Status == ApprovalRecordPending && next.Revision == 1 {
			return nil
		}
		return invalid("first record must be pending revision 1")
	}
	if !reflect.DeepEqual(previous.Authorization, next.Authorization) {
		return invalid("authorization changed while approval was pending")
	}
	if previous.RequestID != next.RequestID || previous.SessionID != next.SessionID || previous.RunID != next.RunID || previous.ItemID != next.ItemID || next.Revision != previous.Revision+1 {
		return invalid("identity or revision mismatch")
	}
	if previous.Status != ApprovalRecordPending || (next.Status != ApprovalRecordResolved && next.Status != ApprovalRecordCancelled) {
		return invalid("transition is not allowed")
	}
	return nil
}

// ValidateApprovalRecord validates one self-contained approval fact. Only the
// approval owner validates its transition and live decision authority.
func ValidateApprovalRecord(record ApprovalRecord) error {
	if record.Schema != ApprovalSchemaVersion || record.RequestID == "" || record.SessionID == "" || record.RunID == "" || record.TurnID == "" {
		return fmt.Errorf("invalid approval identity or schema")
	}
	switch record.Status {
	case ApprovalRecordPending:
		if record.Revision != 1 {
			return fmt.Errorf("pending approval revision must be 1")
		}
	case ApprovalRecordResolved, ApprovalRecordCancelled:
		if record.Revision != 2 {
			return fmt.Errorf("settled approval revision must be 2")
		}
	default:
		return fmt.Errorf("invalid approval status")
	}
	return record.Authorization.Validate(record.Approval)
}
