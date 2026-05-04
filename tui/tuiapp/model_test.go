package tuiapp

import (
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	"github.com/OnslaughtSnail/caelis/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func TestModelViewShowsWelcomeCard(t *testing.T) {
	model := NewModel(Config{
		AppName:         "CAELIS",
		Version:         "dev",
		Workspace:       "/tmp/workspace",
		ModelAlias:      "minimax/MiniMax-M1",
		ShowWelcomeCard: true,
		Commands:        DefaultCommands(),
		Wizards:         DefaultWizards(),
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := updated.View().Content
	if !strings.Contains(view, "CAELIS") {
		t.Fatalf("expected CAELIS in welcome card, got %q", view)
	}
	// The transplanted legacy TUI should show model info and the workspace
	if !strings.Contains(view, "/tmp/workspace") {
		t.Fatalf("expected workspace in view, got %q", view)
	}
}

func TestWelcomeCardPrefersDynamicStatusModel(t *testing.T) {
	model := NewModel(Config{
		AppName:         "CAELIS",
		Version:         "dev",
		Workspace:       "/tmp/workspace",
		ModelAlias:      "",
		ShowWelcomeCard: true,
		Commands:        DefaultCommands(),
		Wizards:         DefaultWizards(),
		RefreshStatus: func() (string, string) {
			return "deepseek/deepseek-v4-flash", ""
		},
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := updated.View().Content
	if !strings.Contains(view, "deepseek/deepseek-v4-flash") {
		t.Fatalf("expected dynamic model in welcome card, got %q", view)
	}
	if strings.Contains(view, "not configured (/connect)") {
		t.Fatalf("welcome card still shows not configured: %q", view)
	}
}

func TestWelcomeCardUpdatesWhenStatusChanges(t *testing.T) {
	model := NewModel(Config{
		AppName:         "CAELIS",
		Version:         "dev",
		Workspace:       "/tmp/workspace",
		ModelAlias:      "",
		ShowWelcomeCard: true,
		Commands:        DefaultCommands(),
		Wizards:         DefaultWizards(),
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m := updated.(*Model)
	m.handleSetStatusMsg(SetStatusMsg{
		Model:     "deepseek/deepseek-v4-flash",
		Workspace: "/tmp/workspace",
	})
	view := m.View().Content
	if !strings.Contains(view, "deepseek/deepseek-v4-flash") {
		t.Fatalf("expected updated model in welcome card, got %q", view)
	}
	if strings.Contains(view, "not configured (/connect)") {
		t.Fatalf("welcome card still shows not configured after status update: %q", view)
	}
}

func TestModelViewDoesNotCallModeLabelCallback(t *testing.T) {
	model := NewModel(Config{
		ModeLabel: func() string {
			return "plan"
		},
	})
	model.cfg.ModeLabel = func() string {
		panic("View must not call ModeLabel")
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := updated.View().Content
	if !strings.Contains(view, "plan") {
		t.Fatalf("expected cached mode label in view, got %q", view)
	}
}

func TestAutoThemeRequestsBackgroundOnFocusAndResize(t *testing.T) {
	t.Setenv("CAELIS_THEME", "auto")
	model := NewModel(Config{})

	_, focusCmd := model.Update(tea.FocusMsg{})
	if focusCmd == nil {
		t.Fatal("expected focus to request background color in auto theme")
	}

	_, resizeCmd := model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if resizeCmd == nil {
		t.Fatal("expected resize to request background color in auto theme")
	}
}

func TestExplicitThemeDoesNotRequestBackgroundOnFocus(t *testing.T) {
	t.Setenv("CAELIS_THEME", "light")
	model := NewModel(Config{})

	_, cmd := model.Update(tea.FocusMsg{})
	if cmd != nil {
		t.Fatal("expected explicit theme to skip background color request")
	}
}

func TestAutoThemeAppliesBackgroundColorMessage(t *testing.T) {
	t.Setenv("CAELIS_THEME", "auto")
	model := NewModel(Config{})
	model.applyTheme(tuikit.ResolveThemeWithState(true, false, model.colorProfile))

	updated, _ := model.Update(tea.BackgroundColorMsg{Color: color.White})
	m := updated.(*Model)
	if m.theme.IsDark {
		t.Fatal("expected light theme after light background color message")
	}
	if got := m.theme.Name; got != "light" {
		t.Fatalf("theme name = %q, want light", got)
	}
}

func TestSetStatusMsgClearsModeLabel(t *testing.T) {
	model := NewModel(Config{})
	model.handleSetStatusMsg(SetStatusMsg{ModeLabel: "plan"})
	model.handleSetStatusMsg(SetStatusMsg{ModeLabel: ""})

	if got := model.modeLabel(); got != "" {
		t.Fatalf("modeLabel() = %q, want empty after status clears it", got)
	}
}

func TestStatusTickNoChangeDoesNotFullSyncViewport(t *testing.T) {
	model := NewModel(Config{
		Workspace: "/tmp/workspace",
		RefreshWorkspace: func() string {
			return "/tmp/workspace"
		},
		RefreshStatus: func() (string, string) {
			return "gpt-4o", "12/128k(0%)"
		},
	})
	model.viewport.SetWidth(80)
	model.viewport.SetHeight(20)
	model.syncViewportContent()

	versionBefore := model.viewportContentVersion
	fullSyncsBefore := model.diag.ViewportFullSyncs

	model.handleStatusTickMsg()

	if got := model.viewportContentVersion; got != versionBefore {
		t.Fatalf("viewportContentVersion = %d, want unchanged %d", got, versionBefore)
	}
	if got := model.diag.ViewportFullSyncs; got != fullSyncsBefore {
		t.Fatalf("ViewportFullSyncs = %d, want unchanged %d", got, fullSyncsBefore)
	}
}

func TestMainColumnUsesFullTerminalWidth(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m := updated.(*Model)

	if got := m.mainColumnX(); got != 0 {
		t.Fatalf("mainColumnX() = %d, want 0", got)
	}
	if got := m.mainColumnWidth(); got != 200 {
		t.Fatalf("mainColumnWidth() = %d, want terminal width 200", got)
	}
	wantViewport := 200 - tuikit.GutterNarrative - m.viewportScrollbarWidth()
	if got := m.viewportContentWidth(); got != wantViewport {
		t.Fatalf("viewportContentWidth() = %d, want %d", got, wantViewport)
	}
	if got := m.viewport.Width(); got != wantViewport {
		t.Fatalf("viewport.Width() = %d, want %d", got, wantViewport)
	}
}

func TestBTWCommandIsHiddenByDefault(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	if got := model.submissionModeForLine("/btw summarize that"); got != SubmissionModeDefault {
		t.Fatalf("submissionModeForLine(/btw ...) = %q, want default hidden-command handling", got)
	}
}

func TestUnknownSlashUserMessageUsesNormalPromptBehavior(t *testing.T) {
	model := NewModel(Config{
		Commands:    DefaultCommands(),
		ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} },
	})
	line := "/rbac/inner/workflow/switch Query 参数"

	_, cmd := model.submitLine(line)
	if cmd == nil {
		t.Fatal("submitLine() command = nil, want ExecuteLine command")
	}
	if !model.showTurnDivider {
		t.Fatal("showTurnDivider = false, want normal user prompt divider")
	}
	if len(model.history) != 1 || model.history[0] != line {
		t.Fatalf("history = %#v, want unknown slash user message recorded", model.history)
	}
}

func TestKnownSlashCommandKeepsControlPromptBehavior(t *testing.T) {
	model := NewModel(Config{
		Commands:    DefaultCommands(),
		ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} },
	})

	_, cmd := model.submitLine("/help")
	if cmd == nil {
		t.Fatal("submitLine() command = nil, want ExecuteLine command")
	}

	if model.showTurnDivider {
		t.Fatal("showTurnDivider = true, want control command to suppress user prompt divider")
	}
	if len(model.history) != 0 {
		t.Fatalf("history = %#v, want control command omitted", model.history)
	}
}

func TestDynamicAgentSlashCommandUsesNormalTurnBehavior(t *testing.T) {
	model := NewModel(Config{
		Commands:    append(DefaultCommands(), "codex"),
		ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} },
	})
	line := "/codex 查询一下上海今天的天气"

	_, cmd := model.submitLine(line)
	if cmd == nil {
		t.Fatal("submitLine() command = nil, want ExecuteLine command")
	}
	if !model.showTurnDivider {
		t.Fatal("showTurnDivider = false, want agent slash prompt to behave like a normal user turn")
	}
	if len(model.history) != 1 || model.history[0] != line {
		t.Fatalf("history = %#v, want agent slash prompt recorded", model.history)
	}
}

func TestTaskResultDividerRendersImmediatelyWhenViewportHasDirtyBlock(t *testing.T) {
	model := NewModel(Config{NoColor: true})
	model.viewport.SetWidth(72)
	model.viewport.SetHeight(20)

	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events, SubagentEvent{Kind: SEAssistant, Text: "done", Done: true})
	model.doc.Append(block)
	model.activeMainACPTurnID = block.BlockID()
	model.markViewportStructureDirty()
	model.syncViewportContent()

	model.showTurnDivider = true
	model.runStartedAt = time.Now().Add(-3 * time.Second)
	model.markViewportBlockDirty(block.BlockID())

	updated, _ := model.Update(TaskResultMsg{})
	model = updated.(*Model)
	model.syncViewportContent()

	joined := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(joined, "─") || (!strings.Contains(joined, "3.") && !strings.Contains(joined, "3s")) {
		t.Fatalf("viewport lines = %#v, want immediate completed-turn divider", model.viewportPlainLines)
	}
}

