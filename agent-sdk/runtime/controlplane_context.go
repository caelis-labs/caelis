package runtime

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) HandoffController(ctx context.Context, req agent.HandoffControllerRequest) (session.Session, error) {
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return session.Session{}, err
	}
	from := session.CloneControllerBinding(activeSession.Controller)
	kind := req.Kind
	if kind == "" {
		kind = session.ControllerKindKernel
	}
	var to session.ControllerBinding
	switch kind {
	case session.ControllerKindACP:
		if r.controllers == nil {
			return session.Session{}, fmt.Errorf("agent-sdk/runtime: ACP controller backend is not configured")
		}
		sinceSeq := 0
		if from.Kind == session.ControllerKindACP && sameControllerAgent(from, req.Agent) {
			sinceSeq = from.ContextSyncSeq
		}
		contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, activeSession, ref, from, sinceSeq, "")
		to, err = r.controllers.Activate(ctx, controller.HandoffRequest{
			SessionRef:     ref,
			Session:        activeSession,
			Agent:          strings.TrimSpace(req.Agent),
			Source:         strings.TrimSpace(req.Source),
			Reason:         strings.TrimSpace(req.Reason),
			ContextPrelude: contextPrelude,
			ContextSyncSeq: contextSeq,
		})
		if err != nil {
			return session.Session{}, err
		}
	default:
		if r.controllers != nil && from.Kind == session.ControllerKindACP {
			if err := r.controllers.Deactivate(ctx, ref); err != nil {
				return session.Session{}, err
			}
		}
		to = r.kernelControllerBinding(firstNonEmpty(strings.TrimSpace(req.Source), "handoff"))
	}

	activeSession, err = r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: ref,
		Binding:    to,
	})
	if err != nil {
		return session.Session{}, err
	}
	if _, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      handoffEvent(from, to, strings.TrimSpace(req.Reason), r.now()),
	}); err != nil {
		return session.Session{}, err
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) buildControllerTurnContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	excludeTurnID string,
) (string, int) {
	binding := session.CloneControllerBinding(activeSession.Controller)
	if binding.Kind != session.ControllerKindACP {
		return "", binding.ContextSyncSeq
	}
	contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, activeSession, ref, binding, binding.ContextSyncSeq, excludeTurnID)
	if contextSeq <= binding.ContextSyncSeq {
		return "", binding.ContextSyncSeq
	}
	return contextPrelude, contextSeq
}

func (r *Runtime) buildControllerHandoffContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	from session.ControllerBinding,
	sinceSeq int,
	excludeTurnID string,
) (string, int) {
	shared := r.buildSharedDialogueDeltaExcludingTurn(ctx, ref, sinceSeq, excludeTurnID)
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
			if agent := strings.TrimSpace(participant.AgentName); agent != "" {
				b.WriteString(" agent=")
				b.WriteString(agent)
			}
		}
	}
	appendSharedDialogueDelta(&b, shared)
	return b.String(), shared.Checkpoint
}

func (r *Runtime) buildParticipantPromptContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) string {
	shared := r.buildSharedDialogueDelta(ctx, ref, binding.ContextSyncSeq)
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
	return strings.TrimSpace(b.String())
}

func (r *Runtime) updateControllerContextCheckpoint(ctx context.Context, ref session.SessionRef) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding := session.CloneControllerBinding(activeSession.Controller)
	binding.ContextSyncSeq = r.sharedDialogueCheckpoint(ctx, ref)
	_, err = r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	return err
}

func (r *Runtime) updateParticipantContextCheckpoint(ctx context.Context, ref session.SessionRef, participantID string) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return nil
	}
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding, ok := participantBinding(activeSession, participantID)
	if !ok {
		return nil
	}
	binding.ContextSyncSeq = r.sharedDialogueCheckpoint(ctx, ref)
	_, err = r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	return err
}

