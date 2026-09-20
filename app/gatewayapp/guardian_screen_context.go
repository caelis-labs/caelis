package gatewayapp

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const (
	guardianScreenCallID = "guardian_screen_call_id"
	guardianScreenAction = "guardian_screen_action"
	guardianScreenResult = "guardian_screen_result"
)

// The key is only a retrieval hint, never an authorization equivalence. The
// classifier still receives the complete current action and original evidence.
func guardianScreenActionKey(name string, input map[string]any) string {
	if name == "" || len(input) == 0 || !guardianActionWithinStructuralLimits(input) {
		return ""
	}
	values := make(map[string]any, len(input))
	for key, value := range input {
		if name == "RunCommand" && (key == "sandbox_permissions" || key == "justification" || key == "yield_time_ms") {
			continue
		}
		values[key] = value
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s:%x", name, sha256.Sum256(raw))
}

// Screening keeps every retained user instruction verbatim, plus one completed
// observation pair: the latest exact-action match, or the latest completed tool
// when there is no match. It does not share the Agent's growing observation
// window. Source sequence numbers preserve the order of retained evidence.
func guardianScreenEvidence(req kernel.ApprovalReviewRequest, events []*session.Event) map[int]bool {
	name, input := req.RuntimeRequest.Call.Name, rawJSONMap(req.RuntimeRequest.Call.Input)
	if req.Approval != nil {
		name = firstNonEmpty(req.Approval.ToolName, name)
		if len(req.Approval.RawInput) > 0 {
			input = req.Approval.RawInput
		}
	}
	key := guardianScreenActionKey(name, input)
	selected := make(map[int]bool)
	calls := make(map[string]int)
	latestCall, latestResult, matchedCall, matchedResult := -1, -1, -1, -1
	for index, event := range events {
		if event == nil {
			continue
		}
		if guardianIsUser(event) {
			selected[index] = true
			continue
		}
		id, _ := event.Meta[guardianScreenCallID].(string)
		if id == "" {
			continue
		}
		if event.Meta[guardianScreenResult] != true {
			calls[id] = index
			continue
		}
		call, ok := calls[id]
		if !ok {
			continue // Do not present a result detached from its operation.
		}
		latestCall, latestResult = call, index
		if key != "" && events[call].Meta[guardianScreenAction] == key {
			matchedCall, matchedResult = call, index
		}
	}
	if matchedResult >= 0 {
		latestCall, latestResult = matchedCall, matchedResult
	}
	if latestResult >= 0 {
		selected[latestCall], selected[latestResult] = true, true
	}
	return selected
}

type guardianScreenError struct {
	reason string
	cause  error
}

func (e *guardianScreenError) Error() string { return e.reason + ": " + e.cause.Error() }
func (e *guardianScreenError) Unwrap() error { return e.cause }

// Reasons are fixed labels. Provider errors may contain sensitive response data.
func guardianScreenReason(err error) string {
	var failure *guardianScreenError
	if errors.As(err, &failure) {
		return failure.reason
	}
	return "evidence_unavailable"
}
