package projector

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestProjectSessionEventEnvelopeProjectsToolUpdate(t *testing.T) {
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, &session.Event{
		ID:        "event-1",
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "call-1",
				Kind:          "RunCommand",
				Title:         "RunCommand",
				Status:        "running",
				RawInput:      map[string]any{"command": "echo ok"},
				Content: []session.ProtocolToolCallContent{{
					Type:       "terminal",
					TerminalID: "terminal-1",
					Content:    session.ProtocolTextContent("ok\n"),
				}},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", events[0].Update)
	}
	if update.ToolCallID != "call-1" || stringPtrValue(update.Kind) != "RunCommand" || stringPtrValue(update.Status) != schema.ToolStatusInProgress {
		t.Fatalf("tool update = %#v, want RUN_COMMAND in_progress call-1", update)
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	if info, ok := metautil.TerminalInfo(update.Meta); !ok || info.TerminalID != "call-1" {
		t.Fatalf("terminal_info = %#v, want call-1", update.Meta)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.TerminalID != "call-1" || output.Data != "ok\n" {
		t.Fatalf("terminal_output = %#v, want ok output", update.Meta)
	}
}

func TestProjectSessionEventEnvelopeDemotesUnpositionedDurableDelivery(t *testing.T) {
	t.Parallel()

	for _, visibility := range []session.Visibility{session.VisibilityCanonical, session.VisibilityMirror} {
		t.Run(string(visibility), func(t *testing.T) {
			message := model.NewTextMessage(model.RoleAssistant, "not stored yet")
			event := &session.Event{
				Type:       session.EventTypeAssistant,
				Visibility: visibility,
				Message:    &message,
			}
			base := EnvelopeBaseFromSessionEvent(
				session.SessionRef{SessionID: "session-1"},
				event,
				SessionEventTransport{},
			)
			events := ProjectSessionEventEnvelope(base, event)
			if len(events) != 1 {
				t.Fatalf("ProjectSessionEventEnvelope() = %#v, want one assistant update", events)
			}
			if events[0].Delivery == nil || events[0].Delivery.Mode != eventstream.DeliveryTransient {
				t.Fatalf("delivery = %#v, want transient without stored Event position", events[0].Delivery)
			}
			if events[0].Position != nil {
				t.Fatalf("position = %#v, want no invented durable position", events[0].Position)
			}
		})
	}
}

func TestSessionEventFinalIgnoresAuditSource(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"acp", "slash", "renamed-product-source"} {
		event := &session.Event{
			Visibility: session.VisibilityUIOnly,
			Scope:      &session.EventScope{Source: source},
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			}},
		}
		if SessionEventFinal(event) {
			t.Fatalf("source %q changed live narrative finality", source)
		}
	}
}

func TestProjectSessionEventEnvelopeProjectsNoticeEventstreamOnly(t *testing.T) {
	event := session.MarkNotice(&session.Event{
		ID:        "notice-1",
		SessionID: "session-1",
		Type:      session.EventTypeNotice,
	}, "notice", compact.CompactNoticeLabel)
	event.Notice.Kind = session.EventNoticeKindCompact
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, event)
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	if events[0].Kind != eventstream.KindNotice || events[0].Notice != compact.CompactNoticeLabel {
		t.Fatalf("projected notice = %#v, want eventstream notice", events[0])
	}
	if events[0].NoticeKind != eventstream.NoticeKindCompact {
		t.Fatalf("projected notice kind = %q, want compact", events[0].NoticeKind)
	}
	if eventstream.UpdateType(events[0].Update) != "" {
		t.Fatalf("projected notice update = %#v, want eventstream-only notice", events[0].Update)
	}
}

func TestProjectSessionEventEnvelopeUpgradesLegacyCompactNoticeKind(t *testing.T) {
	event := session.MarkNotice(&session.Event{
		ID:        "notice-legacy-compact",
		SessionID: "session-1",
		Type:      session.EventTypeNotice,
	}, "notice", compact.CompactNoticeLabel)
	events := ProjectSessionEventEnvelope(eventstream.Envelope{SessionID: "session-1"}, event)
	if len(events) != 1 || events[0].NoticeKind != eventstream.NoticeKindCompact {
		t.Fatalf("projected legacy compact notice = %#v, want typed compact kind", events)
	}
}

