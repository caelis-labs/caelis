package runtime

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type compactionRecoveryKind string

const (
	compactionRecoveryKindWatermark      compactionRecoveryKind = "watermark"
	compactionRecoveryKindRetryExhausted compactionRecoveryKind = "retry_exhausted"
	compactionRecoveryKindOverflow       compactionRecoveryKind = "overflow"
)

type compactionRecovery struct {
	kind     compactionRecoveryKind
	decision autoCompactDecision
	cause    error
}

type compactionFailureError struct {
	phase string
	cause error
}

func (e *compactionFailureError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *compactionFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func wrapCompactionFailure(phase string, err error) error {
	if err == nil {
		return nil
	}
	return &compactionFailureError{
		phase: strings.TrimSpace(phase),
		cause: err,
	}
}

func compactionRecoveryFromError(err error) (compactionRecovery, bool) {
	if decision, ok := autoCompactRequired(err); ok {
		kind := decision.Kind
		if kind == "" {
			kind = compactionRecoveryKindWatermark
		}
		return compactionRecovery{kind: kind, decision: decision, cause: err}, true
	}
	if isCompactionOverflowError(err) {
		return compactionRecovery{kind: compactionRecoveryKindOverflow, cause: err}, true
	}
	return compactionRecovery{}, false
}

func (r *Runtime) recoverByCompacting(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	req agent.RunRequest,
	recovery compactionRecovery,
	sink *runner,
) (compactionProgress, bool, error) {
	switch recovery.kind {
	case compactionRecoveryKindWatermark, compactionRecoveryKindRetryExhausted:
		return r.compactAfterModelRequestWatermark(ctx, activeSession, ref, turnID, recovery.decision, sink)
	case compactionRecoveryKindOverflow:
		return r.compactAfterOverflow(ctx, activeSession, ref, turnID, req, recovery.cause, sink)
	default:
		return compactionProgress{}, false, fmt.Errorf("agent-sdk/runtime: unknown compaction recovery kind %q", recovery.kind)
	}
}

func (r compactionRecovery) noProgressError(cause error) error {
	switch r.kind {
	case compactionRecoveryKindWatermark:
		return fmt.Errorf("agent-sdk/runtime: model-request watermark recovery made no durable compact progress: %w", cause)
	case compactionRecoveryKindRetryExhausted:
		return fmt.Errorf("agent-sdk/runtime: high-water retry exhaustion recovery made no durable compact progress: %w", cause)
	case compactionRecoveryKindOverflow:
		return fmt.Errorf("agent-sdk/runtime: context overflow recovery made no durable compact progress: %w", cause)
	default:
		return fmt.Errorf("agent-sdk/runtime: compaction recovery %q made no durable compact progress: %w", r.kind, cause)
	}
}
