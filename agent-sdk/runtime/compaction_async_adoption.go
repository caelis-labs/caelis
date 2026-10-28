package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) adoptAsyncCompaction(ctx context.Context, l *autoCompactGatedLLM, job *asyncCompactJob) (bool, error) {
	if ctx.Err() != nil || job.guard != session.RuntimeMutationGuard(ctx) {
		return false, ctx.Err()
	}
	validator, ok := r.sessions.(session.MutationGuardValidator)
	if !ok {
		return false, nil
	}
	if err := validator.ValidateMutationGuard(ctx, l.sessionRef, job.guard); err != nil {
		return false, err
	}

	adopted, err := r.adoptCompactionCandidate(ctx, l, job, lifecycleScopeFromContext(ctx).turnID, nil)
	if err != nil || !adopted {
		return adopted, err
	}
	if err := validator.ValidateMutationGuard(ctx, l.sessionRef, job.guard); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Runtime) adoptCompactionCandidate(ctx context.Context, l *autoCompactGatedLLM, job *asyncCompactJob, turnID string, appendCheckpoint checkpointAppender) (bool, error) {
	data, ok := compact.CompactEventDataFromEvent(job.result.CompactEvent)
	delta := compactableEvents(mainInvocationEvents(job.source.Events))
	_, baseData, _ := compact.LatestCompactEvent(job.source.Events)
	if !job.result.Compacted || !ok || data.ContractVersion != compact.CompactContractVersion ||
		data.Revision != baseData.Revision+1 || data.SourceEventCount != len(delta) || data.SourceEventCount != job.coverage.SourceEventCount ||
		data.SummarizedThroughSeq != job.coverage.SummarizedThroughSeq || data.SummarizedThroughID != lastEventID(delta) ||
		data.SummarizedThroughID != job.coverage.SummarizedThroughID ||
		strings.TrimSpace(stripRuntimeContinuity(job.result.CompactText)) == "" {
		return false, nil
	}
	// A grant-bound idle writer never queues behind existing foreground work.
	release, err := r.sessionWrites.acquireIf(ctx, l.sessionRef, appendCheckpoint == nil)
	if err != nil {
		return false, err
	}
	defer release()
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: l.sessionRef})
	if err != nil {
		return false, err
	}
	if !job.validSource(loaded) {
		return false, nil
	}
	if a := l.requestAuthority; a != nil {
		if !a.canSpeculate || a.validate == nil {
			return false, nil
		}
		ctx = context.WithValue(ctx, checkpointValidationKey{}, a.validate)
	}
	appendix, err := r.runtimeCompactionAppendix(ctx, l.sessionRef, loaded.State)
	if err != nil {
		return false, err
	}
	text := appendRuntimeContinuity(job.result.CompactText, appendix)
	event := buildCompactEvent(loaded.Session, text, data)
	event.ID, event.IdempotencyKey = job.id, job.id
	current := mainInvocationEvents(loaded.Events)
	projected := compact.PromptEventsFromLatestCompact(append(session.CloneEvents(current), event))
	if estimatePromptEventsTokens(projected) >= estimatePromptEventsTokens(compact.PromptEventsFromLatestCompact(current)) {
		return false, nil
	}
	_, err = r.persistCompactionArtifactsUsing(ctx, &loaded.Session, loaded, turnID,
		compact.Result{Compacted: true, CompactEvent: event}, job.validSource, appendCheckpoint)
	if err != nil {
		var stale *checkpointCatalogStaleError
		if errors.As(err, &stale) {
			// The guard rejected before append. The candidate is already discarded;
			// current tool admission remains authoritative for foreground work.
			return false, nil
		}
		// No adoption, including on an unresolved write outcome. The caller
		// fails this Turn; it must not mint a second checkpoint as a fallback.
		return false, err
	}

	return true, nil
}

func (j *asyncCompactJob) validSource(current session.LoadedSession) bool {
	sourceEvents := mainInvocationEvents(j.source.Events)
	events := mainInvocationEvents(current.Events)
	if len(events) < len(sourceEvents) || !reflect.DeepEqual(j.source.State, current.State) {
		return false
	}
	// State/config/controller changes are conservatively incompatible. Ordinary
	// event append changes only revision, update time and possibly auto-title.
	sourceSession, currentSession := session.CloneSession(j.source.Session), session.CloneSession(current.Session)
	sourceSession.Revision, currentSession.Revision = 0, 0
	sourceSession.UpdatedAt, currentSession.UpdatedAt = time.Time{}, time.Time{}
	sourceSession.Title, currentSession.Title = "", ""
	if !reflect.DeepEqual(sourceSession, currentSession) {
		return false
	}
	prefix, err := requestsource.Fingerprint(events[:len(sourceEvents)])
	if err != nil || prefix != j.coverage.SourcePrefixHash {
		return false
	}
	var last uint64
	for _, event := range events {
		if event.Seq == 0 || event.Seq <= last {
			return false
		}
		last = event.Seq
	}
	tail := events[len(sourceEvents):]
	for _, event := range tail {
		if compact.IsCompactEvent(event) || event.Seq <= j.coverage.SourceThroughSeq {
			return false
		}
	}
	return asyncToolPairsComplete(tail)
}
