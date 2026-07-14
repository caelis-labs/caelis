package projector

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

// ACPEventHandle is the protocol-facing subset needed to consume one live ACP
// event stream.
type ACPEventHandle interface {
	HandleID() string
	RunID() string
	TurnID() string
	ACPEvents() <-chan eventstream.Envelope
}

// ACPEventsFromGatewayHandle returns the ACP-native event stream for one
// gateway turn handle.
func ACPEventsFromGatewayHandle(handle ACPEventHandle) <-chan eventstream.Envelope {
	if handle == nil {
		return eventstream.EnsureTerminalLifecycle(nil, "", "", "")
	}
	return eventstream.EnsureTerminalLifecycle(handle.ACPEvents(), handle.HandleID(), handle.RunID(), handle.TurnID())
}

// ProjectSessionEventEnvelope projects one canonical session event into
// ACP-native client envelopes using the supplied envelope metadata as the
// transport context.
func ProjectSessionEventEnvelope(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
	return ProjectSessionEventEnvelopeWithProjector(base, event, EventProjector{})
}

// ProjectApprovalPayloadEnvelope projects one live gateway approval request
// through the same ACP permission path used for durable replay.
func ProjectApprovalPayloadEnvelope(base eventstream.Envelope, payload *approval.Payload) []eventstream.Envelope {
	approval := protocolApprovalFromPayload(payload)
	if approval == nil {
		return nil
	}
	permission := permissionRequestFromProtocol(strings.TrimSpace(base.SessionID), base.Meta, approval)
	if permission == nil {
		return nil
	}
	next := base
	next.Kind = eventstream.KindRequestPermission
	next.Permission = permission
	return []eventstream.Envelope{next}
}

// SessionEventTransport carries live transport ids that are unavailable from
// durable session events but should be attached to projected client envelopes.
type SessionEventTransport struct {
	HandleID string
	RunID    string
	TurnID   string
}

// EnvelopeBaseFromSessionEvent returns the canonical eventstream envelope
// metadata derived from one session event plus optional live transport context.
func EnvelopeBaseFromSessionEvent(ref session.SessionRef, event *session.Event, transport SessionEventTransport) eventstream.Envelope {
	base := eventstream.Envelope{
		SessionID: strings.TrimSpace(ref.SessionID),
		HandleID:  strings.TrimSpace(transport.HandleID),
		RunID:     strings.TrimSpace(transport.RunID),
		Scope:     eventstream.ScopeMain,
		ScopeID:   strings.TrimSpace(ref.SessionID),
	}
	if event == nil {
		base.TurnID = strings.TrimSpace(transport.TurnID)
		return base
	}
	base.EventID = strings.TrimSpace(event.ID)
	base.ApprovalRequestID = eventstream.ApprovalRequestID(strings.TrimSpace(event.ApprovalRequestID))
	base.TurnID = firstNonEmpty(sessionEventTurnID(event), strings.TrimSpace(transport.TurnID))
	base.OccurredAt = event.Time
	base.Final = SessionEventFinal(event)
	base.Delivery = sessionEventDelivery(event)
	base.Meta = cloneAnyMap(event.Meta)
	base.Actor = firstNonEmpty(strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID))
	if event.ChildOrigin != nil {
		origin := session.CloneEventChildOrigin(*event.ChildOrigin)
		base.ScopeID = firstNonEmpty(origin.ScopeID, origin.TaskID, origin.DelegationID, base.ScopeID)
		base.ParticipantID = origin.ParticipantID
		switch origin.Scope {
		case session.EventChildScopeSubagent:
			base.Scope = eventstream.ScopeSubagent
		case session.EventChildScopeParticipant:
			base.Scope = eventstream.ScopeParticipant
		}
		if origin.ParentTool.CallID != "" {
			base.ParentTool = &eventstream.ParentToolRelation{
				ToolCallID: origin.ParentTool.CallID,
				ToolName:   origin.ParentTool.Name,
			}
		}
		return base
	}
	if event.Scope == nil {
		return base
	}
	participantID := strings.TrimSpace(event.Scope.Participant.ID)
	base.ParticipantID = participantID
	switch {
	case participantID != "" && event.Scope.Participant.Kind == session.ParticipantKindSubagent:
		base.Scope = eventstream.ScopeSubagent
		base.ScopeID = firstNonEmpty(strings.TrimSpace(event.Scope.Participant.DelegationID), strings.TrimSpace(event.Scope.ACP.SessionID), participantID, base.ScopeID)
	case participantID != "":
		base.Scope = eventstream.ScopeParticipant
		base.ScopeID = firstNonEmpty(strings.TrimSpace(event.Scope.TurnID), strings.TrimSpace(event.Scope.ACP.SessionID), participantID, base.ScopeID)
	default:
		base.ScopeID = firstNonEmpty(strings.TrimSpace(event.Scope.TurnID), base.ScopeID)
	}
	return base
}

