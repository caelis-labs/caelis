// Package controlplane owns Caelis product orchestration around neutral Agent
// SDK controller and participant execution contracts.
package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ContextRouter applies Caelis shared-ledger routing policy to controller and
// participant endpoints.
type ContextRouter struct {
	sessions session.Service
}

// NewContextRouter returns the product context router for one session store.
func NewContextRouter(sessions session.Service) (*ContextRouter, error) {
	if sessions == nil {
		return nil, fmt.Errorf("controlplane: sessions service is required")
	}
	return &ContextRouter{sessions: sessions}, nil
}

// ControllerContext builds the incremental canonical public dialogue routed to
// one controller activation or turn.
func (r *ContextRouter) ControllerContext(ctx context.Context, req controller.ControllerContextRequest) (controller.ContextRoute, error) {
	if r == nil || r.sessions == nil {
		return controller.ContextRoute{}, fmt.Errorf("controlplane: context router is unavailable")
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: session.NormalizeSessionRef(req.SessionRef)})
	if err != nil {
		return controller.ContextRoute{}, err
	}
	shared := sharedDialogueDeltaFromEvents(events, req.SinceSeq, req.ExcludeTurnID)
	activeSession := session.CloneSession(req.Session)
	from := session.CloneControllerBinding(req.Controller)
	var b strings.Builder
	b.WriteString("Caelis controller handoff context. Continue the existing Caelis session; do not treat this as a fresh conversation.\n")
	b.WriteString("session_id: ")
	b.WriteString(strings.TrimSpace(activeSession.SessionID))
	b.WriteString("\nworkspace: ")
	b.WriteString(strings.TrimSpace(activeSession.CWD))
	b.WriteString("\nprevious_controller: ")
	b.WriteString(firstNonEmpty(strings.TrimSpace(from.AgentName), strings.TrimSpace(from.Label), strings.TrimSpace(from.ControllerID), string(from.Kind)))
	b.WriteString("\ncontext_sync_seq: ")
	fmt.Fprintf(&b, "%d", shared.Checkpoint)
	if len(activeSession.Participants) > 0 {
		b.WriteString("\nchild_handles:")
		for _, participant := range activeSession.Participants {
			if participant.Kind != session.ParticipantKindSubagent || participant.Role != session.ParticipantRoleDelegated {
				continue
			}
			handle := strings.TrimSpace(participant.Label)
			if handle == "" {
				continue
			}
			b.WriteString("\n- ")
			b.WriteString(handle)
			if agentName := strings.TrimSpace(participant.AgentName); agentName != "" {
				b.WriteString(" agent=")
				b.WriteString(agentName)
			}
		}
	}
	appendSharedDialogueDelta(&b, shared)
	return controller.ContextRoute{Prelude: b.String(), SyncSeq: shared.Checkpoint}, nil
}

// ParticipantContext builds canonical public dialogue background for one
// bounded participant request.
func (r *ContextRouter) ParticipantContext(ctx context.Context, req controller.ParticipantContextRequest) (controller.ContextRoute, error) {
	if r == nil || r.sessions == nil {
		return controller.ContextRoute{}, fmt.Errorf("controlplane: context router is unavailable")
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: session.NormalizeSessionRef(req.SessionRef)})
	if err != nil {
		return controller.ContextRoute{}, err
	}
	binding := session.CloneParticipantBinding(req.Binding)
	shared := sharedDialogueDeltaFromEvents(events, binding.ContextSyncSeq, "")
	activeSession := session.CloneSession(req.Session)
	var b strings.Builder
	b.WriteString("Caelis shared public dialogue context. Use this as background for the current side-agent request; do not treat it as a fresh session.\n")
	if sessionID := strings.TrimSpace(activeSession.SessionID); sessionID != "" {
		b.WriteString("session_id: ")
		b.WriteString(sessionID)
		b.WriteString("\n")
	}
	if cwd := strings.TrimSpace(activeSession.CWD); cwd != "" {
		b.WriteString("workspace: ")
		b.WriteString(cwd)
		b.WriteString("\n")
	}
	if target := firstNonEmpty(strings.TrimSpace(binding.Label), strings.TrimSpace(binding.AgentName), strings.TrimSpace(binding.ID)); target != "" {
		b.WriteString("target_agent: ")
		b.WriteString(target)
		b.WriteString("\n")
	}
	appendSharedDialogueDelta(&b, shared)
	return controller.ContextRoute{Prelude: strings.TrimSpace(b.String()), SyncSeq: shared.Checkpoint}, nil
}

