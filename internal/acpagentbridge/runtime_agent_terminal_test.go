package acpagentbridge

import (
	"testing"

	protocolacp "github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestNormalizeACPStdioTerminalExtensionDoesNotInventTerminalForPlainTool(t *testing.T) {
	notification := normalizeACPStdioTerminalExtension(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCall{
			SessionUpdate: protocolacp.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "LIST",
			Kind:          protocolacp.ToolKindSearch,
			Status:        protocolacp.ToolStatusPending,
		},
	})
	call, ok := notification.Update.(protocolacp.ToolCall)
	if !ok {
		t.Fatalf("update = %T, want ToolCall", notification.Update)
	}
	if _, ok := metautil.TerminalInfo(call.Meta); ok {
		t.Fatalf("meta = %#v, want no terminal_info for plain tool", call.Meta)
	}
	if len(call.Content) != 0 {
		t.Fatalf("content = %#v, want no terminal anchor for plain tool", call.Content)
	}

	status := protocolacp.ToolStatusCompleted
	notification = normalizeACPStdioTerminalExtension(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			RawOutput:     map[string]any{"result": "ok"},
		},
	})
	update, ok := notification.Update.(protocolacp.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", notification.Update)
	}
	if _, ok := metautil.TerminalExit(update.Meta); ok {
		t.Fatalf("meta = %#v, want no terminal_exit for plain tool", update.Meta)
	}
}

func TestNormalizeACPStdioTerminalExtensionKeepsAnchorAndMovesOutputToMeta(t *testing.T) {
	status := protocolacp.ToolStatusCompleted
	notification := normalizeACPStdioTerminalExtension(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			RawOutput:     map[string]any{"exit_code": 0},
			Content: []protocolacp.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "terminal-1",
				Content:    protocolacp.TextContent{Type: "text", Text: "line\n"},
			}},
		},
	})
	update, ok := notification.Update.(protocolacp.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", notification.Update)
	}
	if len(update.Content) != 1 || update.Content[0].Type != "terminal" || update.Content[0].TerminalID != "terminal-1" {
		t.Fatalf("content = %#v, want one terminal anchor", update.Content)
	}
	if update.Content[0].Content != nil {
		t.Fatalf("terminal anchor content = %#v, want empty", update.Content[0].Content)
	}
	if info, ok := metautil.TerminalInfo(update.Meta); !ok || info.TerminalID != "terminal-1" {
		t.Fatalf("terminal_info = %#v, want terminal-1", update.Meta)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.TerminalID != "terminal-1" || output.Data != "line\n" {
		t.Fatalf("terminal_output = %#v, want line output", update.Meta)
	}
	if exit, ok := metautil.TerminalExit(update.Meta); !ok || exit.TerminalID != "terminal-1" {
		t.Fatalf("terminal_exit = %#v, want terminal-1", update.Meta)
	}
}

func TestACPNarrativeFilterDropsFinalTerminalOutputReplay(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	running := protocolacp.ToolStatusInProgress
	completed := protocolacp.ToolStatusCompleted

	first, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &running,
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", "line 1\nline 2\n"),
		},
	})
	if !ok {
		t.Fatal("first output was suppressed")
	}
	firstUpdate := first.Update.(protocolacp.ToolCallUpdate)
	if output, ok := metautil.TerminalOutput(firstUpdate.Meta); !ok || output.Data != "line 1\nline 2\n" {
		t.Fatalf("first terminal_output = %#v, want live output", firstUpdate.Meta)
	}

	final, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &completed,
			RawOutput:     map[string]any{"exit_code": 0},
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", "line 1\nline 2\n"),
		},
	})
	if !ok {
		t.Fatal("final update was suppressed; status and exit should remain visible")
	}
	finalUpdate := final.Update.(protocolacp.ToolCallUpdate)
	if _, ok := metautil.TerminalOutput(finalUpdate.Meta); ok {
		t.Fatalf("final meta = %#v, want terminal_output stripped after replay", finalUpdate.Meta)
	}
	if exit, ok := metautil.TerminalExit(finalUpdate.Meta); !ok || exit.TerminalID != "terminal-1" {
		t.Fatalf("final meta = %#v, want terminal_exit preserved", finalUpdate.Meta)
	}
}

func TestACPNarrativeFilterSendsFinalTerminalOutputSuffix(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	running := protocolacp.ToolStatusInProgress
	completed := protocolacp.ToolStatusCompleted

	if _, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &running,
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", "line 1\n"),
		},
	}); !ok {
		t.Fatal("live output was suppressed")
	}

	final, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &completed,
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", "line 1\nline 2\n"),
		},
	})
	if !ok {
		t.Fatal("final update was suppressed")
	}
	finalUpdate := final.Update.(protocolacp.ToolCallUpdate)
	if output, ok := metautil.TerminalOutput(finalUpdate.Meta); !ok || output.Data != "line 2\n" {
		t.Fatalf("final terminal_output = %#v, want only unsent suffix", finalUpdate.Meta)
	}
}

func TestACPNarrativeFilterKeepsOneShotFinalTerminalOutput(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	completed := protocolacp.ToolStatusCompleted

	final, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &completed,
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", "complete output\n"),
		},
	})
	if !ok {
		t.Fatal("one-shot final update was suppressed")
	}
	finalUpdate := final.Update.(protocolacp.ToolCallUpdate)
	if output, ok := metautil.TerminalOutput(finalUpdate.Meta); !ok || output.Data != "complete output\n" {
		t.Fatalf("final terminal_output = %#v, want one-shot output preserved", finalUpdate.Meta)
	}
}

