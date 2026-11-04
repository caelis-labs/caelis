package session

import "github.com/caelis-labs/caelis/agent-sdk/model"

// NewModelAttemptReset is the shared transient retry projection. It carries
// only attempt counts and timing, never provider diagnostics or request text.
func NewModelAttemptReset(reset model.AttemptReset) *Event {
	meta := map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"attempt_reset": map[string]any{},
			},
		},
	}
	resetMeta, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := resetMeta["runtime"].(map[string]any)
	attemptMeta, _ := runtimeMeta["attempt_reset"].(map[string]any)
	if reset.Attempt > 0 {
		attemptMeta["attempt"] = reset.Attempt
	}
	if reset.MaxRetries > 0 {
		attemptMeta["max_retries"] = reset.MaxRetries
	}
	if reset.RetryDelayMillis > 0 {
		attemptMeta["retry_delay_ms"] = reset.RetryDelayMillis
	}
	attemptMeta["retrying"] = reset.Retrying
	return MarkUIOnly(&Event{
		Type: EventTypeLifecycle,
		Text: "model attempt reset",
		Lifecycle: &EventLifecycle{
			Status: "attempt_reset",
			Reason: "model_retry",
		},
		Meta: meta,
	})
}