func TestProjectSessionEventEnvelopeProjectsPermission(t *testing.T) {
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
	}, &session.Event{
		ID:        "permission-1",
		SessionID: "session-1",
		Type:      session.EventTypeLifecycle,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodRequestPermission,
			Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:       "call-1",
					Name:     "RunCommand",
					Kind:     "RunCommand",
					Title:    "RunCommand",
					Status:   "pending",
					RawInput: map[string]any{"command": "go test ./..."},
				},
				Options: []session.ProtocolApprovalOption{{
					ID:   "allow_once",
					Name: "Allow once",
					Kind: "allow_once",
				}},
			},
		},
	})
	if len(events) != 1 || events[0].Kind != eventstream.KindRequestPermission || events[0].Permission == nil {
		t.Fatalf("permission projection = %#v, want request_permission", events)
	}
	permission := events[0].Permission
	if permission.ToolCall.ToolCallID != "call-1" || stringPtrValue(permission.ToolCall.Kind) != "RunCommand" {
		t.Fatalf("permission tool call = %#v, want RUN_COMMAND call-1", permission.ToolCall)
	}
	if len(permission.Options) != 1 || permission.Options[0].OptionID != "allow_once" {
		t.Fatalf("permission options = %#v, want allow_once", permission.Options)
	}
}

func TestProjectSessionEventEnvelopeProjectsUsageAsACPUsageUpdate(t *testing.T) {
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
	}, &session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":       12,
				"cached_input_tokens": 3,
				"completion_tokens":   5,
				"reasoning_tokens":    2,
				"total_tokens":        17,
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope() returned %d events, want usage update: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.UsageUpdate)
	if !ok {
		t.Fatalf("update = %#v, want UsageUpdate", events[0].Update)
	}
	usage := eventstream.UsageSnapshotFromUpdate(update)
	if usage == nil || usage.PromptTokens != 12 || usage.CachedInputTokens != 3 || usage.CompletionTokens != 5 || usage.ReasoningTokens != 2 || usage.TotalTokens != 17 {
		t.Fatalf("usage snapshot = %#v", usage)
	}
}

func TestProjectSessionEventLiveSupplementSeparatesFinalNarrativeFromUsage(t *testing.T) {
	message := model.NewTextMessage(model.RoleAssistant, "complete answer")
	event := &session.Event{
		ID:         "assistant-final",
		Seq:        7,
		SessionID:  "session-1",
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Meta: map[string]any{"usage": map[string]any{
			"prompt_tokens": 11, "completion_tokens": 2, "total_tokens": 13,
		}},
	}
	base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, SessionEventTransport{})

	replay := ProjectSessionEventEnvelope(base, event)
	if len(replay) != 2 || eventstream.UpdateType(replay[0].Update) != schema.UpdateAgentMessage ||
		eventstream.UpdateType(replay[1].Update) != schema.UpdateUsage {
		t.Fatalf("replay projection = %#v, want complete narrative plus usage", replay)
	}
	live := ProjectSessionEventLiveSupplementEnvelope(base, event, agent.PublishedAssistantMessage)
	if len(live) != 1 || eventstream.UpdateType(live[0].Update) != schema.UpdateUsage {
		t.Fatalf("live final projection = %#v, want usage without repeated narrative", live)
	}
	usageOnly := ProjectSessionEventUsageEnvelope(base, event)
	if len(usageOnly) != 1 || usageOnly[0].ProjectionID != replay[1].ProjectionID ||
		eventstream.UpdateType(usageOnly[0].Update) != schema.UpdateUsage {
		t.Fatalf("usage-only projection = %#v, want the canonical usage sibling", usageOnly)
	}
}

