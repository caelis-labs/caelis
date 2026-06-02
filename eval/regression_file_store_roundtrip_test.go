package eval

import (
	"context"
	"strings"
	"testing"

	filestore "github.com/OnslaughtSnail/caelis/impl/session/file"
	"github.com/OnslaughtSnail/caelis/internal/evalharness"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func newFileStoreForTest(t *testing.T) *filestore.Store {
	t.Helper()
	return filestore.NewStore(filestore.Config{RootDir: t.TempDir()})
}

func TestRegressionFileStoreRoundTripMinimalToolLoop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newFileStoreForTest(t)

	sess, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		PreferredSessionID: "sess-file-tool-loop",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	liveScripted := evalharness.NewScriptedModel("live-file-tool-loop",
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-1",
			Name: "ECHO",
			Args: `{"value":"pong"}`,
		}),
		evalharness.TextStep("pong"),
	)
	liveRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "live-file-tool-loop",
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
		t.Fatalf("expected at least 2 canonical events, got %d", len(canonicalEvents))
	}

	for _, event := range canonicalEvents {
		event.SessionID = ""
		if _, err := store.AppendEvent(ctx, session.SessionRef{SessionID: sess.SessionID}, event); err != nil {
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

	reloadedScripted := evalharness.NewScriptedModel("reloaded-file-tool-loop",
		evalharness.TextStep("pong"),
	)
	reloadedRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "reloaded-file-tool-loop",
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

func TestRegressionFileStoreRoundTripReasoning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newFileStoreForTest(t)

	sess, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		PreferredSessionID: "sess-file-reasoning",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	liveScripted := evalharness.NewScriptedModel("live-file-reasoning",
		evalharness.AssistantPartsStep("the answer", "internal chain of thought"),
	)
	liveRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "live-file-reasoning",
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

	for _, event := range canonicalEvents {
		event.SessionID = ""
		if _, err := store.AppendEvent(ctx, session.SessionRef{SessionID: sess.SessionID}, event); err != nil {
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

	reloadedScripted := evalharness.NewScriptedModel("reloaded-file-reasoning",
		evalharness.TextStep("follow-up"),
	)
	reloadedRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "reloaded-file-reasoning",
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

func TestRegressionFileStoreRoundTripInvalidToolRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newFileStoreForTest(t)

	sess, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		PreferredSessionID: "sess-file-invalid-retry",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	liveScripted := evalharness.NewScriptedModel("live-file-invalid-retry",
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
		Name:         "live-file-invalid-retry",
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
		if _, err := store.AppendEvent(ctx, session.SessionRef{SessionID: sess.SessionID}, event); err != nil {
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

	reloadedScripted := evalharness.NewScriptedModel("reloaded-file-invalid-retry",
		evalharness.TextStep("fixed"),
	)
	reloadedRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:         "reloaded-file-invalid-retry",
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
