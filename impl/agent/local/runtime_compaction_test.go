package local

import (
	"context"
	"encoding/json"
	"iter"
	"slices"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/toolsearch"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestRuntimeCompactionInjectsCheckpointAndTrimsOldHistory(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-heuristic")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: build compact runtime. Constraint: do not lose blocker continuity."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack objective"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: provider intermittently returns 529 overloaded_error when histories get too large."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack blocker"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: validate with real e2e tests and tune the compact prompt."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack next"))

	testModel := &contextProbeModel{
		t: t,
		wantMessageContains: []string{
			"CONTEXT CHECKPOINT",
			"build compact runtime",
			"529 overloaded_error",
		},
		wantMessagesOmit: []string{
			"Project objective: build compact runtime",
			"Current blocker: provider intermittently returns 529 overloaded_error",
		},
		replyText: "checkpoint ok",
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
	if testModel.normalCalls != 1 {
		t.Fatalf("normalCalls = %d, want 1", testModel.normalCalls)
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	sawCompact := false
	var compactText string
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			sawCompact = true
			compactText = strings.TrimSpace(session.EventText(event))
			break
		}
	}
	if !sawCompact {
		t.Fatal("expected durable compact event in session history")
	}
	if !strings.Contains(compactText, "build compact runtime") {
		t.Fatalf("compact event text = %q, want compact objective", compactText)
	}
}

func TestRuntimeCompactionUsesModelGeneratedCheckpoint(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-model")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: preserve context continuity during very long coding sessions."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: checkpoint quality drops when summaries become too generic."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: run realistic compact e2e tests and tune the summary prompt."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))

	testModel := &modelCheckpointProbe{
		t: t,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if testModel.compactionCalls == 0 {
		t.Fatal("expected at least one model-backed compaction call")
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var compactText string
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			compactText = strings.TrimSpace(session.EventText(event))
		}
	}
	if !strings.Contains(compactText, "model checkpoint objective") {
		t.Fatalf("compact event text = %q, want model-generated checkpoint objective", compactText)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event in durable history")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatal("expected compact event metadata")
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 || !strings.Contains(strings.ToLower(session.EventText(promptEvents[0])), "model checkpoint objective") {
		t.Fatalf("prompt events after compact = %+v, want pure text checkpoint overlay", promptEvents)
	}
	if promptEvents[0].Message != nil || promptEvents[0].Protocol != nil {
		t.Fatalf("checkpoint overlay should stay pure text, got message=%+v protocol=%+v", promptEvents[0].Message, promptEvents[0].Protocol)
	}
	if data.Revision <= 0 {
		t.Fatalf("compact revision = %d, want > 0", data.Revision)
	}
	if data.ContractVersion != compact.CompactContractVersion {
		t.Fatalf("compact contract version = %d, want %d", data.ContractVersion, compact.CompactContractVersion)
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("compact source event count = %d, want > 0", data.SourceEventCount)
	}
}