// Checkpoint returns the latest canonical public-dialogue sequence routed by
// this policy, optionally excluding one in-flight turn.
func (r *ContextRouter) Checkpoint(ctx context.Context, ref session.SessionRef, excludeTurnID string) (int, error) {
	if r == nil || r.sessions == nil {
		return 0, fmt.Errorf("controlplane: context router is unavailable")
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: session.NormalizeSessionRef(ref)})
	if err != nil {
		return 0, err
	}
	return sharedDialogueCheckpoint(events, excludeTurnID), nil
}

type sharedDialogueDelta struct {
	Checkpoint int
	Entries    []sharedDialogueEntry
}

type sharedDialogueEntry struct {
	Seq  int
	Role string
	Text string
}

func sharedDialogueDeltaFromEvents(events []*session.Event, sinceSeq int, excludeTurnID string) sharedDialogueDelta {
	if sinceSeq < 0 {
		sinceSeq = 0
	}
	excludeTurnID = strings.TrimSpace(excludeTurnID)
	latestCompactSeq := latestCompactEventSeq(events)
	startAfter := sinceSeq
	if latestCompactSeq > 0 && sinceSeq < latestCompactSeq {
		startAfter = latestCompactSeq - 1
	}
	out := sharedDialogueDelta{Checkpoint: sharedDialogueCheckpoint(events, excludeTurnID)}
	for i, event := range events {
		seq := eventSequence(event, i)
		if seq <= startAfter || latestCompactSeq > 0 && seq < latestCompactSeq {
			continue
		}
		if excludeTurnID != "" && event != nil && event.Scope != nil && strings.TrimSpace(event.Scope.TurnID) == excludeTurnID {
			continue
		}
		if !isSharedDialogueDeltaEvent(event, latestCompactSeq, seq) {
			continue
		}
		text := strings.TrimSpace(session.EventText(event))
		if text == "" {
			continue
		}
		out.Entries = append(out.Entries, sharedDialogueEntry{Seq: seq, Role: sharedDialogueRole(event), Text: text})
	}
	return out
}

func appendSharedDialogueDelta(b *strings.Builder, delta sharedDialogueDelta) {
	if b == nil {
		return
	}
	if delta.Checkpoint > 0 {
		b.WriteString("\nshared_ledger_checkpoint: ")
		fmt.Fprintf(b, "%d", delta.Checkpoint)
	}
	b.WriteString("\nshared_dialogue_delta:")
	if len(delta.Entries) == 0 {
		b.WriteString("\n(none)")
		return
	}
	for _, entry := range delta.Entries {
		b.WriteString("\n[")
		fmt.Fprintf(b, "%d", entry.Seq)
		b.WriteString("] ")
		b.WriteString(entry.Role)
		b.WriteString(":\n")
		b.WriteString(entry.Text)
	}
}

func eventSequence(event *session.Event, index int) int {
	if event != nil && event.Seq > 0 {
		return int(event.Seq)
	}
	return index + 1
}

func latestCompactEventSeq(events []*session.Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if compact.IsCompactEvent(events[i]) {
			return eventSequence(events[i], i)
		}
	}
	return 0
}

func sharedDialogueCheckpoint(events []*session.Event, excludeTurnID string) int {
	excludeTurnID = strings.TrimSpace(excludeTurnID)
	checkpoint := 0
	latestCompactSeq := latestCompactEventSeq(events)
	for i, event := range events {
		seq := eventSequence(event, i)
		if latestCompactSeq > 0 && seq < latestCompactSeq {
			continue
		}
		if excludeTurnID != "" && event != nil && event.Scope != nil && strings.TrimSpace(event.Scope.TurnID) == excludeTurnID {
			continue
		}
		if isSharedDialogueDeltaEvent(event, latestCompactSeq, seq) && seq > checkpoint {
			checkpoint = seq
		}
	}
	return checkpoint
}

func isSharedDialogueDeltaEvent(event *session.Event, latestCompactSeq int, seq int) bool {
	if event == nil || !session.IsCanonicalHistoryEvent(event) {
		return false
	}
	if latestCompactSeq > 0 && seq == latestCompactSeq && compact.IsCompactEvent(event) {
		return true
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser, session.EventTypeAssistant:
		return true
	default:
		return false
	}
}

func sharedDialogueRole(event *session.Event) string {
	if event == nil {
		return ""
	}
	if compact.IsCompactEvent(event) {
		return "compact"
	}
	role := strings.TrimSpace(string(session.EventTypeOf(event)))
	actor := strings.TrimSpace(event.Actor.Name)
	if actor == "" {
		actor = strings.TrimSpace(event.Actor.ID)
	}
	if actor == "" || strings.EqualFold(actor, role) {
		return role
	}
	return role + "(" + actor + ")"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
