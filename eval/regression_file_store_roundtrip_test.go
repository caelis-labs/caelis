package eval

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	filestore "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func newFileStoreForTest(t *testing.T) *filestore.Store {
	t.Helper()
	return filestore.NewStore(filestore.Config{RootDir: t.TempDir()})
}

func requestToolNames(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		if spec.Function != nil {
			out = append(out, spec.Function.Name)
		}
	}
	return out
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

func TestRegressionFileStoreRoundTripDeferredMCPToolVisibility(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newFileStoreForTest(t)

	sess, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		PreferredSessionID: "sess-file-deferred-mcp-tool",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	const mcpToolName = "mcp__calendar__demo__create_event"
	mcpTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        mcpToolName,
			Description: "Create calendar events",
			InputSchema: map[string]any{"type": "object"},
			Metadata: map[string]any{
				tool.MetadataToolKind:  tool.MetadataToolKindMCP,
				tool.MetadataPluginID:  "calendar",
				tool.MetadataMCPServer: "demo",
				tool.MetadataMCPTool:   "create_event",
			},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart(evalharness.MustJSON(map[string]any{"value": "created"})),
				},
			}, nil
		},
	}
	searchTool := toolsearch.New([]tool.Tool{mcpTool})
	if searchTool == nil {
		t.Fatal("toolsearch.New(MCP tool) = nil")
	}
	tools := []tool.Tool{searchTool, mcpTool}

	liveScripted := evalharness.NewScriptedModel("live-file-deferred-mcp",
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-search",
			Name: tool.ToolSearchToolName,
			Args: `{"query":"calendar event"}`,
		}),
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-mcp",
			Name: mcpToolName,
			Args: `{}`,
		}),
		evalharness.TextStep("created"),
	)
	liveRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:      "live-file-deferred-mcp",
		SessionID: sess.SessionID,
		Prompt:    "create a calendar event",
		Model:     liveScripted,
		Tools:     tools,
	})
	if err != nil {
		t.Fatalf("RunChatScenario(live) error = %v", err)
	}
	if got, want := len(liveRun.Requests), 3; got != want {
		t.Fatalf("live request count = %d, want %d", got, want)
	}
	if got, want := requestToolNames(liveRun.Requests[0]), []string{tool.ToolSearchToolName}; !slices.Equal(got, want) {
		t.Fatalf("initial request tools = %v, want %v", got, want)
	}
	livePostSearchTools := requestToolNames(liveRun.Requests[1])
	if want := []string{tool.ToolSearchToolName, mcpToolName}; !slices.Equal(livePostSearchTools, want) {
		t.Fatalf("post-search request tools = %v, want %v", livePostSearchTools, want)
	}

	userEvent := &session.Event{
		Type:    session.EventTypeUser,
		Text:    "create a calendar event",
		Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{{Kind: model.PartKindText, Text: &model.TextPart{Text: "create a calendar event"}}}},
	}
	allEvents := append([]*session.Event{userEvent}, liveRun.Events...)
	for _, event := range evalharness.CanonicalEvents(allEvents) {
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
	reloadedScripted := evalharness.NewScriptedModel("reloaded-file-deferred-mcp",
		evalharness.TextStep("ok"),
	)
	reloadedRun, err := evalharness.RunChatScenario(ctx, evalharness.ChatScenario{
		Name:      "reloaded-file-deferred-mcp",
		SessionID: sess.SessionID,
		Prompt:    "continue",
		Model:     reloadedScripted,
		Tools:     tools,
		Events:    loaded,
	})
	if err != nil {
		t.Fatalf("RunChatScenario(reloaded) error = %v", err)
	}
	if got, want := len(reloadedRun.Requests), 1; got != want {
		t.Fatalf("reloaded request count = %d, want %d", got, want)
	}
	if got := requestToolNames(reloadedRun.Requests[0]); !slices.Equal(got, livePostSearchTools) {
		t.Fatalf("reloaded request tools = %v, want runtime post-search tools %v", got, livePostSearchTools)
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