func TestReasoningAndAnswerBlocksRemainAdjacentAndIndependent(t *testing.T) {
	model := NewModel(Config{})

	if _, cmd := model.handleStreamBlock("reasoning", "assistant", "thinking...", false); cmd != nil {
		_ = cmd
	}
	if _, cmd := model.handleStreamBlock("reasoning", "assistant", "thinking...", true); cmd != nil {
		_ = cmd
	}
	if model.activeReasoningID != "" {
		t.Fatalf("activeReasoningID = %q, want finalized reasoning block", model.activeReasoningID)
	}
	if got := model.doc.Len(); got != 1 {
		t.Fatalf("doc.Len() after reasoning = %d, want 1", got)
	}
	reasoning, ok := model.doc.Blocks()[0].(*ReasoningBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ReasoningBlock", model.doc.Blocks()[0])
	}
	if reasoning.Streaming {
		t.Fatal("reasoning block should stay in transcript but stop streaming after final")
	}
	if strings.TrimSpace(reasoning.Raw) != "thinking..." {
		t.Fatalf("reasoning raw = %q, want thinking...", reasoning.Raw)
	}

	if _, cmd := model.handleStreamBlock("answer", "assistant", "final answer", true); cmd != nil {
		_ = cmd
	}
	if got := model.doc.Len(); got != 2 {
		t.Fatalf("doc.Len() after answer = %d, want 2", got)
	}
	if _, ok := model.doc.Blocks()[0].(*ReasoningBlock); !ok {
		t.Fatalf("first block = %#v, want ReasoningBlock", model.doc.Blocks()[0])
	}
	answer, ok := model.doc.Blocks()[1].(*AssistantBlock)
	if !ok {
		t.Fatalf("second block = %#v, want AssistantBlock", model.doc.Blocks()[1])
	}
	if answer.Streaming {
		t.Fatal("assistant block should be finalized")
	}
	if strings.TrimSpace(answer.Raw) != "final answer" {
		t.Fatalf("assistant raw = %q, want final answer", answer.Raw)
	}
}