func (r *Runtime) sharedDialogueCheckpoint(ctx context.Context, ref session.SessionRef) int {
	if r == nil || r.sessions == nil {
		return 0
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return 0
	}
	return sharedDialogueCheckpoint(events)
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

func (r *Runtime) buildSharedDialogueDelta(ctx context.Context, ref session.SessionRef, sinceSeq int) sharedDialogueDelta {
	return r.buildSharedDialogueDeltaExcludingTurn(ctx, ref, sinceSeq, "")
}

func (r *Runtime) buildSharedDialogueDeltaExcludingTurn(ctx context.Context, ref session.SessionRef, sinceSeq int, excludeTurnID string) sharedDialogueDelta {
	if r == nil || r.sessions == nil {
		return sharedDialogueDelta{}
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return sharedDialogueDelta{}
	}
	return sharedDialogueDeltaFromEventsExcludingTurn(events, sinceSeq, excludeTurnID)
}

func sharedDialogueDeltaFromEvents(events []*session.Event, sinceSeq int) sharedDialogueDelta {
	return sharedDialogueDeltaFromEventsExcludingTurn(events, sinceSeq, "")
}

func sharedDialogueDeltaFromEventsExcludingTurn(events []*session.Event, sinceSeq int, excludeTurnID string) sharedDialogueDelta {
	if sinceSeq < 0 {
		sinceSeq = 0
	}
	excludeTurnID = strings.TrimSpace(excludeTurnID)
	latestCompactSeq := latestCompactEventSeq(events)
	startAfter := sinceSeq
	if latestCompactSeq > 0 && sinceSeq < latestCompactSeq {
		startAfter = latestCompactSeq - 1
	}
	out := sharedDialogueDelta{Checkpoint: sharedDialogueCheckpointExcludingTurn(events, excludeTurnID)}
	for i, event := range events {
		seq := i + 1
		if seq <= startAfter {
			continue
		}
		if latestCompactSeq > 0 && seq < latestCompactSeq {
			continue
		}
		if excludeTurnID != "" && event != nil && event.Scope != nil && strings.TrimSpace(event.Scope.TurnID) == excludeTurnID {
			continue
		}
		if !isSharedDialogueDeltaEvent(event, latestCompactSeq, seq) {
			continue
		}
		text := sharedDialogueText(event)
		if text == "" {
			continue
		}
		out.Entries = append(out.Entries, sharedDialogueEntry{
			Seq:  seq,
			Role: sharedDialogueRole(event),
			Text: text,
		})
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

func isSharedDialogueDeltaEvent(event *session.Event, latestCompactSeq int, seq int) bool {
	if event == nil || !session.IsCanonicalHistoryEvent(event) {
		return false
	}
	if latestCompactSeq > 0 && seq == latestCompactSeq && compact.IsCompactEvent(event) {
		return true
	}
	return isSharedDialogueEvent(event)
}

func latestCompactEventSeq(events []*session.Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if compact.IsCompactEvent(events[i]) {
			return i + 1
		}
	}
	return 0
}

func sharedDialogueCheckpoint(events []*session.Event) int {
	return sharedDialogueCheckpointExcludingTurn(events, "")
}

func sharedDialogueCheckpointExcludingTurn(events []*session.Event, excludeTurnID string) int {
	excludeTurnID = strings.TrimSpace(excludeTurnID)
	checkpoint := 0
	latestCompactSeq := latestCompactEventSeq(events)
	for i, event := range events {
		seq := i + 1
		if latestCompactSeq > 0 && seq < latestCompactSeq {
			continue
		}
		if excludeTurnID != "" && event != nil && event.Scope != nil && strings.TrimSpace(event.Scope.TurnID) == excludeTurnID {
			continue
		}
		if isSharedDialogueDeltaEvent(event, latestCompactSeq, seq) {
			checkpoint = seq
		}
	}
	return checkpoint
}

func isSharedDialogueEvent(event *session.Event) bool {
	if event == nil || !session.IsCanonicalHistoryEvent(event) {
		return false
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

func sharedDialogueText(event *session.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(session.EventText(event))
}

func sameControllerAgent(binding session.ControllerBinding, agent string) bool {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return false
	}
	for _, candidate := range []string{binding.AgentName, binding.Label, binding.ControllerID} {
		if strings.EqualFold(strings.TrimSpace(candidate), agent) {
			return true
		}
	}
	return false
}