func TestProjectSessionEventLiveSupplementKeepsTerminalFinalStateWithoutBytes(t *testing.T) {
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:       "task-1",
		metautil.RuntimeOutputStart:  int64(0),
		metautil.RuntimeOutputCursor: int64(3),
		metautil.RuntimeOutputDelta:  "ok\n",
	})
	event := &session.Event{
		ID:         "command-final",
		Seq:        9,
		SessionID:  "session-1",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Meta:       meta,
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
			ToolCallID:    "call-1",
			Kind:          "RunCommand",
			Title:         "RunCommand",
			Status:        "completed",
			RawInput:      map[string]any{"command": "echo ok"},
			RawOutput:     map[string]any{"stdout": "ok\n", "exit_code": 0},
			Meta:          meta,
			Content: []session.ProtocolToolCallContent{{
				Type: "terminal", TerminalID: "terminal-1", Content: session.ProtocolTextContent("ok\n"),
			}},
		}},
	}
	base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, SessionEventTransport{})

	replay := ProjectSessionEventEnvelope(base, event)
	replayUpdate := replay[0].Update.(schema.ToolCallUpdate)
	if output, ok := metautil.TerminalOutput(replayUpdate.Meta); !ok || output.Data != "ok\n" {
		t.Fatalf("replay terminal output = %#v, want one complete materialization", replayUpdate.Meta)
	}
	if taskMeta := metautil.RuntimeSection(replayUpdate.Meta, metautil.RuntimeTask); taskMeta[metautil.RuntimeOutputDelta] != "ok\n" {
		t.Fatalf("replay task meta = %#v, want complete observation retained", taskMeta)
	}
	live := ProjectSessionEventLiveSupplementEnvelope(base, event, agent.PublishedTerminal)
	if len(live) != 1 {
		t.Fatalf("live terminal final = %#v, want one state update", live)
	}
	liveUpdate := live[0].Update.(schema.ToolCallUpdate)
	if _, ok := metautil.TerminalOutput(liveUpdate.Meta); ok {
		t.Fatalf("live terminal final meta = %#v, want no second terminal byte source", liveUpdate.Meta)
	}
	if taskMeta := metautil.RuntimeSection(liveUpdate.Meta, metautil.RuntimeTask); taskMeta[metautil.RuntimeOutputDelta] != nil {
		t.Fatalf("live terminal final task meta = %#v, want no compatibility terminal delta", taskMeta)
	}
	if taskMeta := metautil.RuntimeSection(live[0].Meta, metautil.RuntimeTask); taskMeta[metautil.RuntimeOutputDelta] != nil {
		t.Fatalf("live terminal final envelope meta = %#v, want no merged compatibility terminal delta", taskMeta)
	}
	if stringPtrValue(liveUpdate.Status) != schema.ToolStatusCompleted {
		t.Fatalf("live terminal final status = %#v, want completed", liveUpdate.Status)
	}
	if exit, ok := metautil.TerminalExit(liveUpdate.Meta); !ok || exit.ExitCode == nil || *exit.ExitCode != 0 {
		t.Fatalf("live terminal final meta = %#v, want exit state retained", liveUpdate.Meta)
	}
}

func TestProjectSessionEventEnvelopeKeepsUserMessagesForGatewayConsumers(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, &session.Event{
		ID:        "event-user-1",
		SessionID: "session-1",
		Type:      session.EventTypeUser,
		Text:      "hello",
		Message:   &user,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope(user) returned %d events, want 1: %#v", len(events), events)
	}
	chunk, ok := events[0].Update.(schema.ContentChunk)
	if !ok || chunk.SessionUpdate != schema.UpdateUserMessage {
		t.Fatalf("update = %#v, want user_message_chunk for gateway/TUI consumers", events[0].Update)
	}
	content, ok := chunk.Content.(schema.TextContent)
	if !ok || content.Text != "hello" {
		t.Fatalf("content = %#v, want hello text", chunk.Content)
	}
}

