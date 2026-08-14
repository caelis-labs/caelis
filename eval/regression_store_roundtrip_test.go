package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func TestRegressionStoreRoundTripMinimalToolLoop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := inmemory.NewStore(inmemory.Config{})

	sess, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		PreferredSessionID: "sess-roundtrip-tool-loop",
	})

	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	liveScripted := evalharness.NewScriptedModel("live-tool-loop",
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-1",
			Name: "ECHO",
			Args: `{"value":"pong"}`,
		}),
		evalharness.TextStep("pong"),
	)
	liveRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "live-tool-loop",
		SessionID:    sess.SessionID,
		Prompt:       "say pong",
		SystemPrompt: "Use tools when needed.",
		Model:        liveScripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario(live) error = %v", err)
	}

	userEvent := &session.Event{
		Type:    session.EventTypeUser,
		Text:    "say pong",
		Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{{Kind: model.PartKindText, Text: &model.TextPart{Text: "say pong"}}}},
	}
	allEvents := append([]*session.Event{userEvent}, liveRun.Events...)
	canonicalEvents := evalharness.CanonicalEvents(allEvents)
	if len(canonicalEvents) < 2 {
		t.Fatalf("expected at least 2 canonical events (user + agent), got %d", len(canonicalEvents))
	}

	for _, event := range canonicalEvents {
		event.SessionID = ""
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: session.SessionRef{SessionID: sess.SessionID}, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	loaded, err := store.Events(ctx, session.EventsRequest{
		SessionRef: session.SessionRef{SessionID: sess.SessionID},
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}

	if len(loaded) != len(canonicalEvents) {
		t.Fatalf("loaded event count = %d, want %d", len(loaded), len(canonicalEvents))
	}

	for i, event := range loaded {
		got := evalharness.StableJSON(evalharness.EventTrace([]*session.Event{event}))
		want := evalharness.StableJSON(evalharness.EventTrace([]*session.Event{canonicalEvents[i]}))
		if got != want {
			t.Fatalf("loaded event[%d] trace mismatch\nloaded:    %s\ncanonical: %s", i, got, want)
		}
	}

	reloadedScripted := evalharness.NewScriptedModel("reloaded-tool-loop",
		evalharness.TextStep("pong"),
	)
	reloadedRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "reloaded-tool-loop",
		SessionID:    sess.SessionID,
		SystemPrompt: "Use tools when needed.",
		Model:        reloadedScripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
		Events:       loaded,
	})
	if err != nil {
		t.Fatalf("RunChatScenario(reloaded) error = %v", err)
	}

	if len(reloadedRun.Requests) != 1 {
		t.Fatalf("reloaded run request count = %d, want 1", len(reloadedRun.Requests))
	}

	reloadedMessages := evalharness.RequestMessagesJSON(reloadedRun.Requests[0])
	if !strings.Contains(reloadedMessages, "say pong") {
		t.Fatal("reloaded request should contain the original user message 'say pong'")
	}
	if !strings.Contains(reloadedMessages, "pong") {
		t.Fatal("reloaded request should contain the tool result 'pong'")
	}

	canonicalFromReloaded := evalharness.CanonicalEvents(reloadedRun.Events)
	if len(canonicalFromReloaded) != 1 {
		t.Fatalf("reloaded run canonical events = %d, want 1", len(canonicalFromReloaded))
	}
	if canonicalFromReloaded[0].Type != session.EventTypeAssistant {
		t.Fatalf("reloaded event type = %q, want assistant", canonicalFromReloaded[0].Type)
	}
}

