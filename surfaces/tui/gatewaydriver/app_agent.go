package gatewaydriver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	"github.com/OnslaughtSnail/caelis/kernel"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/eventbridge"
)

type appServiceAgentTurnHandle struct {
	services     appservices.Services
	coreRef      coresession.Ref
	participant  coresession.ParticipantBinding
	input        string
	contentParts []coremodel.ContentPart
	source       string
	id           string
	runID        string
	turnID       string
	startedAt    time.Time
	ctx          context.Context
	cancel       context.CancelFunc
	events       chan kernel.EventEnvelope
	done         chan struct{}
	mu           sync.Mutex
	history      []kernel.EventEnvelope
}

func newAppServiceAgentTurnHandle(svc appservices.Services, ref coresession.Ref, participant coresession.ParticipantBinding, input string, parts []coremodel.ContentPart, source string) *appServiceAgentTurnHandle {
	handle := newAppServiceAgentTurnHandleBase(svc, ref, input, parts, source)
	handle.participant = participant
	return handle
}

func newAppServiceAgentTurnHandleBase(svc appservices.Services, ref coresession.Ref, input string, parts []coremodel.ContentPart, source string) *appServiceAgentTurnHandle {
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	idSuffix := fmt.Sprintf("%d", now.UnixNano())
	return &appServiceAgentTurnHandle{
		services:     svc,
		coreRef:      coresession.NormalizeRef(ref),
		input:        strings.TrimSpace(input),
		contentParts: coremodel.CloneContentParts(parts),
		source:       strings.TrimSpace(source),
		id:           "agent-turn-" + idSuffix,
		runID:        "agent-run-" + idSuffix,
		turnID:       "agent-turn-" + idSuffix,
		startedAt:    now,
		ctx:          ctx,
		cancel:       cancel,
		events:       make(chan kernel.EventEnvelope, 32),
		done:         make(chan struct{}),
	}
}

func (h *appServiceAgentTurnHandle) HandleID() string {
	return h.id
}

func (h *appServiceAgentTurnHandle) RunID() string {
	return h.runID
}

func (h *appServiceAgentTurnHandle) TurnID() string {
	return h.turnID
}

func (h *appServiceAgentTurnHandle) SessionRef() portsession.SessionRef {
	return portRefFromCore(h.coreRef)
}

func (h *appServiceAgentTurnHandle) CreatedAt() time.Time {
	return h.startedAt
}

func (h *appServiceAgentTurnHandle) Events() <-chan kernel.EventEnvelope {
	return h.events
}

func (h *appServiceAgentTurnHandle) EventsAfter(cursor string) ([]kernel.EventEnvelope, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cursor = strings.TrimSpace(cursor)
	start := 0
	if cursor != "" {
		for i, env := range h.history {
			if strings.TrimSpace(env.Cursor) == cursor {
				start = i + 1
				break
			}
		}
	}
	out := append([]kernel.EventEnvelope(nil), h.history[start:]...)
	next := cursor
	if len(out) > 0 {
		next = out[len(out)-1].Cursor
	}
	return out, next, nil
}

func (h *appServiceAgentTurnHandle) Submit(context.Context, kernel.SubmitRequest) error {
	return &kernel.Error{
		Kind:    kernel.KindConflict,
		Code:    kernel.CodeNoActiveRun,
		Message: "participant turn does not accept active submissions",
	}
}

func (h *appServiceAgentTurnHandle) Cancel() kernel.CancelResult {
	if h.cancel != nil {
		h.cancel()
	}
	return kernel.CancelResult{Status: kernel.CancelStatusCancelled}
}

func (h *appServiceAgentTurnHandle) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(100 * time.Millisecond):
	}
	return nil
}

