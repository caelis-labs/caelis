package projection

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestProjectSessionEventEnvelopeKeepsStreamingAndFinalMessageID(t *testing.T) {
	t.Parallel()

	const messageID = "logical-message-1"
	base := eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}

	streamChunks := []*session.Event{
		uiAgentMessageChunk(messageID, "hel"),
		uiAgentMessageChunk(messageID, "lo"),
	}
	finalMessage := model.NewTextMessage(model.RoleAssistant, "hello")
	final := &session.Event{
		ID:         "event-final",
		Seq:        3,
		SessionID:  "session-1",
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		MessageID:  messageID,
		Message:    &finalMessage,
		Text:       "hello",
	}

	var wireIDs []string
	var texts []string
	for _, event := range append(streamChunks, final) {
		for _, env := range ProjectSessionEventEnvelope(base, event) {
			chunk, ok := env.Update.(schema.ContentChunk)
			if !ok || chunk.SessionUpdate != schema.UpdateAgentMessage {
				continue
			}
			if chunk.MessageID == "" {
				t.Fatalf("projected agent_message_chunk missing messageId: %#v", chunk)
			}
			wireIDs = append(wireIDs, chunk.MessageID)
			if content, ok := chunk.Content.(schema.TextContent); ok {
				texts = append(texts, content.Text)
			}
		}
	}
	if len(wireIDs) < 2 {
		t.Fatalf("projected chunks = %#v, want stream plus final narrative", texts)
	}
	for i, id := range wireIDs {
		if id != messageID {
			t.Fatalf("wire messageId[%d] = %q, want shared logical identity %q (texts=%#v)", i, id, messageID, texts)
		}
	}
}

func TestProjectSessionEventEnvelopeKeepsPreToolAndPostToolMessageIDsDistinct(t *testing.T) {
	t.Parallel()

	base := eventstream.Envelope{
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}
	preTool := model.MessageFromAssistantParts("I will inspect.", "", []model.ToolCall{{
		ID: "call-1", Name: "Read", Args: `{"path":"README.md"}`,
	}})
	postTool := model.NewTextMessage(model.RoleAssistant, "Done.")

	events := []*session.Event{
		uiAgentMessageChunk("pre-tool-message", "I will inspect."),
		{
			ID:         "tool-call-1",
			Seq:        2,
			SessionID:  "session-1",
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			MessageID:  "pre-tool-message",
			Message:    &preTool,
			Text:       "I will inspect.",
			Tool: &session.EventTool{
				ID: "call-1", Name: "Read", Status: "pending",
				Input: map[string]any{"path": "README.md"},
			},
		},
		{
			ID:         "post-tool-final",
			Seq:        4,
			SessionID:  "session-1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			MessageID:  "post-tool-message",
			Message:    &postTool,
			Text:       "Done.",
		},
	}

	var agentMessageIDs []string
	for _, event := range events {
		for _, env := range ProjectSessionEventEnvelope(base, event) {
			chunk, ok := env.Update.(schema.ContentChunk)
			if !ok || chunk.SessionUpdate != schema.UpdateAgentMessage {
				continue
			}
			if chunk.MessageID == "" {
				t.Fatalf("agent_message_chunk missing messageId: %#v", chunk)
			}
			agentMessageIDs = append(agentMessageIDs, chunk.MessageID)
		}
	}
	if len(agentMessageIDs) < 2 {
		t.Fatalf("agent message ids = %#v, want pre-tool and post-tool narrative", agentMessageIDs)
	}
	for i, id := range agentMessageIDs[:len(agentMessageIDs)-1] {
		if id != "pre-tool-message" {
			t.Fatalf("pre-tool narrative messageId[%d] = %q, want pre-tool-message (ids=%#v)", i, id, agentMessageIDs)
		}
	}
	last := agentMessageIDs[len(agentMessageIDs)-1]
	if last != "post-tool-message" {
		t.Fatalf("post-tool messageId = %q, want post-tool-message", last)
	}
	if last == agentMessageIDs[0] {
		t.Fatal("post-tool narrative reused pre-tool messageId; reconnect would splice two logical messages")
	}
}

func TestProjectSessionEventEnvelopeReplayPreservesCanonicalMessageID(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleAssistant, "replayed answer")
	event := session.CanonicalizeEvent(&session.Event{
		ID:         "event-1",
		Seq:        9,
		SessionID:  "session-1",
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		MessageID:  "replay-message-1",
		Message:    &message,
		Text:       "replayed answer",
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     "replay-message-1",
				Content:       session.ProtocolTextContent("replayed answer"),
			},
		},
	})
	if event == nil || event.MessageID != "replay-message-1" {
		t.Fatalf("canonical event = %#v, want typed MessageID retained", event)
	}

	live := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, event)
	replay := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, event)
	if len(live) == 0 || len(replay) == 0 {
		t.Fatalf("live=%#v replay=%#v, want narrative projection", live, replay)
	}
	liveChunk, ok := live[0].Update.(schema.ContentChunk)
	if !ok || liveChunk.MessageID != "replay-message-1" {
		t.Fatalf("live projection = %#v, want messageId replay-message-1", live[0].Update)
	}
	replayChunk, ok := replay[0].Update.(schema.ContentChunk)
	if !ok || replayChunk.MessageID != "replay-message-1" {
		t.Fatalf("replay projection = %#v, want messageId replay-message-1", replay[0].Update)
	}
}

func uiAgentMessageChunk(messageID, text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return session.MarkUIOnly(&session.Event{
		Type:      session.EventTypeAssistant,
		MessageID: messageID,
		Message:   &message,
		Text:      text,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     messageID,
				Content:       session.ProtocolTextContent(text),
			},
		},
	})
}