func TestProjectSessionEventEnvelopeUsesUserDisplayTextWhenMessageIsProjected(t *testing.T) {
	modelVisible := model.NewTextMessage(model.RoleUser, "Load skill `cmpctl` before taking task actions, then follow its instructions.\n\nUser request:\narchive preflight")
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, &session.Event{
		ID:        "event-user-1",
		SessionID: "session-1",
		Type:      session.EventTypeUser,
		Text:      "$cmpctl archive preflight",
		Message:   &modelVisible,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope(user) returned %d events, want 1: %#v", len(events), events)
	}
	chunk, ok := events[0].Update.(schema.ContentChunk)
	if !ok || chunk.SessionUpdate != schema.UpdateUserMessage {
		t.Fatalf("update = %#v, want user_message_chunk for gateway/TUI consumers", events[0].Update)
	}
	content, ok := chunk.Content.(schema.TextContent)
	if !ok || content.Text != "$cmpctl archive preflight" {
		t.Fatalf("content = %#v, want display text", chunk.Content)
	}
}

func TestProjectSessionEventEnvelopeKeepsLiveAndReplayNarrativeAligned(t *testing.T) {
	message := model.MessageFromAssistantParts("I will run pwd.", "Need inspect cwd.", []model.ToolCall{{
		ID:   "call-1",
		Name: "RunCommand",
		Args: `{"command":"pwd"}`,
	}})
	event := &session.Event{
		ID:        "event-1",
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		MessageID: "message-1",
		Message:   &message,
	}

	live := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, event)
	if len(live) != 3 {
		t.Fatalf("live projection produced %d events, want thought, message, and tool call: %#v", len(live), live)
	}
	if eventstream.UpdateType(live[0].Update) != schema.UpdateAgentThought ||
		eventstream.UpdateType(live[1].Update) != schema.UpdateAgentMessage ||
		eventstream.UpdateType(live[2].Update) != schema.UpdateToolCall {
		t.Fatalf("live projection = %#v, want narrative chunks followed by tool call", live)
	}
	for i := 0; i < 2; i++ {
		chunk, ok := live[i].Update.(schema.ContentChunk)
		if !ok || chunk.MessageID != "message-1" {
			t.Fatalf("live narrative[%d] = %#v, want shared typed message identity", i, live[i].Update)
		}
	}

	replay := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, event)
	if len(replay) != len(live) {
		t.Fatalf("replay projection produced %d events, want %d: %#v", len(replay), len(live), replay)
	}
	for i := range live {
		if eventstream.UpdateType(replay[i].Update) != eventstream.UpdateType(live[i].Update) {
			t.Fatalf("projection[%d] live=%q replay=%q", i, eventstream.UpdateType(live[i].Update), eventstream.UpdateType(replay[i].Update))
		}
	}
	for i := 0; i < 2; i++ {
		chunk, ok := replay[i].Update.(schema.ContentChunk)
		if !ok || chunk.MessageID != "message-1" {
			t.Fatalf("replay narrative[%d] = %#v, want shared typed message identity", i, replay[i].Update)
		}
	}
}

func TestEnvelopeBaseFromSessionEventUsesDurableChildOrigin(t *testing.T) {
	event := &session.Event{
		ID:         "child-mirror-1",
		Seq:        17,
		SessionID:  "parent-1",
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityMirror,
		ChildOrigin: &session.EventChildOrigin{
			Scope:         session.EventChildScopeSubagent,
			ScopeID:       "task-1",
			TaskID:        "task-1",
			DelegationID:  "task-1",
			ParticipantID: "child-1",
			ACPSessionID:  "acp-child-1",
			SourceEventID: "task-1:8",
			ParentTool:    session.EventParentTool{CallID: "spawn-1", Name: "Spawn"},
		},
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			MessageID:     "same-message-id",
			Content:       session.ProtocolTextContent("child output"),
		}},
	}
	base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "parent-1"}, event, SessionEventTransport{TurnID: "turn-1"})
	if base.Scope != eventstream.ScopeSubagent || base.ScopeID != "task-1" || base.ParticipantID != "child-1" {
		t.Fatalf("base scope = %#v", base)
	}
	if base.ParentTool == nil || base.ParentTool.ToolCallID != "spawn-1" || base.ParentTool.ToolName != "Spawn" {
		t.Fatalf("base parent relation = %#v", base.ParentTool)
	}
	if base.Delivery == nil || base.Delivery.Mode != eventstream.DeliveryMirror {
		t.Fatalf("base delivery = %#v, want mirror", base.Delivery)
	}
	if base.Final {
		t.Fatal("durable child narrative was marked final before its message boundary closed")
	}
}

