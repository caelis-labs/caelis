package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Active-turn recovery keeps exact authorization and the current request. Tool
// exchanges become evidence references, never a model-authored authorization.
// The SDK persists this checkpoint and its coverage so replay uses the same input.
type guardianTurnCompactor struct {
	evidence *guardianEvidenceStore
	output   *model.OutputSpec
	queries  *guardianQueries
}

func (guardianTurnCompactor) Prepare(_ context.Context, req compact.Request) (compact.Result, error) {
	return compact.Result{PromptEvents: session.CloneEvents(req.Events)}, nil
}

func (c guardianTurnCompactor) CompactOnOverflow(ctx context.Context, req compact.Request, cause error) (compact.Result, error) {
	if c.evidence == nil {
		return compact.Result{}, cause
	}
	return c.Force(ctx, req, "context_overflow")
}

func (c guardianTurnCompactor) Force(ctx context.Context, req compact.Request, trigger string) (compact.Result, error) {
	if err := ctx.Err(); err != nil {
		return compact.Result{}, err
	}
	if c.evidence == nil {
		return compact.Result{}, fmt.Errorf("guardian evidence recovery unavailable")
	}
	if c.queries != nil {
		c.queries.mu.Lock()
		c.queries.recoveries++
		c.queries.mu.Unlock()
	}
	events := compact.PromptEventsFromLatestCompact(req.Events)
	lastInput := -1
	for i, e := range events {
		if session.EventTypeOf(e) == session.EventTypeUser && !guardianIsUser(e) && e.Meta[guardianSourceProjection] != true {
			lastInput = i
		}
	}
	var retained []*session.Event
	for i, e := range events {
		if guardianIsUser(e) || i == lastInput || compact.IsCompactEvent(e) {
			retained = append(retained, session.CloneEvent(e))
			continue
		}
		message, ok := session.ModelMessageOf(e)
		if !ok {
			continue
		}
		raw, err := json.Marshal(message)
		if err != nil {
			return compact.Result{}, err
		}
		ref, err := c.evidence.save(raw)
		if err != nil {
			return compact.Result{}, err
		}
		one := guardianEvidenceEvent(fmt.Sprintf("Earlier evidence (not authorization): ReadEvidence ref=%s; type=%s", ref, session.EventTypeOf(e)))
		one.Meta[guardianTurnKey] = fmt.Sprintf("recovered:%d", i)
		retained = append(retained, one)
	}
	// Pending exact input is budgeted but appended by Runtime, not duplicated in
	// the checkpoint. Completed effects above remain retrievable after retry.
	pending := strings.Builder{}
	for _, e := range req.PendingEvents {
		pending.WriteString(session.EventText(e))
		pending.WriteByte('\n')
	}
	retained, fits := guardianFitHistory(retained, req.Model, pending.String(), c.output, "")
	if !fits {
		return compact.Result{}, fmt.Errorf("guardian mandatory input exceeds model capacity")
	}
	var text strings.Builder
	text.WriteString("Guardian context checkpoint. Earlier evidence is referenced, not re-executed.\n")
	for _, e := range retained {
		text.WriteString(session.EventText(e))
		text.WriteByte('\n')
	}
	if len(req.Events) == 0 {
		return compact.Result{}, fmt.Errorf("guardian has no history to recover")
	}
	last := req.Events[len(req.Events)-1]
	data := compact.CompactEventData{Revision: 1, ContractVersion: compact.CompactContractVersion, SummarizedThroughID: last.ID, SummarizedThroughSeq: last.Seq, Generator: "guardian", Trigger: trigger, SourceEventCount: len(req.Events)}
	event := &session.Event{Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "guardian"}, Text: text.String(), Meta: map[string]any{compact.MetaKeyCompact: compact.CompactEventDataValue(data)}}
	for _, e := range retained {
		if guardianIsUser(e) {
			// A checkpoint containing authorization must outlive completed-turn
			// eviction. Keep its original recoverable if physical pressure later
			// requires folding this protected checkpoint itself.
			ref, err := c.evidence.save([]byte(event.Text))
			if err != nil {
				return compact.Result{}, err
			}
			event.Meta[guardianUserSource] = "checkpoint"
			event.Meta[guardianCheckpointEvidence] = ref
			break
		}
	}
	return compact.Result{Compacted: true, CompactText: event.Text, CompactEvent: event, PromptEvents: compact.PromptEventsFromLatestCompact([]*session.Event{event})}, nil
}