func TestRuntimeManualCompactUsesPureTextCheckpointOverlay(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-manual")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: make manual compact preserve context instead of truncating history."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack objective"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: bare compact events cause prompt replay to drop all prior context."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack blocker"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: route manual compact through the model-backed compactor."))

	testModel := &contextProbeModel{
		t: t,
		wantCompactionInputContains: []string{
			"make manual compact preserve context",
		},
		compactBody: `CONTEXT CHECKPOINT

## Current Objective
- make manual compact preserve context instead of truncating history

## User Constraints And Corrections
- keep user-facing compact handoff as structured Markdown, not JSON

## Current Plan And Progress
- manual compact is being aligned with auto compact

## Key Files And Facts
- impl/agent/local/compaction.go:940-1120 owns checkpoint overlay rendering
- license.go:30-100 is a line-index fact that must survive checkpoint overlay

## Validation And Tool Results
- not run yet

## Open Questions Or Risks
- compact events without checkpoint overlay must not be emitted

## Next Actions
1. route manual compact through the model-backed compactor`,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			SegmentTokenBudget: 80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Compact(context.Background(), CompactRequest{
		SessionRef: activeSession.SessionRef,
		Model:      testModel,
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("Compact() did not compact")
	}
	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
	if testModel.normalCalls != 0 {
		t.Fatalf("normalCalls = %d, want 0", testModel.normalCalls)
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("compact event missing structured metadata: %+v", compactEvent.Meta)
	}
	if data.Trigger != "manual" {
		t.Fatalf("compact trigger = %q, want manual", data.Trigger)
	}
	if data.ContractVersion != compact.CompactContractVersion || data.SourceEventCount == 0 {
		t.Fatalf("compact metadata = version:%d source:%d, want contract metadata", data.ContractVersion, data.SourceEventCount)
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 {
		t.Fatal("prompt events empty after manual compact")
	}
	promptText := strings.Join(eventTextsForTest(promptEvents), "\n")
	if strings.Contains(promptText, "Project objective: make manual compact preserve context instead of truncating history.") {
		t.Fatalf("prompt events still replay raw pre-compact history: %+v", promptEvents)
	}
	for _, needle := range []string{
		"## Current Objective",
		"## Key Files And Facts",
		"license.go:30-100",
	} {
		if !strings.Contains(promptText, needle) {
			t.Fatalf("prompt events missing raw markdown checkpoint detail %q: %q", needle, promptText)
		}
	}
	if strings.Contains(promptText, "Objective: make manual compact preserve context instead of truncating history") {
		t.Fatalf("prompt events reconstructed labeled checkpoint fields instead of preserving markdown: %q", promptText)
	}
}

func TestRuntimeCompactionPreservesDeferredMCPToolVisibility(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-tool-search")
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
	}
	searchTool := toolsearch.New([]tool.Tool{mcpTool})
	if searchTool == nil {
		t.Fatal("toolsearch.New(MCP tool) = nil")
	}
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Find the calendar event tool."))
	appendTestEvent(t, sessions, activeSession.SessionRef, &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "call-search",
			Name:   tool.ToolSearchToolName,
			Status: "completed",
			Output: toolSearchOutputMapForTest(t, mcpTool.Definition()),
		},
	})
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("Calendar tool is available."))

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			SegmentTokenBudget: 80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	compactModel := &contextProbeModel{
		t: t,
		compactBody: `CONTEXT CHECKPOINT

## Current Objective
- use the discovered calendar tool

## Next Actions
1. continue with the calendar MCP tool`,
	}
	result, err := runtime.Compact(context.Background(), CompactRequest{
		SessionRef: activeSession.SessionRef,
		Model:      compactModel,
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("Compact() did not compact")
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("compact event missing metadata: %+v", compactEvent.Meta)
	}
	if !slices.Equal(data.DiscoveredTools, []string{mcpToolName}) {
		t.Fatalf("compact discovered tools = %v, want %v", data.DiscoveredTools, []string{mcpToolName})
	}

	probe := &toolListProbeModel{}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: probe,
			Tools: []tool.Tool{searchTool, mcpTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if got, want := toolNamesFromRequestForTest(probe.last), []string{tool.ToolSearchToolName, mcpToolName}; !slices.Equal(got, want) {
		t.Fatalf("post-compact request tools = %v, want %v", got, want)
	}
}

func TestRuntimeManualCompactIncludesConfirmedUserMessage(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-user-confirm")
	oldCompact := buildCompactEvent(activeSession, `CONTEXT CHECKPOINT

## Current Objective
- Remove gm_license legacy behavior

## Next Actions
1. wait for explicit implementation approval`, compact.CompactEventData{
		ContractVersion: compact.CompactContractVersion,
		Generator:       "model_markdown",
		Trigger:         "manual",
	})
	appendTestEvent(t, sessions, activeSession.SessionRef, oldCompact)
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("Plan prepared. Next action: wait for user confirmation before writing code."))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("开始实现"))

	testModel := &contextProbeModel{
		t: t,
		wantCompactionInputContains: []string{
			"# Existing Compact Checkpoint (reference only)",
			"wait for user confirmation before writing code",
			"开始实现",
		},
		compactBody: `CONTEXT CHECKPOINT

## Current Objective
- Implement the compact optimization now.

## User Constraints And Corrections
- 用户已经发送“开始实现”，下一步应立即实现，不再等待确认。

## Current Plan And Progress
- Plan was prepared before compact.

## Next Actions
1. Start editing impl/agent/local/compaction.go.`,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			SegmentTokenBudget: 80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Compact(context.Background(), CompactRequest{
		SessionRef: activeSession.SessionRef,
		Model:      testModel,
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("Compact() did not compact")
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	promptText := strings.Join(eventTextsForTest(compact.PromptEventsFromLatestCompact(loaded.Events)), "\n")
	for _, needle := range []string{"开始实现", "下一步应立即实现", "Start editing impl/agent/local/compaction.go"} {
		if !strings.Contains(promptText, needle) {
			t.Fatalf("prompt after compact missing %q: %q", needle, promptText)
		}
	}
	if strings.Contains(promptText, "Remove gm_license legacy behavior") {
		t.Fatalf("prompt after compact retained stale old checkpoint objective: %q", promptText)
	}
}

func TestRuntimeCompactionReplaysFromEventsAfterReload(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-compact-replay" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-compact-replay",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: replay compacted history strictly from append-only events."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: raw transcript replay grows too large under long sessions."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: verify reload from file-backed events only."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))

	runtime1, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New(runtime1) error = %v", err)
	}

	result1, err := runtime1.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name: "chat",
			Model: &contextProbeModel{
				t:         t,
				replyText: "seed ok",
				compactBody: `CONTEXT CHECKPOINT

Objective: replay compacted history strictly from append-only events
Blocker: raw transcript replay grows too large under long sessions
Next action: verify reload from file-backed events only

## Current Progress
- compact summary persisted as a durable event

## Next Actions
1. verify reload from file-backed events only`,
			},
		},
	})
	if err != nil {
		t.Fatalf("runtime1.Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result1.Handle); err != nil {
		t.Fatalf("runtime1 runner error = %v", err)
	}

	reopenedSessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	reopenedState, err := reopenedSessions.SnapshotState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if len(reopenedState) != 0 {
		t.Fatalf("reopened state = %v, want compact replay to not depend on session state", reopenedState)
	}
	runtime2, err := New(Config{
		Sessions: reopenedSessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.95,
			ForceWatermarkRatio:        0.99,
			DefaultContextWindowTokens: 4096,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New(runtime2) error = %v", err)
	}

	replayModel := &contextProbeModel{
		t: t,
		wantMessageContains: []string{
			"CONTEXT CHECKPOINT",
			"replay compacted history strictly from append-only events",
			"verify reload from file-backed events only",
		},
		replyText: "replay ok",
	}
	result, err := runtime2.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue after reload",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: replayModel,
		},
	})
	if err != nil {
		t.Fatalf("runtime2.Run() error = %v", err)
	}
	events, seqErr := drainRunnerEvents(t, result.Handle)
	if seqErr != nil {
		t.Fatalf("runner error = %v", seqErr)
	}
	finalText := lastAssistantText(events)
	if finalText != "replay ok" {
		t.Fatalf("final assistant text = %q, want %q", finalText, "replay ok")
	}
}

