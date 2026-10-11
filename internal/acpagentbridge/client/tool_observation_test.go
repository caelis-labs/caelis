package client

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestDraftToolObservationUsesSessionOwnerAndTerminalAssociation(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		session  string
		value    map[string]any
		previous bool
	}{
		{"session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool", "previouslyObserved": true, "_meta": map[string]any{"previouslyObserved": true}}, false},
		{"session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool", "status": "completed"}, true},
		{"another-session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool", "previouslyObserved": true}, false},
		{"session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "terminal-tool", "content": []any{map[string]any{"type": "terminal", "terminalId": "terminal"}}}, false},
		{"session", map[string]any{"sessionUpdate": "terminal_update", "terminalId": "terminal", "output": map[string]any{"data": "b3V0cHV0"}}, true},
		{"session", map[string]any{"sessionUpdate": "terminal_output_chunk", "terminalId": "terminal", "data": "c3VmZml4"}, true},
	} {
		if err := agent.update(ctx, test.session, test.value); err != nil {
			t.Fatal(err)
		}
		tool, ok := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
		if !ok || tool.PreviouslyObserved != test.previous {
			t.Fatalf("tool observation lost or accepted wire spoof: %+v", tool)
		}
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		var restored ToolCallUpdate
		if err := json.Unmarshal(raw, &restored); err != nil || restored.PreviouslyObserved {
			t.Fatalf("private observation serialized: %s %v", raw, err)
		}
	}
	if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "terminal_update", "terminalId": "early-terminal", "output": map[string]any{"data": "ZWFybHk="}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := requireDraftUpdate(t, ctx, updates).Update.(RawUpdate); !ok {
		t.Fatal("unassociated terminal unexpectedly became a tool")
	}
	if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "early-tool", "content": []any{map[string]any{"type": "terminal", "terminalId": "early-terminal"}}}); err != nil {
		t.Fatal(err)
	}
	first, ok := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
	if !ok || first.PreviouslyObserved {
		t.Fatalf("terminal association suppressed a new tool: %+v", first)
	}
	flushed, ok := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
	if !ok || !flushed.PreviouslyObserved || flushed.ToolCallID != first.ToolCallID {
		t.Fatalf("terminal flush lost its owning tool observation: %+v", flushed)
	}
}

func TestDraftToolIdentityBudgetClosesWithoutEviction(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			select {
			case <-updates:
			case <-ctx.Done():
				return
			case <-client.conn.Done():
				return
			}
		}
	}()
	for index := 0; index <= draftMaxSessionRecords; index++ {
		if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": fmt.Sprintf("tool-%d", index)}); err != nil {
			break
		}
	}
	select {
	case <-client.conn.Done():
	case <-ctx.Done():
		t.Fatal("tool identity exhaustion did not close the channel")
	}
	client.draft.mu.Lock()
	session := client.draft.sessionLocked("session")
	count, bytes := len(session.tools), session.cacheBytes
	_, firstRetained := session.tools["tool-0"]
	client.draft.mu.Unlock()
	if count != draftMaxSessionRecords || !firstRetained || bytes < draftMaxSessionRecords*128 {
		t.Fatalf("tool identities evicted or omitted from byte budget: count=%d first=%t bytes=%d", count, firstRetained, bytes)
	}
}
