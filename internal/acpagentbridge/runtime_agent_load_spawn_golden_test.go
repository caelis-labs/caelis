package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestRuntimeAgentACPSessionLoadSpawnGolden(t *testing.T) {
	agent, sessions := newRuntimeAgentWithConfig(t, runtimeacp.Config{})
	ctx := context.Background()
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "session-load-spawn",
		Workspace: session.WorkspaceRef{
			Key: "/tmp/acp-load-spawn",
			CWD: "/tmp/acp-load-spawn",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	alphaFinal := "# Alpha final\n\n- exact markdown\n"
	betaFinal := "Beta final."
	events := []*session.Event{
		loadGoldenToolEvent(session.EventTypeToolCall, "spawn-alpha", "Spawn", "pending",
			map[string]any{"agent": "breeze", "prompt": "alpha work"}, nil),
		loadGoldenToolEvent(session.EventTypeToolCall, "spawn-beta", "Spawn", "pending",
			map[string]any{"agent": "orbit", "prompt": "beta work"}, nil),
		loadGoldenToolEvent(session.EventTypeToolResult, "spawn-alpha", "Spawn", "running",
			nil, map[string]any{
				"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
				"state": "running", "target_kind": "subagent",
			}),
		loadGoldenToolEvent(session.EventTypeToolResult, "spawn-beta", "Spawn", "running",
			nil, map[string]any{
				"handle": "beta", "parent_call": "spawn-beta", "parent_tool": "Spawn",
				"state": "running", "target_kind": "subagent",
			}),
		loadGoldenNarrativeToolCall(
			"wait-one", "Task", `{"action":"wait","handle":"alpha,beta"}`,
			"The sub-agents are running. I will wait for both results.",
			"Waiting for alpha and beta.",
			map[string]any{"action": "wait", "handle": "alpha,beta"},
		),
		loadGoldenToolEvent(session.EventTypeToolResult, "wait-one", "Task", "completed",
			map[string]any{"action": "wait", "handle": "alpha,beta"}, map[string]any{
				"action": "wait",
				"tasks": []any{
					map[string]any{
						"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "final_message": alphaFinal,
					},
					map[string]any{
						"handle": "beta", "parent_call": "spawn-beta", "parent_tool": "Spawn",
						"state": "running", "target_kind": "subagent",
					},
					map[string]any{
						"handle": "command", "parent_call": "command-call", "parent_tool": "RunCommand",
						"state": "completed", "target_kind": "command",
					},
				},
			}),
		loadGoldenNarrativeToolCall(
			"wait-two", "Task", `{"action":"wait","handle":"beta"}`,
			"Beta is still running after the first wait. I will wait again.",
			"Continuing to wait for beta.",
			map[string]any{"action": "wait", "handle": "beta"},
		),
		loadGoldenToolEvent(session.EventTypeToolResult, "wait-two", "Task", "completed",
			map[string]any{"action": "wait", "handle": "beta"}, map[string]any{
				"action": "wait",
				"tasks": []any{
					map[string]any{
						"handle": "beta", "parent_call": "spawn-beta", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "final_message": betaFinal,
					},
					map[string]any{
						"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "final_message": "duplicate alpha",
					},
				},
			}),
		loadGoldenNarrativeToolCall(
			"read-report", "Read", `{"path":"report.md"}`,
			"Both sub-agents completed. I will verify the generated report.",
			"Reading the report now.",
			map[string]any{"path": "report.md"},
		),
		loadGoldenToolEvent(session.EventTypeToolResult, "read-report", "Read", "completed",
			map[string]any{"path": "report.md"}, map[string]any{"result": "verified"}),
	}
	for _, event := range events {
		if _, err := sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: active.SessionRef,
			Event:      event,
		}); err != nil {
			t.Fatalf("AppendEvent(%s/%s) error = %v", event.Tool.Name, event.Tool.ID, err)
		}
	}

	callbacks := &recordingPromptCallbacks{}
	if _, err := agent.LoadSession(ctx, acp.LoadSessionRequest{
		SessionID: active.SessionID,
		CWD:       active.CWD,
	}, callbacks); err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}

	wantFinal := map[string]string{
		"spawn-alpha": alphaFinal,
		"spawn-beta":  betaFinal,
	}
	seenFinal := map[string]int{}
	for _, notification := range callbacks.notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok || update.Status == nil || *update.Status != acp.ToolStatusCompleted {
			continue
		}
		wantText, isSpawn := wantFinal[update.ToolCallID]
		if !isSpawn {
			continue
		}
		seenFinal[update.ToolCallID]++
		output, ok := metautil.TerminalOutput(update.Meta)
		if !ok || output.TerminalID != update.ToolCallID || output.Data != wantText {
			t.Fatalf("Spawn %s terminal_output = %#v, want exact FinalMessage %q", update.ToolCallID, update.Meta, wantText)
		}
		if exit, ok := metautil.TerminalExit(update.Meta); !ok || exit.TerminalID != update.ToolCallID {
			t.Fatalf("Spawn %s terminal_exit = %#v, want matching terminal exit", update.ToolCallID, update.Meta)
		}
	}
	for toolCallID := range wantFinal {
		if seenFinal[toolCallID] != 1 {
			t.Fatalf("Spawn %s completed updates = %d, want exactly one", toolCallID, seenFinal[toolCallID])
		}
	}
	assertLoadGoldenToolNarrativeSiblings(t, callbacks.notifications, []loadGoldenNarrativeExpectation{
		{
			ToolCallID: "wait-one",
			Reasoning:  "The sub-agents are running. I will wait for both results.",
			Assistant:  "Waiting for alpha and beta.",
		},
		{
			ToolCallID: "wait-two",
			Reasoning:  "Beta is still running after the first wait. I will wait again.",
			Assistant:  "Continuing to wait for beta.",
		},
		{
			ToolCallID: "read-report",
			Reasoning:  "Both sub-agents completed. I will verify the generated report.",
			Assistant:  "Reading the report now.",
		},
	})

	got, err := json.MarshalIndent(callbacks.notifications, "", "  ")
	if err != nil {
		t.Fatalf("marshal ACP session/load notifications: %v", err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/golden/acp_stdio_session_load_spawn.golden.json")
	if err != nil {
		t.Fatalf("read ACP session/load Spawn golden: %v\n--- got ---\n%s", err, got)
	}
	if string(got) != string(want) {
		t.Fatalf("ACP session/load Spawn projection changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

type loadGoldenNarrativeExpectation struct {
	ToolCallID string
	Reasoning  string
	Assistant  string
}

func assertLoadGoldenToolNarrativeSiblings(
	t *testing.T,
	notifications []acp.SessionNotification,
	expectations []loadGoldenNarrativeExpectation,
) {
	t.Helper()

	for _, expectation := range expectations {
		callIndexes := make([]int, 0, 1)
		for index, notification := range notifications {
			call, ok := notification.Update.(acp.ToolCall)
			if ok && call.ToolCallID == expectation.ToolCallID {
				callIndexes = append(callIndexes, index)
			}
		}
		if len(callIndexes) != 1 {
			t.Fatalf("tool_call %s indexes = %v, want exactly one", expectation.ToolCallID, callIndexes)
		}
		callIndex := callIndexes[0]
		if callIndex < 2 {
			t.Fatalf("tool_call %s index = %d, want reasoning and assistant siblings first", expectation.ToolCallID, callIndex)
		}
		assertLoadGoldenContentChunk(t, notifications[callIndex-2], acp.UpdateAgentThought, expectation.Reasoning)
		assertLoadGoldenContentChunk(t, notifications[callIndex-1], acp.UpdateAgentMessage, expectation.Assistant)
	}
}

func assertLoadGoldenContentChunk(t *testing.T, notification acp.SessionNotification, kind string, text string) {
	t.Helper()

	chunk, ok := notification.Update.(acp.ContentChunk)
	if !ok || chunk.SessionUpdate != kind {
		t.Fatalf("notification update = %#v, want %s content chunk", notification.Update, kind)
	}
	content, ok := chunk.Content.(acp.TextContent)
	if !ok || content.Text != text {
		t.Fatalf("%s content = %#v, want exact text %q", kind, chunk.Content, text)
	}
}

func loadGoldenNarrativeToolCall(
	toolCallID string,
	toolName string,
	toolArgs string,
	reasoning string,
	assistant string,
	input map[string]any,
) *session.Event {
	message := model.MessageFromAssistantParts(assistant, reasoning, []model.ToolCall{{
		ID:   toolCallID,
		Name: toolName,
		Args: toolArgs,
	}})
	event := loadGoldenToolEvent(session.EventTypeToolCall, toolCallID, toolName, "pending", input, nil)
	event.Message = &message
	return event
}

func loadGoldenToolEvent(
	eventType session.EventType,
	toolCallID string,
	toolName string,
	status string,
	input map[string]any,
	output map[string]any,
) *session.Event {
	return &session.Event{
		Type:       eventType,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     toolCallID,
			Name:   toolName,
			Status: status,
			Input:  input,
			Output: output,
		},
	}
}