func TestEnvelopeBaseKeepsCanonicalizedDurableChildDeltaNonFinal(t *testing.T) {
	message := model.MessageFromAssistantParts("。", "", nil)
	event := session.CanonicalizeEvent(&session.Event{
		ID:         "child-mirror-delta",
		Seq:        18,
		SessionID:  "parent-1",
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityMirror,
		Scope: &session.EventScope{ACP: session.ACPRef{
			SessionID: "child-session-1",
			EventType: string(session.ProtocolUpdateTypeAgentMessage),
		}},
		ChildOrigin: &session.EventChildOrigin{
			Scope:      session.EventChildScopeSubagent,
			ScopeID:    "task-1",
			TaskID:     "task-1",
			ParentTool: session.EventParentTool{CallID: "spawn-1", Name: "Spawn"},
		},
		Message: &message,
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			Content:       session.ProtocolTextContent("。"),
		}},
	})
	if event.Protocol != nil {
		t.Fatalf("CanonicalizeEvent() retained redundant protocol payload: %#v", event.Protocol)
	}
	base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "parent-1"}, event, SessionEventTransport{})
	if base.Final {
		t.Fatal("canonicalized durable child delta was marked final")
	}
	events := ProjectSessionEventEnvelope(base, event)
	if len(events) != 1 || events[0].Final {
		t.Fatalf("ProjectSessionEventEnvelope() = %#v, want one non-final child delta", events)
	}
	chunk, ok := events[0].Update.(schema.ContentChunk)
	if !ok || chunk.SessionUpdate != schema.UpdateAgentMessage || schema.ExtractTextValue(chunk.Content) != "。" {
		t.Fatalf("projected child delta = %#v", events[0].Update)
	}
}

func TestSessionEventFinalKeepsCanonicalAssistantBoundaryFinal(t *testing.T) {
	event := &session.Event{
		ID:         "assistant-1",
		Visibility: session.VisibilityCanonical,
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			MessageID:     "message-1",
			Content:       session.ProtocolTextContent("complete assistant message"),
		}},
	}
	if !SessionEventFinal(event) {
		t.Fatal("ordinary durable canonical assistant event lost its final boundary")
	}
}

func TestProjectSessionEventNotificationsPreservesCustomNotificationsAndAppendsUsage(t *testing.T) {
	notifications, err := ProjectSessionEventNotifications(eventstream.Envelope{
		SessionID: "base-session",
	}, &session.Event{
		SessionID: "event-session",
		Type:      session.EventTypeAssistant,
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 4,
				"total_tokens":      7,
			},
		},
	}, notificationOverrideProjector{})
	if err != nil {
		t.Fatalf("ProjectSessionEventNotifications() error = %v", err)
	}
	if len(notifications) != 2 {
		t.Fatalf("ProjectSessionEventNotifications() produced %d notifications, want custom notification + usage: %#v", len(notifications), notifications)
	}
	chunk, ok := notifications[0].Update.(schema.ContentChunk)
	if !ok || notifications[0].SessionID != "custom-session" || schema.ExtractTextValue(chunk.Content) != "from notifications" {
		t.Fatalf("first notification = %#v, want custom ProjectNotifications output", notifications[0])
	}
	usage, ok := notifications[1].Update.(schema.UsageUpdate)
	if !ok || notifications[1].SessionID != "base-session" || usage.Used != 7 {
		t.Fatalf("usage notification = %#v, want appended usage_update used=7 on base session", notifications[1])
	}
}

