package kernel

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
	if event == nil {
		return nil
	}
	if err := session.ValidateDurableCoreEvent(event); err != nil {
		return replayValidationError(event.ID, session.EventValidationDetail(err))
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
