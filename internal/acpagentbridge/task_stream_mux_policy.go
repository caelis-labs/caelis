package acpagentbridge

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const (
	acpTaskStreamResolveMaxAttempts = 4
	acpTaskStreamResolveRetryDelay  = 25 * time.Millisecond
	acpTaskStreamResumeMaxAttempts  = 4
)

func retryACPTaskStream[T any](
	ctx context.Context,
	maxAttempts int,
	canRetry func() bool,
	operation func() (T, error),
) (T, int, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, attempt - 1, err
		}
		if canRetry != nil && !canRetry() {
			if lastErr != nil {
				return zero, attempt - 1, lastErr
			}
			return zero, attempt - 1, context.Canceled
		}
		value, err := operation()
		if err == nil {
			return value, attempt, nil
		}
		lastErr = err
		if !acpTaskStreamResolveRetryable(err) || attempt == maxAttempts {
			return zero, attempt, err
		}
		timer := time.NewTimer(acpTaskStreamResolveRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return zero, attempt, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, maxAttempts, lastErr
}

type acpTaskStreamNoticeKind uint8

const (
	acpTaskStreamNoticeAttachFailed acpTaskStreamNoticeKind = 1 << iota
	acpTaskStreamNoticeInterrupted
)

type acpTaskStreamNoticeFacts struct {
	kind            acpTaskStreamNoticeKind
	anchor          acpTaskStreamAnchor
	err             error
	retryExhausted  bool
	hasCursor       bool
	resumeExhausted bool
}

func buildACPTaskStreamNotice(sessionID string, facts acpTaskStreamNoticeFacts) eventstream.Envelope {
	toolName := shell.RunCommandToolName
	if facts.anchor.kind == task.KindSubagent {
		toolName = spawn.ToolName
	}
	code := errorcode.CodeOf(facts.err)
	meta := map[string]any{
		"target_handle": facts.anchor.handle,
		"unavailable":   true,
		"error_code":    string(code),
	}
	var notice string
	switch facts.kind {
	case acpTaskStreamNoticeInterrupted:
		notice = fmt.Sprintf(
			"%s live output for Task %s was interrupted: %s; previously delivered output remains valid and the final Task result remains authoritative",
			toolName,
			facts.anchor.handle,
			activeTaskStreamFailureReason(code, facts.hasCursor, facts.resumeExhausted),
		)
		meta["active_stream_interrupted"] = true
		meta["resume_cursor_available"] = facts.hasCursor
		meta["resume_exhausted"] = facts.resumeExhausted
	default:
		notice = fmt.Sprintf(
			"%s live output is unavailable for Task %s: %s; the final Task result remains available",
			toolName,
			facts.anchor.handle,
			attachTaskStreamFailureReason(code),
		)
		meta["retry_exhausted"] = facts.retryExhausted
	}
	return eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: sessionID,
		Scope:     eventstream.ScopeMain,
		ScopeID:   sessionID,
		Notice:    notice,
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Meta:      map[string]any{"task_stream": meta},
	}
}

func attachTaskStreamFailureReason(code errorcode.Code) string {
	switch code {
	case errorcode.NotFound, errorcode.Unavailable:
		return "the Task stream did not become available during the recovery window"
	case errorcode.PermissionDenied, errorcode.Unauthenticated:
		return "access to the Task live output was denied"
	case errorcode.Conflict:
		return "the Task stream identity was ambiguous or conflicted"
	case errorcode.InvalidArgument, errorcode.FailedPrecondition:
		return "the Task stream request was invalid for this Task"
	default:
		return "the Task stream could not be attached"
	}
}

func activeTaskStreamFailureReason(code errorcode.Code, hasCursor bool, resumeExhausted bool) string {
	switch {
	case !hasCursor:
		return "the active Task stream ended before a safe resume cursor was available"
	case resumeExhausted:
		return "the active Task stream could not be resumed after bounded retries"
	case code == errorcode.PermissionDenied || code == errorcode.Unauthenticated:
		return "access to resume the active Task stream was denied"
	case code == errorcode.Conflict:
		return "the active Task stream resume conflicted with its retained boundary"
	case code == errorcode.InvalidArgument || code == errorcode.FailedPrecondition:
		return "the retained Task stream boundary could not be resumed"
	default:
		return "the active Task stream ended unexpectedly"
	}
}

func acpTaskStreamResolveRetryable(err error) bool {
	// Only discovery delay and transport/storage unavailability are retryable.
	// A cursor-bearing retry remains exact-only; Control never splices a
	// replacement after an already visible exact prefix.
	return errorcode.Is(err, errorcode.NotFound) ||
		errorcode.Is(err, errorcode.Unavailable)
}