func TestProjectSessionEventEnvelopeProjectsContextCompactingAsTransientLifecycle(t *testing.T) {
	event := session.MarkUIOnly(&session.Event{
		Type: session.EventTypeLifecycle,
		Lifecycle: &session.EventLifecycle{
			Status: session.LifecycleStatusContextCompacting,
		},
	})
	base := EnvelopeBaseFromSessionEvent(
		session.SessionRef{SessionID: "session-1"},
		event,
		SessionEventTransport{RunID: "run-1", TurnID: "turn-1"},
	)
	events := ProjectSessionEventEnvelope(base, event)
	if len(events) != 1 || events[0].Kind != eventstream.KindLifecycle || events[0].Lifecycle == nil ||
		events[0].Lifecycle.State != session.LifecycleStatusContextCompacting {
		t.Fatalf("context compacting projection = %#v, want one lifecycle envelope", events)
	}
	if events[0].Delivery == nil || events[0].Delivery.Mode != eventstream.DeliveryTransient || events[0].Position != nil {
		t.Fatalf("context compacting delivery = %#v position=%#v, want transient and unpositioned", events[0].Delivery, events[0].Position)
	}
	if eventstream.IsTurnTerminalLifecycle(events[0]) {
		t.Fatalf("context compacting envelope ended the Turn: %#v", events[0])
	}
}

func TestProjectSessionEventEnvelopeProjectsParticipantAndLifecycleExtensions(t *testing.T) {
	participant := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:        "participant-1",
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "agent-1",
		ParticipantID: "agent-1",
	}, &session.Event{
		ID:    "participant-1",
		Type:  session.EventTypeParticipant,
		Actor: session.ActorRef{Kind: session.ActorKindParticipant, Name: "@agent"},
		Scope: &session.EventScope{Participant: session.ParticipantRef{ID: "agent-1"}},
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodParticipantUpdate,
			Update: &session.ProtocolUpdate{SessionUpdate: "attached"},
		},
	})
	if len(participant) != 1 || participant[0].Kind != eventstream.KindParticipant || participant[0].Participant == nil || participant[0].Participant.State != "attached" {
		t.Fatalf("participant projection = %#v, want participant attached", participant)
	}
	if participant[0].Actor != "@agent" || participant[0].ParticipantID != "agent-1" {
		t.Fatalf("participant envelope = %#v, want actor and participant id", participant[0])
	}

	lifecycle := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:    "lifecycle-1",
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Actor:     "codex",
	}, &session.Event{
		ID:        "lifecycle-1",
		Type:      session.EventTypeLifecycle,
		Actor:     session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		Lifecycle: &session.EventLifecycle{Status: "COMPLETED", Reason: "done"},
	})
	if len(lifecycle) != 1 || lifecycle[0].Kind != eventstream.KindLifecycle || lifecycle[0].Lifecycle == nil || lifecycle[0].Lifecycle.State != "completed" || lifecycle[0].Lifecycle.Reason != "done" {
		t.Fatalf("lifecycle projection = %#v, want lifecycle completed", lifecycle)
	}

	handoff := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:    "handoff-1",
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
	}, &session.Event{
		ID:    "handoff-1",
		Type:  session.EventTypeHandoff,
		Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodControllerHandoff,
			Update: &session.ProtocolUpdate{SessionUpdate: "activation"},
		},
	})
	if len(handoff) != 1 || handoff[0].Kind != eventstream.KindLifecycle || handoff[0].Lifecycle == nil || handoff[0].Lifecycle.State != "activation" {
		t.Fatalf("handoff projection = %#v, want lifecycle activation", handoff)
	}
}

type notificationOverrideProjector struct{}

func (notificationOverrideProjector) ProjectEvent(*session.Event) ([]Update, error) {
	return nil, nil
}

func (notificationOverrideProjector) ProjectNotifications(*session.Event) ([]SessionNotification, error) {
	return []SessionNotification{{
		SessionID: "custom-session",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "from notifications"},
		},
	}}, nil
}

func (notificationOverrideProjector) ProjectPermissionRequest(*session.Event) (*RequestPermissionRequest, bool, error) {
	return nil, false, nil
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