func sessionEventDelivery(event *session.Event) *eventstream.Delivery {
	switch {
	case event == nil:
		return nil
	case session.IsMirror(event):
		return &eventstream.Delivery{Mode: eventstream.DeliveryMirror}
	case session.IsTransient(event):
		return &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	case session.IsCanonicalHistoryEvent(event):
		return &eventstream.Delivery{Mode: eventstream.DeliveryCanonical}
	default:
		return nil
	}
}

// SessionEventFinal reports whether a projected session event should be treated
// as a final transcript/update boundary.
func SessionEventFinal(event *session.Event) bool {
	if event == nil {
		return false
	}
	return event.Visibility != session.VisibilityUIOnly && !isLiveStreamingNarrativeEvent(event)
}

func isLiveStreamingNarrativeEvent(event *session.Event) bool {
	if event == nil {
		return false
	}
	durableChildMirror := session.IsMirror(event) && event.ChildOrigin != nil
	if strings.TrimSpace(event.ID) != "" && !durableChildMirror {
		return false
	}
	if event.Scope == nil && !durableChildMirror {
		return false
	}
	updateType := strings.TrimSpace(session.ProtocolSessionUpdateType(event))
	if updateType == "" && event.Scope != nil {
		// Canonical storage removes a redundant Protocol.Update when Message
		// already carries the same text. Child ACP chunks still retain their
		// normalized update identity on EventScope.ACP; use it so durable mirror
		// replay does not turn every delta into a final transcript boundary.
		updateType = strings.TrimSpace(event.Scope.ACP.EventType)
	}
	switch updateType {
	case string(session.ProtocolUpdateTypeAgentMessage), string(session.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
}

func sessionEventTurnID(event *session.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}

// ProjectSessionEventEnvelopeWithProjector projects one canonical session event
// with a caller-supplied ACP projector, then appends standard usage_update when
// provider usage is attached to the source event.
func ProjectSessionEventEnvelopeWithProjector(base eventstream.Envelope, event *session.Event, projector Projector) []eventstream.Envelope {
	if projector == nil {
		projector = EventProjector{}
	}
	out := projectSessionEventToACPEnvelopes(base, event, projector)
	if len(out) == 0 {
		out = append(out, projectSessionEventstreamOnlyEvents(base, event)...)
	}
	if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil && !containsUsageUpdate(out) {
		out = append(out, gatewayUsageEnvelope(base, usage))
	}
	return stampDurableProjectionPositions(event, out)
}

func stampDurableProjectionPositions(event *session.Event, events []eventstream.Envelope) []eventstream.Envelope {
	if event == nil || event.Seq == 0 || strings.TrimSpace(event.ID) == "" || len(events) == 0 {
		return events
	}
	out := make([]eventstream.Envelope, len(events))
	for index, env := range events {
		env.EventID = strings.TrimSpace(event.ID)
		env.ProjectionID = eventstream.FormatProjectionID(event.ID, index)
		env.Position = &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
			Seq:             event.Seq,
			ProjectionIndex: uint32(index),
		}}
		out[index] = env
	}
	return out
}

