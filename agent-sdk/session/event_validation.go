package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// EventValidationError reports a canonical session event that cannot be used to
// rebuild model context safely.
type EventValidationError struct {
	Detail string
}

func (e *EventValidationError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		return ErrInvalidEvent.Error()
	}
	return ErrInvalidEvent.Error() + ": " + detail
}

func (e *EventValidationError) Unwrap() error {
	return ErrInvalidEvent
}

func (e *EventValidationError) ErrorCode() errorcode.Code { return errorcode.InvalidArgument }

// EventValidationDetail returns the precise validation detail carried by err.
func EventValidationDetail(err error) string {
	var eventErr *EventValidationError
	if errors.As(err, &eventErr) {
		return strings.TrimSpace(eventErr.Detail)
	}
	return strings.TrimSpace(err.Error())
}

// ValidateDurableCoreEvent rejects persisted core facts that cannot faithfully
// rebuild model-visible context. Runtime-only control information is allowed to
// remain custom/lifecycle/protocol-shaped when it does not enter prompt history.
func ValidateDurableCoreEvent(event *Event) error {
	if event == nil || IsTransient(event) {
		return nil
	}
	migrated, err := MigrateEvent(*event)
	if err != nil {
		return coreEventValidationError(err.Error())
	}
	event = &migrated
	if err := jsonvalue.Validate(event); err != nil {
		return coreEventValidationError(fmt.Sprintf("event contains invalid JSON-compatible value: %v", err))
	}
	if event.Journal != nil {
		if err := ValidateExecutionJournalEntry(*event.Journal); err != nil {
			return coreEventValidationError(err.Error())
		}
	}
	if event.ChildOrigin != nil {
		if err := ValidateEventChildOrigin(*event.ChildOrigin); err != nil {
			return coreEventValidationError(err.Error())
		}
		if !IsMirror(event) {
			return coreEventValidationError("child origin requires mirror visibility")
		}
	}
	if !IsCanonicalHistoryEvent(event) {
		return nil
	}
	switch EventTypeOf(event) {
	case EventTypeUser, EventTypeAssistant, EventTypeSystem, EventTypeContext:
		if event.Message == nil {
			return coreEventValidationError("model-visible event is missing durable Event.Message")
		}
	case EventTypeToolCall:
		if event.Tool == nil {
			return coreEventValidationError("tool call is missing durable Event.Tool")
		}
		if event.Message != nil && len(event.Message.ToolCalls()) == 0 {
			return coreEventValidationError("tool call Event.Message is missing model tool-call payload")
		}
		return validateDurableCoreMeta(event.Meta)
	case EventTypeToolResult:
		return validateDurableCoreToolResult(event)
	case EventTypeCustom:
		if event.Message != nil {
			return coreEventValidationError("custom model-visible event with Event.Message must use an explicit model-context event type")
		}
	}
	return nil
}

func validateDurableCoreToolResult(event *Event) error {
	if event.Tool != nil {
		if len(event.Tool.Output) > 0 {
			if err := validateDurableCoreRawOutput(event.Tool.Output); err != nil {
				return err
			}
		}
		if err := validateDurableCoreToolMessageOutput(event); err != nil {
			return err
		}
		return validateDurableCoreMeta(event.Meta)
	}
	if event.Message != nil && len(event.Message.ToolResults()) > 0 {
		return validateDurableCoreMeta(event.Meta)
	}
	return coreEventValidationError("tool result is missing durable Event.Tool or model tool-result payload")
}

func validateDurableCoreRawOutput(rawOutput map[string]any) error {
	if _, err := json.Marshal(rawOutput); err != nil {
		return coreEventValidationError(fmt.Sprintf("tool raw_output is not JSON-serializable: %v", err))
	}
	_, info := tool.TruncateMap(rawOutput, tool.DefaultTruncationPolicy())
	if info.Truncated {
		return coreEventValidationError(fmt.Sprintf("tool raw_output is not canonical-truncated (estimated %d tokens > %d tokens)", info.EstimatedTokens, info.MaxTokens))
	}
	return nil
}

func validateDurableCoreToolMessageOutput(event *Event) error {
	if event == nil || event.Tool == nil || len(event.Tool.Output) == 0 || event.Message == nil {
		return nil
	}
	results := event.Message.ToolResults()
	if len(results) == 0 {
		return nil
	}
	toolID := strings.TrimSpace(event.Tool.ID)
	if toolID == "" {
		if len(results) != 1 {
			return coreEventValidationError("durable Event.Tool id is required when tool message carries multiple results")
		}
		if strings.TrimSpace(results[0].ToolUseID) != "" {
			return coreEventValidationError("durable Event.Tool id is required to match tool message result")
		}
	}
	var matched *model.ToolResultPart
	for idx := range results {
		result := results[idx]
		resultID := strings.TrimSpace(result.ToolUseID)
		if toolID == "" && resultID == "" {
			matched = &result
			break
		}
		if toolID != "" && resultID == toolID {
			matched = &result
			break
		}
	}
	if matched == nil {
		return coreEventValidationError("tool message result does not match durable Event.Tool id")
	}
	if event.Tool.Name != "" && strings.TrimSpace(matched.Name) != "" && strings.TrimSpace(matched.Name) != strings.TrimSpace(event.Tool.Name) {
		return coreEventValidationError("tool message result does not match durable Event.Tool name")
	}
	payload, err := toolMessageOutputPayload(*matched)
	if err != nil {
		return coreEventValidationError(err.Error())
	}
	if len(payload) == 0 {
		return coreEventValidationError("tool message result is missing payload")
	}
	if !sameCanonicalJSON(event.Tool.Output, payload) {
		return coreEventValidationError("tool message result diverges from durable Event.Tool output")
	}
	return nil
}

func toolMessageOutputPayload(result model.ToolResultPart) (map[string]any, error) {
	for _, part := range result.Content {
		raw := part.JSONValue()
		if len(raw) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("tool message result is not JSON: %w", err)
		}
		if payload, ok := decoded.(map[string]any); ok {
			return payload, nil
		}
		return map[string]any{"result": decoded}, nil
	}
	texts := make([]string, 0, len(result.Content))
	for _, part := range result.Content {
		if part.Text != nil {
			texts = append(texts, part.Text.Text)
		}
	}
	if len(texts) > 0 {
		return map[string]any{"result": strings.Join(texts, "\n")}, nil
	}
	return nil, nil
}

func sameCanonicalJSON(left any, right any) bool {
	leftRaw, err := canonicalJSONBytes(left)
	if err != nil {
		return false
	}
	rightRaw, err := canonicalJSONBytes(right)
	if err != nil {
		return false
	}
	return string(leftRaw) == string(rightRaw)
}

func canonicalJSONBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func validateDurableCoreMeta(meta map[string]any) error {
	for _, key := range []string{"stdout", "stderr", "result", "error", "exit_code"} {
		if _, exists := meta[key]; exists {
			return coreEventValidationError(fmt.Sprintf("tool output field %q is stored in event meta", key))
		}
	}
	return nil
}

func coreEventValidationError(detail string) error {
	return &EventValidationError{Detail: strings.TrimSpace(detail)}
}
