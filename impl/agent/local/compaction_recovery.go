package local

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/ports/agent"
	"github.com/caelis-labs/caelis/ports/session"
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
) (bool, error) {
	switch recovery.kind {
	case compactionRecoveryKindWatermark, compactionRecoveryKindRetryExhausted:
		return r.compactAfterModelRequestWatermark(ctx, activeSession, ref, turnID, recovery.decision, sink)
	case compactionRecoveryKindOverflow:
		return r.compactAfterOverflow(ctx, activeSession, ref, turnID, req, recovery.cause, sink)
	default:
		return false, fmt.Errorf("impl/agent/local: unknown compaction recovery kind %q", recovery.kind)
	}
}

func (r compactionRecovery) limitError(limit int, cause error) error {
	switch r.kind {
	case compactionRecoveryKindWatermark:
		return fmt.Errorf("impl/agent/local: model-request watermark persisted after %d compaction recoveries: %w", limit, cause)
	case compactionRecoveryKindRetryExhausted:
		return fmt.Errorf("impl/agent/local: high-water retry exhaustion persisted after %d compaction recoveries: %w", limit, cause)
	case compactionRecoveryKindOverflow:
		return fmt.Errorf("impl/agent/local: context overflow persisted after %d compaction recoveries: %w", limit, cause)
	default:
		return fmt.Errorf("impl/agent/local: compaction recovery %q persisted after %d compaction recoveries: %w", r.kind, limit, cause)
	}
}