func TestPromptRequestWithoutChoicesStillRendersModal(t *testing.T) {
	model := NewModel(Config{
		AppName:   "CAELIS",
		Version:   "dev",
		Workspace: "/tmp/workspace",
		Commands:  DefaultCommands(),
		Wizards:   DefaultWizards(),
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	m := updated.(*Model)
	updated, _ = m.Update(PromptRequestMsg{
		Title:    "Approval Required",
		Prompt:   "Approval Required",
		Response: make(chan PromptResponse, 1),
	})
	m = updated.(*Model)

	view := m.View().Content
	if !strings.Contains(view, "Approval Required") {
		t.Fatalf("prompt view = %q, want modal title", view)
	}
}

func TestPromptRequestKeepsGatewayToolContentVisible(t *testing.T) {
	model := NewModel(Config{
		AppName:   "CAELIS",
		Version:   "dev",
		Workspace: "/tmp/workspace",
		Commands:  DefaultCommands(),
		Wizards:   DefaultWizards(),
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m := updated.(*Model)
	updated, _ = m.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				RawInput: map[string]any{"path": "/tmp/demo.txt"},
				Status:   "running",
				Scope:    appgateway.EventScopeMain,
			},
		},
	})
	m = updated.(*Model)
	updated, _ = m.Update(PromptRequestMsg{
		Title:    "Approval Required",
		Prompt:   "Approval Required",
		Response: make(chan PromptResponse, 1),
		Choices: []PromptChoice{
			{Label: "Allow once", Value: "allow_once"},
			{Label: "Reject once", Value: "reject_once"},
		},
		DefaultChoice: "allow_once",
	})
	m = updated.(*Model)

	view := m.View().Content
	if !strings.Contains(view, "READ") {
		t.Fatalf("view = %q, want tool row to remain visible", view)
	}
	if !strings.Contains(view, "Approval Required") {
		t.Fatalf("view = %q, want prompt title", view)
	}
}

func TestRunningGatewayToolCallIsVisibleBeforeTaskCompletes(t *testing.T) {
	model := NewModel(Config{
		AppName:   "CAELIS",
		Version:   "dev",
		Workspace: "/tmp/workspace",
		Commands:  DefaultCommands(),
		Wizards:   DefaultWizards(),
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m := updated.(*Model)
	updated, _ = m.Update(SetRunningMsg{Running: true})
	m = updated.(*Model)
	updated, _ = m.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawInput: map[string]any{"command": `echo "hi"`},
				Status:   "running",
				Scope:    appgateway.EventScopeMain,
			},
		},
	})
	m = updated.(*Model)

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, `• Ran echo "hi"`) {
		t.Fatalf("view = %q, want running tool call before task result", view)
	}
}

func TestPendingGatewayToolCallIsVisibleBeforeTaskCompletes(t *testing.T) {
	model := NewModel(Config{
		AppName:   "CAELIS",
		Version:   "dev",
		Workspace: "/tmp/workspace",
		Commands:  DefaultCommands(),
		Wizards:   DefaultWizards(),
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m := updated.(*Model)
	updated, _ = m.Update(SetRunningMsg{Running: true})
	m = updated.(*Model)
	updated, _ = m.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "LIST",
				RawInput: map[string]any{"path": `/tmp/workspace`},
				Status:   "pending",
				Scope:    appgateway.EventScopeMain,
			},
		},
	})
	m = updated.(*Model)

	view := m.View().Content
	if !strings.Contains(view, "LIST") {
		t.Fatalf("view = %q, want pending tool call before task result", view)
	}
}