func TestRegressionStoreRoundTripReasoning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := inmemory.NewStore(inmemory.Config{})

	sess, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		PreferredSessionID: "sess-roundtrip-reasoning",
	})

	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	liveScripted := evalharness.NewScriptedModel("live-reasoning",
		evalharness.AssistantPartsStep("the answer", "internal chain of thought"),
	)
	liveRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "live-reasoning",
		SessionID:    sess.SessionID,
		Prompt:       "what is 2+2",
		SystemPrompt: "Think step by step.",
		Model:        liveScripted,
	})
	if err != nil {
		t.Fatalf("RunChatScenario(live) error = %v", err)
	}

	userEvent := &session.Event{
		Type:    session.EventTypeUser,
		Text:    "what is 2+2",
		Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{{Kind: model.PartKindText, Text: &model.TextPart{Text: "what is 2+2"}}}},
	}
	allEvents := append([]*session.Event{userEvent}, liveRun.Events...)
	canonicalEvents := evalharness.CanonicalEvents(allEvents)
	if len(canonicalEvents) < 2 {
		t.Fatalf("expected at least 2 canonical events, got %d", len(canonicalEvents))
	}

	for _, event := range canonicalEvents {
		event.SessionID = ""
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: session.SessionRef{SessionID: sess.SessionID}, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	loaded, err := store.Events(ctx, session.EventsRequest{
		SessionRef: session.SessionRef{SessionID: sess.SessionID},
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}

	if len(loaded) != len(canonicalEvents) {
		t.Fatalf("loaded event count = %d, want %d", len(loaded), len(canonicalEvents))
	}

	for i, event := range loaded {
		got := evalharness.StableJSON(evalharness.EventTrace([]*session.Event{event}))
		want := evalharness.StableJSON(evalharness.EventTrace([]*session.Event{canonicalEvents[i]}))
		if got != want {
			t.Fatalf("loaded event[%d] trace mismatch\nloaded:    %s\ncanonical: %s", i, got, want)
		}
	}

	reloadedScripted := evalharness.NewScriptedModel("reloaded-reasoning",
		evalharness.TextStep("follow-up"),
	)
	reloadedRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "reloaded-reasoning",
		SessionID:    sess.SessionID,
		SystemPrompt: "Think step by step.",
		Model:        reloadedScripted,
		Events:       loaded,
	})
	if err != nil {
		t.Fatalf("RunChatScenario(reloaded) error = %v", err)
	}

	if len(reloadedRun.Requests) != 1 {
		t.Fatalf("reloaded run request count = %d, want 1", len(reloadedRun.Requests))
	}

	reloadedMessages := evalharness.RequestMessagesJSON(reloadedRun.Requests[0])
	if !strings.Contains(reloadedMessages, "what is 2+2") {
		t.Fatal("reloaded request should contain the original user question")
	}
	if !strings.Contains(reloadedMessages, "the answer") {
		t.Fatal("reloaded request should contain the assistant response 'the answer'")
	}
}

func TestRegressionStoreRoundTripInvalidToolRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := inmemory.NewStore(inmemory.Config{})

	sess, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		PreferredSessionID: "sess-roundtrip-invalid-retry",
	})

	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	liveScripted := evalharness.NewScriptedModel("live-invalid-retry",
		evalharness.ToolCallStep("trying...", model.ToolCall{
			ID:   "call-invalid",
			Name: "ECHO",
			Args: `{"value":"pong"`,
		}),
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-valid",
			Name: "ECHO",
			Args: `{"value":"pong"}`,
		}),
		evalharness.TextStep("pong"),
	)
	liveRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "live-invalid-retry",
		SessionID:    sess.SessionID,
		Prompt:       "say pong",
		SystemPrompt: "Use tools when needed.",
		Model:        liveScripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario(live) error = %v", err)
	}

	userEvent := &session.Event{
		Type:    session.EventTypeUser,
		Text:    "say pong",
		Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{{Kind: model.PartKindText, Text: &model.TextPart{Text: "say pong"}}}},
	}
	allEvents := append([]*session.Event{userEvent}, liveRun.Events...)
	canonicalEvents := evalharness.CanonicalEvents(allEvents)

	for _, event := range canonicalEvents {
		if event.Tool != nil && event.Tool.ID == "call-invalid" {
			t.Fatal("canonical events should not contain invalid tool call 'call-invalid'")
		}
	}

	for _, event := range canonicalEvents {
		event.SessionID = ""
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: session.SessionRef{SessionID: sess.SessionID}, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	loaded, err := store.Events(ctx, session.EventsRequest{
		SessionRef: session.SessionRef{SessionID: sess.SessionID},
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}

	for _, event := range loaded {
		if event.Tool != nil && event.Tool.ID == "call-invalid" {
			t.Fatal("persisted events should not contain invalid tool call 'call-invalid'")
		}
	}

	reloadedScripted := evalharness.NewScriptedModel("reloaded-invalid-retry",
		evalharness.TextStep("fixed"),
	)
	reloadedRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "reloaded-invalid-retry",
		SessionID:    sess.SessionID,
		SystemPrompt: "Use tools when needed.",
		Model:        reloadedScripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
		Events:       loaded,
	})
	if err != nil {
		t.Fatalf("RunChatScenario(reloaded) error = %v", err)
	}

	reloadedTrace := evalharness.EventTrace(reloadedRun.Events)
	for _, entry := range reloadedTrace {
		if entry.Tool != nil && entry.Tool.ID == "call-invalid" {
			t.Fatal("reloaded trace should not contain invalid tool call 'call-invalid'")
		}
		if strings.Contains(entry.Text, "All checks pass") {
			t.Fatal("reloaded trace should not contain invalid attempt narrative text")
		}
	}

	reloadedMessages := evalharness.RequestMessagesJSON(reloadedRun.Requests[0])
	if strings.Contains(reloadedMessages, `{\"value\":\"pong\"`) {
		t.Fatal("reloaded request should not contain the invalid JSON args from the failed attempt")
	}
}