// ProjectSessionEventNotifications projects one canonical session event into
// ACP session/update notifications. Eventstream-only extensions and historical
// request_permission prompts are intentionally not replayed through session/load.
func ProjectSessionEventNotifications(base eventstream.Envelope, event *session.Event, projector Projector) ([]SessionNotification, error) {
	if projector == nil {
		projector = EventProjector{}
	}
	notifications, err := projector.ProjectNotifications(event)
	if err != nil {
		return nil, err
	}
	out := cloneSessionNotifications(notifications, base, event)
	if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil && !containsUsageNotification(out) {
		usageEnv := gatewayUsageEnvelope(base, usage)
		out = append(out, SessionNotification{
			SessionID: sessionNotificationID(usageEnv.SessionID, base, event),
			Update:    eventstream.CloneUpdate(usageEnv.Update),
		})
	}
	return out, nil
}

func cloneSessionNotifications(notifications []SessionNotification, base eventstream.Envelope, event *session.Event) []SessionNotification {
	out := make([]SessionNotification, 0, len(notifications))
	for _, notification := range notifications {
		if notification.Update == nil {
			continue
		}
		out = append(out, SessionNotification{
			SessionID: sessionNotificationID(notification.SessionID, base, event),
			Update:    eventstream.CloneUpdate(notification.Update),
		})
	}
	return out
}

func sessionNotificationID(candidate string, base eventstream.Envelope, event *session.Event) string {
	if sessionID := firstNonEmpty(candidate, base.SessionID); sessionID != "" {
		return sessionID
	}
	if event == nil {
		return ""
	}
	return strings.TrimSpace(event.SessionID)
}

func projectSessionEventToACPEnvelopes(base eventstream.Envelope, sessionEvent *session.Event, projector Projector) []eventstream.Envelope {
	out := make([]eventstream.Envelope, 0, 2)
	if permission, ok, err := projector.ProjectPermissionRequest(sessionEvent); err != nil {
		return []eventstream.Envelope{eventstream.Error(err)}
	} else if ok && permission != nil {
		next := base
		next.Kind = eventstream.KindRequestPermission
		permission.Meta = mergeACPEventMeta(permission.Meta, base.Meta)
		if meta := permissionToolMeta(base.Meta, sessionEvent); len(meta) > 0 {
			permission.ToolCall.Meta = mergeACPEventMeta(permission.ToolCall.Meta, meta)
		}
		next.Permission = permission
		out = append(out, next)
	}
	updates, err := projector.ProjectEvent(sessionEvent)
	if err != nil {
		return []eventstream.Envelope{eventstream.Error(err)}
	}
	var updateMeta map[string]any
	if update := session.ProtocolUpdateOf(sessionEvent); update != nil {
		updateMeta = update.Meta
	}
	for _, update := range updates {
		if update == nil {
			continue
		}
		next := base
		next.Kind = eventstream.KindSessionUpdate
		next.Update = update
		if len(updateMeta) > 0 {
			next.Meta = mergeACPEventMeta(updateMeta, next.Meta)
		}
		out = append(out, next)
	}
	return out
}

func gatewayUsageEnvelope(base eventstream.Envelope, usage *session.UsageSnapshot) eventstream.Envelope {
	if usage == nil {
		return eventstream.Envelope{}
	}
	return eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		Cursor:        base.Cursor,
		EventID:       base.EventID,
		ProjectionID:  base.ProjectionID,
		Position:      eventstream.CloneFeedPosition(base.Position),
		SessionID:     base.SessionID,
		HandleID:      base.HandleID,
		RunID:         base.RunID,
		TurnID:        base.TurnID,
		OccurredAt:    base.OccurredAt,
		Scope:         base.Scope,
		ScopeID:       base.ScopeID,
		Actor:         base.Actor,
		ParticipantID: base.ParticipantID,
		ParentTool:    cloneParentToolRelation(base.ParentTool),
		Delivery:      cloneDelivery(base.Delivery),
		Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{
			PromptTokens:        usage.PromptTokens,
			CachedInputTokens:   usage.CachedInputTokens,
			CompletionTokens:    usage.CompletionTokens,
			ReasoningTokens:     usage.ReasoningTokens,
			TotalTokens:         usage.TotalTokens,
			ContextWindowTokens: usage.ContextWindowTokens,
		}, base.Meta),
	}
}