func (h *appServiceAgentTurnHandle) run(parent context.Context) {
	defer close(h.events)
	defer close(h.done)
	ctx := h.ctx
	if parent != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(parent)
		defer cancel()
		go func() {
			select {
			case <-h.ctx.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	if event := h.userEvent(); event.Type != "" {
		cursor, err := h.services.Engine().RecordEvents(ctx, h.coreRef, []coresession.Event{event})
		if err != nil {
			h.publishError(err)
			return
		}
		h.publishCore(cursor, event)
	}
	result, err := h.services.Agents().Invoke(ctx, appservices.AgentInvokeRequest{
		AgentID:      h.participant.ID,
		SessionRef:   h.coreRef,
		TurnID:       h.turnID,
		Participant:  h.participant,
		Input:        h.input,
		ContentParts: h.contentParts,
	})
	if err != nil {
		h.publishError(err)
		return
	}
	for _, event := range result.Events {
		h.publishCore("", event)
	}
}

func (h *appServiceAgentTurnHandle) userEvent() coresession.Event {
	if h.input == "" && len(h.contentParts) == 0 {
		return coresession.Event{}
	}
	parts := make([]coremodel.Part, 0, len(h.contentParts))
	for _, part := range h.contentParts {
		if messagePart, ok := contentPartMessagePart(part); ok {
			parts = append(parts, messagePart)
		}
	}
	if len(parts) == 0 && h.input != "" {
		parts = append(parts, coremodel.NewTextPart(h.input))
	}
	if len(parts) == 0 {
		return coresession.Event{}
	}
	scope := &coresession.EventScope{
		TurnID: h.turnID,
		Source: firstNonEmpty(h.source, "tui_agent_prompt"),
	}
	meta := map[string]any{}
	scope.Participant = h.participant
	meta["agent"] = strings.TrimSpace(h.participant.ID)
	meta["handle"] = strings.TrimSpace(h.participant.Label)
	return coresession.Event{
		Type:       coresession.EventUser,
		Visibility: coresession.VisibilityCanonical,
		Time:       h.startedAt,
		Actor:      coresession.ActorRef{Kind: coresession.ActorUser, ID: "user", Name: "user"},
		Scope:      scope,
		Message: &coremodel.Message{
			Role:  coremodel.RoleUser,
			Parts: parts,
		},
		Meta: meta,
	}
}

func contentPartMessagePart(part coremodel.ContentPart) (coremodel.Part, bool) {
	switch part.Type {
	case coremodel.ContentPartText:
		if strings.TrimSpace(part.Text) == "" {
			return coremodel.Part{}, false
		}
		return coremodel.NewTextPart(part.Text), true
	case coremodel.ContentPartImage:
		source := coremodel.MediaSource{Kind: coremodel.MediaInline, Data: part.Data}
		if strings.TrimSpace(part.URI) != "" {
			source = coremodel.MediaSource{Kind: coremodel.MediaURL, URI: strings.TrimSpace(part.URI)}
		} else if strings.TrimSpace(part.Data) == "" && strings.TrimSpace(part.FileName) != "" {
			source = coremodel.MediaSource{Kind: coremodel.MediaLocalRef, LocalRef: strings.TrimSpace(part.FileName)}
		}
		if source.Data == "" && source.URI == "" && source.LocalRef == "" {
			return coremodel.Part{}, false
		}
		return coremodel.Part{Kind: coremodel.PartMedia, Media: &coremodel.MediaPart{
			Modality: coremodel.MediaImage,
			Source:   source,
			MimeType: strings.TrimSpace(part.MimeType),
			Name:     strings.TrimSpace(part.FileName),
		}}, true
	case coremodel.ContentPartFile:
		ref := coremodel.FileRefPart{
			Name:     strings.TrimSpace(part.FileName),
			MimeType: strings.TrimSpace(part.MimeType),
			URI:      strings.TrimSpace(part.URI),
		}
		if ref.URI == "" {
			ref.LocalRef = strings.TrimSpace(part.FileName)
		}
		if ref.Name == "" && ref.URI == "" && ref.LocalRef == "" {
			return coremodel.Part{}, false
		}
		return coremodel.Part{Kind: coremodel.PartFileRef, FileRef: &ref}, true
	default:
		return coremodel.Part{}, false
	}
}

func (h *appServiceAgentTurnHandle) publishCore(cursor coresession.Cursor, event coresession.Event) {
	env, ok := eventbridge.KernelEnvelopeFromCore(coreruntime.EventEnvelope{Cursor: cursor, Event: event})
	if !ok {
		return
	}
	env.Event.HandleID = h.HandleID()
	env.Event.RunID = h.RunID()
	if env.Event.TurnID == "" {
		env.Event.TurnID = h.TurnID()
	}
	h.publish(env)
}

func (h *appServiceAgentTurnHandle) publishError(err error) {
	if err == nil {
		return
	}
	h.publish(kernel.EventEnvelope{Err: &kernel.Error{Kind: kernel.KindInternal, Code: kernel.CodeInternal, Message: err.Error()}})
}

func (h *appServiceAgentTurnHandle) publish(env kernel.EventEnvelope) {
	h.mu.Lock()
	h.history = append(h.history, env)
	h.mu.Unlock()
	select {
	case h.events <- env:
	case <-h.ctx.Done():
	}
}

func (h *appServiceAgentTurnHandle) state() kernel.ActiveTurnState {
	return kernel.ActiveTurnState{
		SessionRef: h.SessionRef(),
		Kind:       kernel.ActiveTurnKindParticipant,
		HandleID:   h.HandleID(),
		RunID:      h.RunID(),
		TurnID:     h.TurnID(),
		StartedAt:  h.CreatedAt(),
	}
}

func (g *appServiceGateway) participantForAttach(ctx context.Context, snapshot coresession.Snapshot, req kernel.AttachParticipantRequest) (coresession.ParticipantBinding, error) {
	descriptor, ok, err := g.resolveAgentDescriptor(ctx, req.Agent)
	if err != nil {
		return coresession.ParticipantBinding{}, err
	}
	if !ok {
		return coresession.ParticipantBinding{}, fmt.Errorf("core app-service TUI gateway: agent %q is not configured", strings.TrimSpace(req.Agent))
	}
	existing := participantsFromCoreSnapshot(snapshot)
	id := allocateCoreParticipantID(existing, descriptor.ID)
	role := coresession.ParticipantRole(req.Role)
	if role == "" {
		role = coresession.ParticipantSidecar
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = firstNonEmpty(descriptor.Name, descriptor.ID)
	}
	return coresession.ParticipantBinding{
		ID:         id,
		Kind:       coresession.ParticipantACP,
		Role:       role,
		AgentName:  strings.TrimSpace(descriptor.ID),
		Label:      label,
		Source:     firstNonEmpty(req.Source, "tui_agent_attach"),
		AttachedAt: time.Now().UTC(),
	}, nil
}

func (g *appServiceGateway) resolveAgentDescriptor(ctx context.Context, target string) (appservices.AgentDescriptor, bool, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return appservices.AgentDescriptor{}, false, nil
	}
	agents, err := g.services.Agents().List(ctx)
	if err != nil {
		return appservices.AgentDescriptor{}, false, err
	}
	for _, agent := range agents {
		if agent.Kind != appservices.AgentKindExternalACP {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(agent.ID), target) || strings.EqualFold(strings.TrimSpace(agent.Name), target) {
			if strings.TrimSpace(agent.ID) == "" {
				agent.ID = firstNonEmpty(agent.Name, agent.Command)
			}
			return agent, strings.TrimSpace(agent.ID) != "", nil
		}
	}
	return appservices.AgentDescriptor{}, false, nil
}

func participantLifecycleEvent(participant coresession.ParticipantBinding, action string, source string) coresession.Event {
	return coresession.Event{
		Type:       coresession.EventParticipant,
		Visibility: coresession.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      coresession.ActorRef{Kind: coresession.ActorSystem, ID: "caelis", Name: "caelis"},
		Scope: &coresession.EventScope{
			Source:      firstNonEmpty(source, "tui_agent_"+strings.TrimSpace(action)),
			Participant: participant,
		},
		Meta: map[string]any{"action": strings.TrimSpace(action)},
	}
}

func participantsFromCoreSnapshot(snapshot coresession.Snapshot) []coresession.ParticipantBinding {
	out := make([]coresession.ParticipantBinding, 0, len(snapshot.Session.Participants))
	index := map[string]int{}
	upsert := func(participant coresession.ParticipantBinding) {
		participant = normalizeCoreParticipant(participant)
		if participant.ID == "" {
			return
		}
		if i, ok := index[strings.ToLower(participant.ID)]; ok {
			out[i] = mergeCoreParticipant(out[i], participant)
			return
		}
		index[strings.ToLower(participant.ID)] = len(out)
		out = append(out, participant)
	}
	remove := func(id string) {
		id = strings.ToLower(strings.TrimSpace(id))
		i, ok := index[id]
		if !ok {
			return
		}
		out = append(out[:i], out[i+1:]...)
		index = map[string]int{}
		for j, participant := range out {
			index[strings.ToLower(participant.ID)] = j
		}
	}
	for _, participant := range snapshot.Session.Participants {
		upsert(participant)
	}
	for _, event := range snapshot.Events {
		if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
			continue
		}
		participant := event.Scope.Participant
		if event.Type == coresession.EventParticipant && strings.EqualFold(coreEventMetaString(event.Meta, "action"), "detached") {
			remove(participant.ID)
			continue
		}
		upsert(participant)
	}
	return out
}