func TestSnapshotUsageUsesPromptBaselinePlusReplayDelta(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 32000,
		ReserveOutputTokens:        5000,
		SafetyMarginTokens:         2048,
	})}
	assistant := assistantEvent("Short visible assistant reply.")
	assistant.ID = "assistant-1"
	assistant.Meta = map[string]any{
		"provider":          "stub",
		"model":             "test-model",
		"prompt_tokens":     120,
		"completion_tokens": 900,
		"total_tokens":      1020,
	}
	followUp := userTextEvent("Follow up with the latest status update.")
	followUp.ID = "user-2"
	events := []*session.Event{assistant, followUp}

	usage := compactor.snapshotUsage(compact.Request{}, events)
	want := 120 + estimatePromptEventTokens(assistant) + estimatePromptEventTokens(followUp)
	if usage.TotalTokens != want {
		t.Fatalf("usage.TotalTokens = %d, want %d", usage.TotalTokens, want)
	}
	if usage.Source != compact.UsageSourceProvider {
		t.Fatalf("usage.Source = %q, want provider", usage.Source)
	}
	if usage.AsOfEventID != "assistant-1" {
		t.Fatalf("usage.AsOfEventID = %q, want %q", usage.AsOfEventID, "assistant-1")
	}
}

func TestSnapshotUsageTotalOnlyFallbackDoesNotDoubleCountSnapshotGroup(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 32000,
	})}
	assistant := assistantEvent("Assistant reply already captured in transcript.")
	assistant.ID = "assistant-1"
	assistant.Meta = map[string]any{
		"provider":     "stub",
		"model":        "test-model",
		"total_tokens": 400,
	}
	followUp := userTextEvent("User turn added after the provider snapshot.")
	followUp.ID = "user-2"
	events := []*session.Event{assistant, followUp}

	usage := compactor.snapshotUsage(compact.Request{}, events)
	want := 400 + estimatePromptEventTokens(followUp)
	if usage.TotalTokens != want {
		t.Fatalf("usage.TotalTokens = %d, want %d", usage.TotalTokens, want)
	}
}

