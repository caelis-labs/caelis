package session

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ContextEditProvenance identifies an appended hook context's original source.
// It conveys no user authority and cannot be used to dispatch an operation.
type ContextEditProvenance struct {
	Version          int    `json:"version"`
	SourceStamp      string `json:"source_stamp"`
	SourceThroughID  string `json:"source_through_id"`
	SourceThroughSeq uint64 `json:"source_through_seq"`
	Hook             string `json:"hook"`
	LogicalRequestID string `json:"logical_request_id"`
}

func validateCheckpointContract(e *Event) error {
	if p := e.ContextEdit; p != nil {
		if p.Version != 1 {
			return fmt.Errorf("unsupported context append version %d", p.Version)
		}
		if e.Type != EventTypeContext || p.SourceStamp == "" || p.SourceThroughID == "" || p.SourceThroughSeq == 0 || p.Hook == "" || p.LogicalRequestID == "" {
			return fmt.Errorf("invalid context append provenance")
		}
	}
	// Open metadata on other events does not establish a checkpoint contract.
	if e.Type != EventTypeCompact {
		return nil
	}
	if e.Meta == nil {
		return nil
	}
	if _, present := e.Meta[CheckpointMetadataKey]; !present {
		return nil
	}
	d, ok := CheckpointDataFromEvent(e)
	if !ok {
		return fmt.Errorf("invalid checkpoint contract")
	}
	switch d.ContractVersion {
	case 0, 1:
		if d.Purpose != "" && d.Purpose != CheckpointSummary || d.ReplacementMessages != nil {
			return fmt.Errorf("legacy summary cannot contain replacement messages")
		}
	case 2:
		if e.Type != EventTypeCompact || d.Purpose != CheckpointReplacement || d.SourceStamp == "" || d.SummarizedThroughID == "" || d.SummarizedThroughSeq == 0 || d.SourceEventCount <= 0 || d.ReplacementMessages == nil {
			return fmt.Errorf("invalid context replacement checkpoint")
		}
		if err := ValidateContextMessages(d.ReplacementMessages); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported checkpoint contract version %d", d.ContractVersion)
	}
	return nil
}

// ValidateContextMessages requires complete contiguous tool groups with exact
// call/result names. It never repairs, drops or invents a missing result.
func ValidateContextMessages(messages []model.Message) error {
	pending := map[string]string{}
	for _, m := range messages {
		calls, results := m.ToolCalls(), m.ToolResults()
		if len(calls) > 0 {
			if m.Role != model.RoleAssistant || len(results) > 0 || len(pending) > 0 {
				return fmt.Errorf("replacement splits tool groups")
			}
			for _, c := range calls {
				if c.ID == "" || c.Name == "" || pending[c.ID] != "" {
					return fmt.Errorf("invalid replacement call identity")
				}
				pending[c.ID] = c.Name
			}
		} else if len(results) > 0 {
			if m.Role != model.RoleTool {
				return fmt.Errorf("replacement result requires tool role")
			}
			for _, r := range results {
				if pending[r.ToolUseID] == "" || pending[r.ToolUseID] != r.Name {
					return fmt.Errorf("replacement result has no matching call")
				}
				delete(pending, r.ToolUseID)
			}
		} else if len(pending) > 0 {
			return fmt.Errorf("replacement splits tool groups")
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("replacement omits tool results")
	}
	return nil
}