func cloneParentToolRelation(in *eventstream.ParentToolRelation) *eventstream.ParentToolRelation {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneDelivery(in *eventstream.Delivery) *eventstream.Delivery {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func containsUsageUpdate(events []eventstream.Envelope) bool {
	for _, env := range events {
		if eventstream.UpdateType(env.Update) == schema.UpdateUsage {
			return true
		}
	}
	return false
}

func containsUsageNotification(notifications []SessionNotification) bool {
	for _, notification := range notifications {
		if eventstream.UpdateType(notification.Update) == schema.UpdateUsage {
			return true
		}
	}
	return false
}

func projectSessionEventstreamOnlyEvents(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
	if event == nil {
		return nil
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeNotice:
		notice, ok := session.NoticeOf(event)
		if !ok || strings.TrimSpace(notice.Text) == "" {
			return nil
		}
		next := base
		next.Kind = eventstream.KindNotice
		next.Notice = strings.TrimSpace(notice.Text)
		return []eventstream.Envelope{next}
	case session.EventTypeParticipant:
		if event.Protocol == nil {
			return nil
		}
		participant, err := semantic.DecodeParticipant(*event.Protocol)
		if err != nil {
			return nil
		}
		return participantEventstreamEnvelope(base, participant.Action, firstNonEmpty(strings.TrimSpace(base.Actor), strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID)))
	case session.EventTypeHandoff:
		if event.Protocol == nil {
			return nil
		}
		handoff, err := semantic.DecodeHandoff(*event.Protocol)
		if err != nil {
			return nil
		}
		return lifecycleEventstreamEnvelope(base, handoff.Phase, "", firstNonEmpty(strings.TrimSpace(base.Actor), strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID)))
	case session.EventTypeLifecycle:
		if event.Lifecycle == nil {
			return nil
		}
		return lifecycleEventstreamEnvelope(base, event.Lifecycle.Status, event.Lifecycle.Reason, firstNonEmpty(strings.TrimSpace(base.Actor), strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID)))
	default:
		return nil
	}
}

func participantEventstreamEnvelope(base eventstream.Envelope, state string, actor string) []eventstream.Envelope {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil
	}
	next := base
	next.Kind = eventstream.KindParticipant
	next.Actor = strings.TrimSpace(actor)
	next.Participant = &eventstream.Participant{State: state}
	return []eventstream.Envelope{next}
}

func lifecycleEventstreamEnvelope(base eventstream.Envelope, state string, reason string, actor string) []eventstream.Envelope {
	state = strings.TrimSpace(state)
	reason = strings.TrimSpace(reason)
	if state == "" && reason == "" {
		return nil
	}
	next := base
	next.Kind = eventstream.KindLifecycle
	next.Actor = strings.TrimSpace(actor)
	next.Lifecycle = &eventstream.Lifecycle{
		State:  strings.ToLower(state),
		Reason: reason,
	}
	return []eventstream.Envelope{next}
}

func mergeACPEventMeta(base map[string]any, overlay map[string]any) map[string]any {
	return metautil.Merge(base, overlay)
}

func permissionToolMeta(baseMeta map[string]any, sessionEvent *session.Event) map[string]any {
	toolName := ""
	if sessionEvent != nil && sessionEvent.Protocol != nil {
		if permission := session.ProtocolPermissionOf(sessionEvent); permission != nil {
			toolName = strings.TrimSpace(permission.ToolCall.Name)
		}
	}
	return acpMetaWithToolName(baseMeta, toolName)
}

func acpMetaWithToolName(meta map[string]any, toolName string) map[string]any {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return cloneAnyMap(meta)
	}
	return metautil.WithRuntimeSection(meta, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: toolName,
	})
}