func TestSnapshotUsageClampsEffectiveBudgetForSmallWindows(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 2048,
		ReserveOutputTokens:        5000,
		SafetyMarginTokens:         2048,
	})}

	usage := compactor.snapshotUsage(compact.Request{}, []*session.Event{userTextEvent("small window probe")})
	if usage.EffectiveInputBudget != 1280 {
		t.Fatalf("usage.EffectiveInputBudget = %d, want %d", usage.EffectiveInputBudget, 1280)
	}
	if usage.EffectiveInputBudget <= 0 || usage.EffectiveInputBudget > usage.ContextWindowTokens {
		t.Fatalf("effective input budget out of range: %+v", usage)
	}
}

func TestSnapshotUsagePreservesConfiguredMarginsForLongWindows(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 200000,
		ReserveOutputTokens:        5000,
		SafetyMarginTokens:         2048,
	})}

	usage := compactor.snapshotUsage(compact.Request{}, []*session.Event{userTextEvent("long window probe")})
	if usage.EffectiveInputBudget != 192952 {
		t.Fatalf("usage.EffectiveInputBudget = %d, want %d", usage.EffectiveInputBudget, 192952)
	}
}

func TestPrepareCompactionFitsPendingInputWithinBudget(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             0.6,
		ForceWatermarkRatio:        0.75,
		DefaultContextWindowTokens: 192,
		ReserveOutputTokens:        32,
		SafetyMarginTokens:         16,
		SegmentTokenBudget:         80,
	})}
	events := []*session.Event{
		userTextEvent(strings.Repeat("Objective continuity detail. ", 8)),
		assistantEvent("ack"),
		userTextEvent(strings.Repeat("Most recent blocker and progress detail. ", 8)),
	}
	pending := userTextEvent(strings.Repeat("New user turn that must still fit after compaction. ", 6))

	result, err := compactor.Prepare(context.Background(), compact.Request{
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis",
				UserID:  "user-1",
			},
		},
		Events:        events,
		PendingEvents: []*session.Event{pending},
		Model: staticModel{text: `Objective: preserve compact budget
Blocker: pre-turn prompt is near the limit
Next action: fit the pending user turn inside the compacted prompt

- keep only the minimal continuity handoff`},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("expected compaction to trigger")
	}
	if result.Usage.TotalTokens > result.Usage.EffectiveInputBudget {
		t.Fatalf("usage.TotalTokens = %d, want <= effective budget %d", result.Usage.TotalTokens, result.Usage.EffectiveInputBudget)
	}
	data, ok := compact.CompactEventDataFromEvent(result.CompactEvent)
	if !ok {
		t.Fatal("expected compact event data")
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("source event count = %d, want > 0", data.SourceEventCount)
	}
}