func TestACPNarrativeFilterDropsTerminalOutputReplayAfterFinal(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	completed := protocolacp.ToolStatusCompleted
	running := protocolacp.ToolStatusInProgress
	outputText := "Hello from RUN_COMMAND!\nMon Jun 29 17:47:54 CST 2026\n"

	if _, ok := filter.FilterNotificationWithFinal(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &completed,
			RawOutput:     map[string]any{"exit_code": 0},
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", outputText),
		},
	}, true); !ok {
		t.Fatal("durable final output was suppressed")
	}

	replay, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &running,
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", outputText),
		},
	})
	if !ok {
		t.Fatal("stream replay update was fully suppressed; terminal anchor should remain visible")
	}
	update := replay.Update.(protocolacp.ToolCallUpdate)
	if _, ok := metautil.TerminalOutput(update.Meta); ok {
		t.Fatalf("stream replay meta = %#v, want terminal_output stripped", update.Meta)
	}
	if info, ok := metautil.TerminalInfo(update.Meta); !ok || info.TerminalID != "terminal-1" {
		t.Fatalf("stream replay meta = %#v, want terminal_info preserved", update.Meta)
	}
}

func TestACPNarrativeFilterDropsFinalAgentMessageReplayByMarker(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	partial := "## 工具演示总结\n\n" +
		"| # | 工具 | 功能 | 状态 |\n" +
		"|---|------|------|\n" +
		"| 1 | **LIST** | 列出目录内容 | ✅ |\n" +
		"| 2 | **GLOB** | 按模式匹配搜索文件路径 | ✅ |\n" +
		"| 3 | **SEARCH** | 搜索文件内容（文本/正则） | ✅ |\n" +
		"| 4 | **READ** | 读取文件内容 | ✅ |\n" +
		"| 5 | **WRITE** | 创建/覆写文件 | ✅ |\n" +
		"| 6 | **PATCH** | 精确文本替换编辑 | ✅ |\n\n" +
		"共 **6 个工具**，其中 **6 个演示成功**。\n"
	finalReplay := "---\n\n## 工具演示总结\n\n" +
		"| # | 工具 | 功能 | 状态 |\n" +
		"|---|------|------|------|\n" +
		"| 1 | **LIST** | 列出目录内容 | ✅ |\n" +
		"| 2 | **GLOB** | 按模式匹配搜索文件路径 | ✅ |\n" +
		"| 3 | **SEARCH** | 搜索文件内容（文本/正则） | ✅ |\n" +
		"| 4 | **READ** | 读取文件内容 | ✅ |\n" +
		"| 5 | **WRITE** | 创建/覆写文件 | ✅ |\n" +
		"| 6 | **PATCH** | 精确文本替换编辑 | ✅ |\n\n" +
		"共 **6 个工具**，其中 **6 个演示成功**。\n"

	if _, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ContentChunk{
			SessionUpdate: protocolacp.UpdateAgentMessage,
			Content:       protocolacp.TextContent{Type: "text", Text: partial},
		},
	}); !ok {
		t.Fatal("partial message was suppressed")
	}
	if _, ok := filter.FilterNotificationWithFinal(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ContentChunk{
			SessionUpdate: protocolacp.UpdateAgentMessage,
			Content:       protocolacp.TextContent{Type: "text", Text: finalReplay},
		},
	}, true); ok {
		t.Fatal("marked final replay was emitted")
	}
}

func TestACPNarrativeFilterKeepsMarkedFinalAgentMessageWithoutLiveChunk(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	final, ok := filter.FilterNotificationWithFinal(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ContentChunk{
			SessionUpdate: protocolacp.UpdateAgentMessage,
			Content:       protocolacp.TextContent{Type: "text", Text: "Final answer."},
		},
	}, true)
	if !ok {
		t.Fatal("one-shot final message was suppressed")
	}
	chunk := final.Update.(protocolacp.ContentChunk)
	if got := acpTextContentText(chunk.Content); got != "Final answer." {
		t.Fatalf("final message = %q, want one-shot final", got)
	}
}

func TestACPNarrativeFilterKeepsMarkedFinalAgentMessageAfterToolUpdateBarrier(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	completed := protocolacp.ToolStatusCompleted
	if _, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ContentChunk{
			SessionUpdate: protocolacp.UpdateAgentMessage,
			Content:       protocolacp.TextContent{Type: "text", Text: "I will run a tool."},
		},
	}); !ok {
		t.Fatal("live message was suppressed")
	}
	if _, ok := filter.FilterNotification(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ToolCallUpdate{
			SessionUpdate: protocolacp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &completed,
		},
	}); !ok {
		t.Fatal("tool update was suppressed")
	}
	final, ok := filter.FilterNotificationWithFinal(protocolacp.SessionNotification{
		SessionID: "session-1",
		Update: protocolacp.ContentChunk{
			SessionUpdate: protocolacp.UpdateAgentMessage,
			Content:       protocolacp.TextContent{Type: "text", Text: "Tool finished."},
		},
	}, true)
	if !ok {
		t.Fatal("post-tool final message was suppressed")
	}
	chunk := final.Update.(protocolacp.ContentChunk)
	if got := acpTextContentText(chunk.Content); got != "Tool finished." {
		t.Fatalf("post-tool final message = %q, want Tool finished.", got)
	}
}
