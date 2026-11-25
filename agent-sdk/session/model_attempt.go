package session

import "github.com/caelis-labs/caelis/agent-sdk/model"

// LifecycleStatusModelAttemptReset marks a transient restart of model generation.
const LifecycleStatusModelAttemptReset = "attempt_reset"

// NewModelAttemptReset reports a model retry without a specific narrative owner.
func NewModelAttemptReset(reset model.AttemptReset) *Event {
	return NewModelAttemptResetForMessage(reset, "")
}

// NewModelAttemptResetForMessage retires the live output for messageID and
// reports the retry count and timing.
func NewModelAttemptResetForMessage(reset model.AttemptReset, messageID string) *Event {
	attemptMeta := map[string]any{"retrying": reset.Retrying}
	if reset.Attempt > 0 {
		attemptMeta["attempt"] = reset.Attempt
	}
	if reset.MaxRetries > 0 {
		attemptMeta["max_retries"] = reset.MaxRetries
	}
	if reset.RetryDelayMillis > 0 {
		attemptMeta["retry_delay_ms"] = reset.RetryDelayMillis
	}
	if messageID != "" {
		attemptMeta["message_id"] = messageID
	}
	return MarkUIOnly(&Event{
		Type: EventTypeLifecycle,
		Text: "model attempt reset",
		Lifecycle: &EventLifecycle{
			Status: LifecycleStatusModelAttemptReset,
			Reason: "model_retry",
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{"attempt_reset": attemptMeta},
			},
		},
	})
}

// ModelAttemptResetMessageID reads the opaque narrative owner from reset metadata.
func ModelAttemptResetMessageID(meta map[string]any) string {
	caelis, _ := meta["caelis"].(map[string]any)
	runtime, _ := caelis["runtime"].(map[string]any)
	attempt, _ := runtime["attempt_reset"].(map[string]any)
	messageID, _ := attempt["message_id"].(string)
	return messageID
}
