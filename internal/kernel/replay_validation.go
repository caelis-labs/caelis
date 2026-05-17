package kernel

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func validateReplaySessionEvents(events []*session.Event) error {
	for _, event := range events {
		if err := validateReplaySessionEvent(event); err != nil {
			return err
		}
	}
	return nil
}

func validateReplaySessionEvent(event *session.Event) error {
	if event == nil || session.EventTypeOf(event) != session.EventTypeToolResult {
		return nil
	}
	if event.Tool != nil {
		if len(event.Tool.Output) > 0 {
			if err := validateCanonicalReplayRawOutput(event.ID, event.Tool.Output); err != nil {
				return err
			}
		}
		return validateCanonicalReplayMeta(event.ID, event.Meta)
	}
	if event.Message != nil && len(event.Message.ToolResults()) > 0 {
		return validateCanonicalReplayMeta(event.ID, event.Meta)
	}
	return replayValidationError(event.ID, "tool result is missing durable Event.Tool or model tool-result payload")
}

func validateCanonicalReplayRawOutput(cursor string, rawOutput map[string]any) error {
	if _, err := json.Marshal(rawOutput); err != nil {
		return replayValidationError(cursor, fmt.Sprintf("tool raw_output is not JSON-serializable: %v", err))
	}
	_, info := tool.TruncateMap(rawOutput, tool.DefaultTruncationPolicy())
	if info.Truncated {
		return replayValidationError(cursor, fmt.Sprintf("tool raw_output is not canonical-truncated (estimated %d tokens > %d tokens)", info.EstimatedTokens, info.MaxTokens))
	}
	return nil
}

func validateCanonicalReplayMeta(cursor string, meta map[string]any) error {
	for _, key := range []string{"stdout", "stderr", "result", "error", "exit_code"} {
		if _, exists := meta[key]; exists {
			return replayValidationError(cursor, fmt.Sprintf("tool output field %q is stored in event meta", key))
		}
	}
	return nil
}

func replayValidationError(cursor string, detail string) error {
	cursor = strings.TrimSpace(cursor)
	if cursor != "" {
		detail = "event " + cursor + ": " + detail
	}
	return &Error{
		Kind:        KindValidation,
		Code:        CodeInvalidRequest,
		UserVisible: true,
		Message:     "gateway: refused to replay non-canonical session",
		Detail:      detail,
	}
}
