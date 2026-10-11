package client

import (
	"context"
	"encoding/json"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

func TestDraftMessageObservationIsOwnedBySessionMaterialization(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "agent_message", "messageId": "message", "_meta": map[string]any{"label": "metadata only introduction"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 0 {
		t.Fatal("metadata-only introduction emitted content")
	}
	for _, test := range []struct {
		session, kind string
		content       any
		previous      bool
		metaOnly      bool
	}{
		{"session", "agent_message", []any{map[string]any{"type": "text", "text": "first"}}, false, false},
		{"session", "agent_message_chunk", map[string]any{"type": "text", "text": " suffix"}, true, false},
		{"session", "agent_message", nil, true, true},
		{"another-session", "agent_message", []any{map[string]any{"type": "text", "text": "new Session answer"}}, false, false},
	} {
		value := map[string]any{"sessionUpdate": test.kind, "messageId": "message", "previouslyObserved": true}
		if test.metaOnly {
			value["_meta"] = nil
		} else {
			value["content"] = test.content
		}
		if err := agent.update(ctx, test.session, value); err != nil {
			t.Fatal(err)
		}
		chunk, ok := requireDraftUpdate(t, ctx, updates).Update.(ContentChunk)
		if !ok || chunk.PreviouslyObserved != test.previous || chunk.MetaPresent != test.metaOnly {
			t.Fatalf("Session observation lost or accepted peer hint: %+v", chunk)
		}
		raw, err := json.Marshal(chunk)
		if err != nil {
			t.Fatal(err)
		}
		var restored ContentChunk
		if err := json.Unmarshal(raw, &restored); err != nil || restored.PreviouslyObserved {
			t.Fatalf("process-local observation leaked to wire: %s %v", raw, err)
		}
	}
}

func TestDraftRepeatedReplayRetainsMessageObservation(t *testing.T) {
	agent := &draftTestAgent{}
	agent.resume = func(ctx context.Context, request v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
		return v2.ResumeSessionResponse{}, agent.update(ctx, string(request.SessionId), map[string]any{"sessionUpdate": "agent_message", "messageId": "persisted-answer", "content": []any{map[string]any{"type": "text", "text": "persisted answer"}}})
	}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, previous := range []bool{false, true} {
		_, err := client.LoadSessionWithReplayEnd(ctx, "session", "/tmp", nil, func(context.Context) error {
			chunk, ok := requireDraftUpdate(t, ctx, updates).Update.(ContentChunk)
			if !ok || chunk.MessageID != "persisted-answer" || chunk.PreviouslyObserved != previous {
				t.Errorf("repeated resume changed Session observation: %+v", chunk)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		created, err := client.NewSession(ctx, "/tmp", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := agent.update(ctx, created.SessionID, map[string]any{"sessionUpdate": "agent_message", "messageId": "persisted-answer", "content": []any{map[string]any{"type": "text", "text": "new Session answer"}}}); err != nil {
			t.Fatal(err)
		}
		chunk, ok := requireDraftUpdate(t, ctx, updates).Update.(ContentChunk)
		if !ok || chunk.PreviouslyObserved {
			t.Fatalf("new Session reused old observation: %+v", chunk)
		}
	}
}