func TestRuntimeCompactionIgnoresStateOnlyPlanSnapshot(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-state-omit")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Objective: keep compaction event-only."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Blocker: runtime state can drift away from durable events."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: compact only from canonical events and verify no state leakage."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))

	if err := sessions.UpdateState(context.Background(), activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		if state == nil {
			state = map[string]any{}
		}
		state["plan"] = map[string]any{
			"version": 1,
			"entries": []any{
				map[string]any{
					"content": "state-only plan item that must never leak into compaction",
					"status":  "in_progress",
				},
			},
		}
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	testModel := &contextProbeModel{
		t: t,
		wantCompactionInputContains: []string{
			"Objective: keep compaction event-only.",
		},
		wantCompactionInputOmit: []string{
			"Current runtime state:",
			"state-only plan item that must never leak into compaction",
		},
		replyText: "ok",
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
}

func TestRenderCompactionEventIncludesPlanEntries(t *testing.T) {
	t.Parallel()

	event := &session.Event{
		Type:       session.EventTypePlan,
		Visibility: session.VisibilityCanonical,
		Text:       "execution plan refreshed",
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypePlan),
			Plan: &session.ProtocolPlan{
				Entries: []session.ProtocolPlanEntry{
					{Content: "run provider compact e2e", Status: "in_progress"},
					{Content: "verify append-only replay", Status: "pending"},
					{Content: "preserve plan item three", Status: "pending"},
					{Content: "preserve plan item four", Status: "pending"},
					{Content: "preserve plan item five", Status: "pending"},
					{Content: "preserve plan item six", Status: "pending"},
				},
			},
		},
	}

	got := renderCompactionEvent(event)
	for _, needle := range []string{
		"## Plan Update",
		"execution plan refreshed",
		"- [in_progress] run provider compact e2e",
		"- [pending] verify append-only replay",
		"- [pending] preserve plan item six",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("renderCompactionEvent() = %q, want substring %q", got, needle)
		}
	}
}

func TestCompactableEventsIgnoreReplacementOverlayHistory(t *testing.T) {
	t.Parallel()

	retainedMsg := model.NewTextMessage(model.RoleUser, "Retained user text from the previous compact.")
	overlay := &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityOverlay,
		Message:    &retainedMsg,
		Text:       retainedMsg.TextContent(),
	}
	canonical := userTextEvent("Fresh canonical user event after the latest compact.")
	events := []*session.Event{
		overlay,
		canonical,
	}

	got := compactableEvents(events)
	if len(got) != 1 {
		t.Fatalf("compactableEvents() count = %d, want 1 (%v)", len(got), got)
	}
	if text := eventTextForCompaction(got[0]); text != "Fresh canonical user event after the latest compact." {
		t.Fatalf("compactable event text = %q, want fresh canonical event", text)
	}
}

func TestRenderCompactionEventFallsBackToMessageText(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleAssistant, "message-only assistant text")
	event := &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
	}

	got := renderCompactionEvent(event)
	if !strings.Contains(got, "message-only assistant text") {
		t.Fatalf("renderCompactionEvent() = %q, want message text fallback", got)
	}
}

func TestRuntimeRecoversFromContextOverflowByCompactingMidTurn(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-overflow")
	testModel := &overflowRecoveryModel{t: t}
	targetTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart([]byte(`{"value":"pong"}`)),
				},
			}, nil
		},
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.95,
			ForceWatermarkRatio:        0.99,
			DefaultContextWindowTokens: 128,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "Use ECHO and then finish.",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{targetTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(session.EventText(event))
		}
	}
	if finalText != "recovered after compact" {
		t.Fatalf("finalText = %q, want %q", finalText, "recovered after compact")
	}
	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
	if !testModel.sawCheckpointOnRetry {
		t.Fatal("expected retry to see compact checkpoint with tool result continuity")
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	sawCompact := false
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			sawCompact = true
			if !strings.Contains(strings.ToLower(session.EventText(event)), "echo tool result completed") {
				t.Fatalf("compact event text = %q, want retained tool result summary", session.EventText(event))
			}
		}
	}
	if !sawCompact {
		t.Fatal("expected compact event after overflow recovery")
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected latest compact event")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("compact metadata missing compact payload: %+v", compactEvent.Meta)
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("compact source event count = %d, want > 0", data.SourceEventCount)
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 || !strings.Contains(strings.ToLower(session.EventText(promptEvents[0])), "echo tool result completed") {
		t.Fatalf("prompt events after compact = %+v, want tool result continuity in checkpoint overlay", promptEvents)
	}
}

type toolListProbeModel struct {
	last model.Request
}

func (m *toolListProbeModel) Name() string { return "tool-list-probe" }

func (m *toolListProbeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req != nil {
		m.last = *model.CloneRequest(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "ok"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func toolSearchOutputMapForTest(t *testing.T, definitions ...tool.Definition) map[string]any {
	t.Helper()
	raw, err := json.Marshal(tool.NewToolSearchResult(definitions))
	if err != nil {
		t.Fatalf("marshal tool_search result: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal tool_search result: %v", err)
	}
	return out
}

func toolNamesFromRequestForTest(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		if spec.Function != nil {
			out = append(out, spec.Function.Name)
		}
	}
	return out
}