func normalizeCoreParticipant(in coresession.ParticipantBinding) coresession.ParticipantBinding {
	in.ID = strings.TrimSpace(in.ID)
	in.AgentName = strings.TrimSpace(in.AgentName)
	in.Label = strings.TrimSpace(in.Label)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.Source = strings.TrimSpace(in.Source)
	if in.Kind == "" {
		in.Kind = coresession.ParticipantACP
	}
	if in.Role == "" {
		in.Role = coresession.ParticipantSidecar
	}
	return in
}

func mergeCoreParticipant(existing coresession.ParticipantBinding, next coresession.ParticipantBinding) coresession.ParticipantBinding {
	next = normalizeCoreParticipant(next)
	existing = normalizeCoreParticipant(existing)
	existing.Kind = firstCoreParticipantKind(existing.Kind, next.Kind)
	existing.Role = firstCoreParticipantRole(existing.Role, next.Role)
	existing.AgentName = firstNonEmpty(next.AgentName, existing.AgentName)
	existing.Label = firstNonEmpty(next.Label, existing.Label)
	existing.SessionID = firstNonEmpty(next.SessionID, existing.SessionID)
	existing.Source = firstNonEmpty(next.Source, existing.Source)
	existing.ParentTurnID = firstNonEmpty(next.ParentTurnID, existing.ParentTurnID)
	existing.DelegationID = firstNonEmpty(next.DelegationID, existing.DelegationID)
	if next.ContextSyncSeq != 0 {
		existing.ContextSyncSeq = next.ContextSyncSeq
	}
	if !next.AttachedAt.IsZero() {
		existing.AttachedAt = next.AttachedAt
	}
	existing.ControllerRef = firstNonEmpty(next.ControllerRef, existing.ControllerRef)
	return existing
}

func firstCoreParticipantKind(values ...coresession.ParticipantKind) coresession.ParticipantKind {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstCoreParticipantRole(values ...coresession.ParticipantRole) coresession.ParticipantRole {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func findCoreParticipant(participants []coresession.ParticipantBinding, id string) (coresession.ParticipantBinding, bool) {
	id = strings.TrimSpace(id)
	for _, participant := range participants {
		if strings.EqualFold(strings.TrimSpace(participant.ID), id) {
			return participant, true
		}
	}
	return coresession.ParticipantBinding{}, false
}

func allocateCoreParticipantID(existing []coresession.ParticipantBinding, base string) string {
	base = strings.ToLower(strings.Trim(strings.TrimSpace(base), "@"))
	base = strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-").Replace(base)
	if base == "" {
		base = "agent"
	}
	used := map[string]struct{}{}
	for _, participant := range existing {
		id := strings.ToLower(strings.TrimSpace(participant.ID))
		if id != "" {
			used[id] = struct{}{}
		}
	}
	if _, ok := used[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, ok := used[candidate]; !ok {
			return candidate
		}
	}
}

func coreEventMetaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}
