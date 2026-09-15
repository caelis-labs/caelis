package runtime

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

// The command owner versions execution data inside the existing Task Spec.
// Legacy records remain observable but cannot reconstruct an approval worker.
type commandExecutionSpec struct {
	Version     int                 `json:"version"`
	Timeout     time.Duration       `json:"timeout_ns"`
	Constraints sandbox.Constraints `json:"constraints"`
	Approval    *commandApprovalRef `json:"approval,omitempty"`
}

type commandApprovalRef struct {
	RunID    string    `json:"run_id"`
	TurnID   string    `json:"turn_id"`
	TokenID  string    `json:"token_id"`
	Deadline time.Time `json:"deadline,omitempty"`
}

func commandExecutionForRequest(req taskapi.CommandStartRequest) commandExecutionSpec {
	constraints, _ := req.Constraints.(sandbox.Constraints)
	return commandExecutionSpec{Version: 1, Timeout: req.Timeout, Constraints: constraints}
}

func (s commandExecutionSpec) value() map[string]any {
	if s.Version == 0 {
		return nil
	}
	raw, _ := json.Marshal(s)
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return value
}

func commandExecutionFromEntry(entry *taskapi.Entry) (commandExecutionSpec, error) {
	var spec commandExecutionSpec
	value := entry.Spec["execution"]
	if value == nil {
		return spec, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, err
	}
	if spec.Version != 1 {
		return spec, fmt.Errorf("unsupported command execution spec version %d", spec.Version)
	}
	if spec.Approval != nil && (spec.Approval.TokenID == "" || spec.Approval.RunID == "" || spec.Approval.TurnID == "") {
		return spec, fmt.Errorf("incomplete command approval association")
	}
	return spec, nil
}
