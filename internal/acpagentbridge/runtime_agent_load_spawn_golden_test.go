package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

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
		loadGoldenToolEvent(session.EventTypeToolCall, "wait-one", "Task", "pending",
			map[string]any{"action": "wait", "handle": "alpha,beta"}, nil),
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
		loadGoldenToolEvent(session.EventTypeToolCall, "wait-two", "Task", "pending",
			map[string]any{"action": "wait", "handle": "beta"}, nil),
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