func permissionRequestFromProtocol(sessionID string, meta map[string]any, approval *session.ProtocolApproval) *schema.RequestPermissionRequest {
	if approval == nil {
		return nil
	}
	req, err := semantic.EncodePermissionRequest(session.SessionRef{SessionID: sessionID}, approval, meta)
	if err != nil {
		return nil
	}
	// Permission wire semantics come from semantic.EncodePermissionRequest.
	// Projection may add display-only defaults without changing canonical
	// identity, input, output, options, or approval meaning.
	displayToolCall := permissionToolCallUpdateFromProtocol(approval.ToolCall)
	if req.ToolCall.Title == nil {
		req.ToolCall.Title = displayToolCall.Title
	}
	if req.ToolCall.Kind == nil {
		req.ToolCall.Kind = displayToolCall.Kind
	}
	if req.ToolCall.Status == nil {
		req.ToolCall.Status = displayToolCall.Status
	}
	if len(req.ToolCall.Content) == 0 {
		req.ToolCall.Content = displayToolCall.Content
	}
	req.ToolCall.Meta = mergeACPEventMeta(req.ToolCall.Meta, displayToolCall.Meta)
	if strings.TrimSpace(req.ToolCall.ToolCallID) == "" &&
		req.ToolCall.Title == nil &&
		req.ToolCall.Kind == nil &&
		len(approval.Options) == 0 &&
		len(schema.NormalizeRawMap(req.ToolCall.RawInput)) == 0 {
		return nil
	}
	return &req
}

func ApprovalPayloadFromPermission(req *schema.RequestPermissionRequest) *approval.Payload {
	if req == nil {
		return nil
	}
	_, normalized, _, err := semantic.DecodePermissionRequest(*req)
	if err != nil || normalized == nil {
		return nil
	}
	rawInput := cloneAnyMap(normalized.ToolCall.RawInput)
	payload := &approval.Payload{
		ToolCallID:         strings.TrimSpace(normalized.ToolCall.ID),
		ToolName:           strings.TrimSpace(normalized.ToolCall.Name),
		ToolKind:           strings.TrimSpace(normalized.ToolCall.Kind),
		ToolTitle:          strings.TrimSpace(normalized.ToolCall.Title),
		ToolStatus:         strings.TrimSpace(normalized.ToolCall.Status),
		RawInput:           rawInput,
		RawOutput:          cloneAnyMap(normalized.ToolCall.RawOutput),
		Content:            session.CloneProtocolToolCallContent(normalized.ToolCall.Content),
		Reason:             firstNonEmpty(rawString(rawInput, "approval_reason"), rawString(rawInput, "reason")),
		Justification:      rawString(rawInput, "justification"),
		SandboxPermissions: rawString(rawInput, "sandbox_permissions"),
		Status:             approval.StatusPending,
	}
	if len(normalized.Options) > 0 {
		payload.Options = make([]approval.Option, 0, len(normalized.Options))
		for _, option := range normalized.Options {
			payload.Options = append(payload.Options, approval.Option{
				ID:   strings.TrimSpace(option.ID),
				Name: strings.TrimSpace(option.Name),
				Kind: strings.TrimSpace(option.Kind),
			})
		}
	}
	return payload
}

func protocolApprovalFromPayload(payload *approval.Payload) *session.ProtocolApproval {
	return approval.ProtocolApprovalFromPayload(payload)
}

func approvalRawInput(payload *approval.Payload) map[string]any {
	if payload == nil {
		return nil
	}
	raw := cloneAnyMap(payload.RawInput)
	raw = putApprovalRawStringIfMissing(raw, "approval_reason", payload.Reason)
	raw = putApprovalRawStringIfMissing(raw, "justification", payload.Justification)
	raw = putApprovalRawStringIfMissing(raw, "sandbox_permissions", payload.SandboxPermissions)
	return raw
}

func putApprovalRawStringIfMissing(raw map[string]any, key string, value string) map[string]any {
	value = strings.TrimSpace(value)
	if value == "" {
		return raw
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if _, exists := raw[key]; !exists {
		raw[key] = value
	}
	return raw
}

func rawString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	text, _ := values[key].(string)
	return strings.TrimSpace(text)
}

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
