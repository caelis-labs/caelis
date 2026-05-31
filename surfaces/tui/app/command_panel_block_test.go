package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func TestCommandPanelBlockRendersSettingsPanelPayload(t *testing.T) {
	panel := appviewmodel.SettingsPanelView{
		Configured: true,
		Runtime:    appviewmodel.RuntimeStatus{WorkspaceCWD: "/repo"},
		Model: appviewmodel.ModelStatus{Current: &appviewmodel.ModelChoice{
			Provider: "openai",
			Model:    "gpt-4o",
		}},
		Sandbox: appviewmodel.SandboxPanel{Status: appviewmodel.SandboxPanelStatus{
			RequestedBackend: "host",
			ResolvedBackend:  "host",
			Route:            "host",
		}},
		Sections: []appviewmodel.SettingsPanelSection{{
			ID:    "sandbox",
			Title: "Sandbox",
			Fields: []appviewmodel.SettingsPanelField{{
				ID:       "sandbox.backend",
				Label:    "Requested backend",
				Kind:     "select",
				Command:  "/settings set sandbox.backend ",
				Value:    "host",
				Editable: true,
				Options: []appviewmodel.SettingsPanelFieldOption{{
					Value: "host",
					Label: "Host",
				}},
			}},
		}},
		Actions: []appviewmodel.SettingsPanelAction{{
			ID:      "model.connect",
			Label:   "Connect model",
			Command: "/connect",
			Enabled: true,
		}},
	}
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		Command:       "settings",
		SettingsPanel: &panel,
	})
	model := NewModel(Config{})
	rows := block.Render(BlockRenderContext{Width: 96, Theme: model.theme})
	plain := renderedPlainText(rows)
	for _, want := range []string{"SETTINGS", "Configuration", "workspace", "/repo", "sandbox.backend", "model.connect"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered settings panel = %q, missing %q", plain, want)
		}
	}
	if !rowsContainCommandPanelInput(rows, "/settings set sandbox.backend ") {
		t.Fatalf("settings field row missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/connect") {
		t.Fatalf("settings action row missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{SettingsPanel: &panel}, "/connect"); action.line != "/connect" {
		t.Fatalf("settings connect action = %#v, want immediate /connect submit", action)
	}
}

func TestCommandPanelBlockRendersTaskActions(t *testing.T) {
	panel := appviewmodel.TaskPanelView{
		Supported: true,
		Summary:   appviewmodel.TaskPanelSummary{Total: 1, Running: 1},
		Tasks: []appviewmodel.TaskItem{{
			ID:            "task-1",
			Kind:          "command",
			Source:        "live",
			State:         "running",
			Running:       true,
			SupportsInput: true,
			Title:         "echo ready",
		}},
		Actions: []appviewmodel.TaskPanelAction{{
			ID:      "task.tail:task-1",
			Kind:    "tail",
			Label:   "Tail",
			Command: "/task tail task-1",
			TaskID:  "task-1",
			Enabled: true,
		}, {
			ID:            "task.write:task-1",
			Kind:          "write",
			Label:         "Write",
			Command:       "/task write task-1 -- ",
			TaskID:        "task-1",
			Enabled:       true,
			RequiresInput: true,
		}, {
			ID:          "task.cancel:task-1",
			Kind:        "cancel",
			Label:       "Cancel",
			Command:     "/task cancel task-1",
			TaskID:      "task-1",
			Enabled:     true,
			Destructive: true,
		}},
	}
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		Command:   "task",
		TaskPanel: &panel,
	})
	model := NewModel(Config{})
	rows := block.Render(BlockRenderContext{Width: 96, Theme: model.theme})
	plain := renderedPlainText(rows)
	for _, want := range []string{"TASKS", "Actions", "task.write:task-1", "task.cancel:task-1", "/task write task-1 --", "/task cancel task-1"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered task panel = %q, missing %q", plain, want)
		}
	}
	if !rowsContainCommandPanelInput(rows, "/task tail task-1") {
		t.Fatalf("task tail action missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/task write task-1 -- ") {
		t.Fatalf("task write action missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/task cancel task-1") {
		t.Fatalf("task cancel action missing command-panel input token: %#v", renderedPlainRows(rows))
	}
}

func TestCommandPanelBlockRendersResumePanelActions(t *testing.T) {
	panel := appviewmodel.ResumePanelView{
		Workspace: session.Workspace{CWD: "/repo"},
		Sessions: []appviewmodel.ResumeSessionItem{{
			Ref:        session.Ref{SessionID: "sess-alpha"},
			SessionID:  "sess-alpha",
			Title:      "alpha work",
			Workspace:  "/repo",
			EventCount: 3,
			UpdatedAt:  time.Date(2026, 5, 31, 10, 30, 0, 0, time.UTC),
			Command:    "/resume sess-alpha",
			Actions: []appviewmodel.ResumeSessionAction{{
				ID:        "resume.open:sess-alpha",
				Kind:      "open",
				Label:     "Resume",
				Command:   "/resume sess-alpha",
				SessionID: "sess-alpha",
				Enabled:   true,
			}},
		}},
	}
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		Command:     "resume",
		ResumePanel: &panel,
	})
	model := NewModel(Config{})
	rows := block.Render(BlockRenderContext{Width: 96, Theme: model.theme})
	plain := renderedPlainText(rows)
	for _, want := range []string{"SESSIONS", "Resume Session", "sess-alpha", "alpha work", "/repo"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered resume panel = %q, missing %q", plain, want)
		}
	}
	if !rowsContainCommandPanelInput(rows, "/resume sess-alpha") {
		t.Fatalf("resume row missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{ResumePanel: &panel}, "/resume sess-alpha"); action.line != "/resume sess-alpha" {
		t.Fatalf("resume panel action = %#v, want immediate submit", action)
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{ResumePanel: &panel}, "/resume missing"); action.line != "" || action.fillInput != "/resume missing" {
		t.Fatalf("unknown resume panel action = %#v, want fill input instead of broad submit", action)
	}
}

func TestCommandPanelBlockRendersApprovalPanelActions(t *testing.T) {
	panel := appviewmodel.ApprovalPanelView{
		Scope:           "session",
		CurrentMode:     "manual",
		CurrentModeName: "Manual",
		ModeOptions: []appviewmodel.ApprovalModeChoice{{
			ID:      "auto-review",
			Name:    "Auto Review",
			Command: "/approval auto-review",
		}, {
			ID:      "manual",
			Name:    "Manual",
			Current: true,
			Command: "/approval manual",
		}},
		Pending: []appviewmodel.ApprovalItem{{
			ID:      "approval-1",
			Tool:    "run_command",
			Command: "printf hi",
			Actions: []appviewmodel.ApprovalAction{{
				ID:       "allow_once",
				Name:     "Allow once",
				Approved: true,
			}, {
				ID:   "reject_once",
				Name: "Reject once",
			}},
		}},
		Actions: []appviewmodel.ApprovalPanelAction{{
			ID:      "approval.mode.toggle",
			Kind:    "toggle",
			Label:   "Toggle mode",
			Command: "/approval toggle",
			Enabled: true,
		}},
	}
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		Command:       "approval",
		ApprovalPanel: &panel,
	})
	model := NewModel(Config{})
	rows := block.Render(BlockRenderContext{Width: 96, Theme: model.theme})
	plain := renderedPlainText(rows)
	for _, want := range []string{"APPROVAL", "Approval", "manual", "approval-1", "run_command", "approval.mode.toggle", "/approval auto-review", "/approval toggle"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered approval panel = %q, missing %q", plain, want)
		}
	}
	if !rowsContainCommandPanelInput(rows, "/approval auto-review") {
		t.Fatalf("approval mode row missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/approval toggle") {
		t.Fatalf("approval action row missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{ApprovalPanel: &panel}, "/approval toggle"); action.line != "/approval toggle" {
		t.Fatalf("approval panel action = %#v, want immediate submit", action)
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{ApprovalPanel: &panel}, "/approval auto-review"); action.line != "/approval auto-review" {
		t.Fatalf("approval mode action = %#v, want immediate mode submit", action)
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{ApprovalPanel: &panel}, "/approval manual"); action.line != "" || action.fillInput != "/approval manual" {
		t.Fatalf("current approval mode action = %#v, want fill input instead of broad submit", action)
	}
}

func TestCommandPanelBlockRendersModelSelectionActions(t *testing.T) {
	panel := appviewmodel.ModelSelectionView{
		Current: &appviewmodel.ModelChoice{
			ID:       "alpha",
			Alias:    "alpha",
			Provider: "openai-compatible",
			Model:    "gpt-alpha",
			Default:  true,
		},
		Configured: []appviewmodel.ModelChoice{{
			ID:       "alpha",
			Alias:    "alpha",
			Provider: "openai-compatible",
			Model:    "gpt-alpha",
			Default:  true,
		}, {
			ID:       "beta",
			Alias:    "beta",
			Provider: "openai-compatible",
			Model:    "gpt-beta",
		}},
		RemoteEnabled: true,
		Actions: []appviewmodel.ModelSelectionAction{{
			ID:      "model.connect",
			Kind:    "connect",
			Label:   "Connect model",
			Command: "/connect",
			Enabled: true,
		}, {
			ID:      "model.use:beta",
			Kind:    "use",
			Label:   "Use beta",
			ModelID: "beta",
			Command: "/model use beta",
			Enabled: true,
		}, {
			ID:          "model.delete:alpha",
			Kind:        "delete",
			Label:       "Delete alpha",
			ModelID:     "alpha",
			Command:     "/model del alpha",
			Enabled:     true,
			Destructive: true,
		}},
	}
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		Command:        "model",
		ModelSelection: &panel,
	})
	model := NewModel(Config{})
	rows := block.Render(BlockRenderContext{Width: 96, Theme: model.theme})
	plain := renderedPlainText(rows)
	for _, want := range []string{"MODELS", "Model Selection", "alpha", "current", "default", "model.use:beta", "model.delete:alpha"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered model panel = %q, missing %q", plain, want)
		}
	}
	if !rowsContainCommandPanelInput(rows, "/connect") {
		t.Fatalf("model connect action missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/model use beta") {
		t.Fatalf("model use action missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/model del alpha") {
		t.Fatalf("model delete action missing command-panel input token: %#v", renderedPlainRows(rows))
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{ModelSelection: &panel}, "/model use beta"); action.line != "/model use beta" {
		t.Fatalf("model use action = %#v, want immediate submit", action)
	}
	action := commandPanelActionForInput(appviewmodel.CommandExecutionView{ModelSelection: &panel}, "/model del alpha")
	if action.prompt == nil || action.prompt.buildLine("run") != "/model del alpha" {
		t.Fatalf("model delete action = %#v, want confirmation prompt", action)
	}
}

func TestCommandPanelBlockRendersAgentManagementActions(t *testing.T) {
	panel := appviewmodel.AgentManagementView{
		CanRegisterCustom: true,
		Registered: []appviewmodel.AgentManagementItem{{
			Agent:      appviewmodel.AgentItem{ID: "reviewer", Name: "reviewer", Kind: "external_acp", Command: "reviewer-acp"},
			Source:     "registered",
			Registered: true,
			Actions: []appviewmodel.AgentManagementAction{{
				ID:            "invoke",
				Name:          "Invoke",
				Kind:          "invoke",
				AgentID:       "reviewer",
				Command:       "/reviewer ",
				Enabled:       true,
				RequiresInput: true,
			}, {
				ID:      "use_controller",
				Name:    "Use as controller",
				Kind:    "controller",
				AgentID: "reviewer",
				Command: "/agent use reviewer",
				Enabled: true,
			}, {
				ID:          "remove",
				Name:        "Remove",
				Kind:        "remove",
				AgentID:     "reviewer",
				Command:     "/agent remove reviewer",
				Enabled:     true,
				Destructive: true,
			}},
		}},
		Builtins: []appviewmodel.AgentManagementItem{{
			Agent:       appviewmodel.AgentItem{ID: "codex", Name: "codex", Kind: "external_acp", Command: "npx"},
			Source:      "builtin",
			Builtin:     true,
			Installable: true,
			Actions: []appviewmodel.AgentManagementAction{{
				ID:      "register",
				Name:    "Register",
				Kind:    "register",
				AgentID: "codex",
				Command: "/agent add codex",
				Enabled: true,
			}, {
				ID:      "install",
				Name:    "Install",
				Kind:    "install",
				AgentID: "codex",
				Command: "/agent install codex",
				Enabled: true,
			}},
		}},
		Installable: []appviewmodel.AgentInstallItem{{
			ID:     "claude",
			Name:   "claude",
			Detail: "Claude Code ACP",
			Actions: []appviewmodel.AgentManagementAction{{
				ID:      "install",
				Name:    "Install",
				Kind:    "install",
				AgentID: "claude",
				Command: "/agent install claude",
				Enabled: true,
			}},
		}},
		Actions: []appviewmodel.AgentManagementAction{{
			ID:            "register_custom",
			Name:          "Register custom agent",
			Kind:          "register_custom",
			Command:       "/agent add custom ",
			Enabled:       true,
			RequiresInput: true,
		}},
	}
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		Command:         "agent",
		AgentManagement: &panel,
	})
	model := NewModel(Config{})
	rows := block.Render(BlockRenderContext{Width: 112, Theme: model.theme})
	plain := renderedPlainText(rows)
	for _, want := range []string{"AGENTS", "Agent Registry", "reviewer", "use_controller", "remove", "codex", "claude", "register_custom"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered agent panel = %q, missing %q", plain, want)
		}
	}
	for _, input := range []string{"/reviewer ", "/agent use reviewer", "/agent remove reviewer", "/agent add codex", "/agent install claude", "/agent add custom "} {
		if !rowsContainCommandPanelInput(rows, input) {
			t.Fatalf("agent action missing command-panel input %q: %#v", input, renderedPlainRows(rows))
		}
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{AgentManagement: &panel}, "/reviewer "); action.fillInput != "/reviewer " {
		t.Fatalf("agent invoke action = %#v, want input fill", action)
	}
	if action := commandPanelActionForInput(appviewmodel.CommandExecutionView{AgentManagement: &panel}, "/agent use reviewer"); action.line != "/agent use reviewer" {
		t.Fatalf("agent use action = %#v, want immediate submit", action)
	}
	action := commandPanelActionForInput(appviewmodel.CommandExecutionView{AgentManagement: &panel}, "/agent remove reviewer")
	if action.prompt == nil || action.prompt.buildLine("run") != "/agent remove reviewer" {
		t.Fatalf("agent remove action = %#v, want confirmation prompt", action)
	}
}

func TestCommandPanelBlockRendersControllerConfigActions(t *testing.T) {
	panel := appviewmodel.ControllerPanelView{
		Active: true,
		Summary: appviewmodel.ControllerPanelSummary{
			Agent:           "reviewer",
			RemoteSessionID: "remote-1",
			Model:           "gpt-remote",
			ReasoningEffort: "high",
			Mode:            "code",
		},
		Sections: []appviewmodel.ControllerPanelSection{{
			ID:    "configuration",
			Title: "Configuration",
			Fields: []appviewmodel.ControllerPanelField{{
				ID:       "controller.model",
				Label:    "Model",
				Kind:     "select",
				Value:    "gpt-remote",
				Command:  "/model use ",
				Editable: true,
				Options: []appviewmodel.ControllerConfigChoice{{
					Value: "gpt-remote",
					Name:  "GPT Remote",
				}},
			}, {
				ID:       "controller.reasoning",
				Label:    "Reasoning",
				Kind:     "select",
				Value:    "high",
				Command:  "/model use gpt-remote ",
				Editable: true,
				Options: []appviewmodel.ControllerConfigChoice{{
					Value: "low",
					Name:  "Low",
				}, {
					Value: "high",
					Name:  "High",
				}},
			}, {
				ID:       "controller.config.theme",
				Label:    "Theme",
				Kind:     "select",
				Value:    "light",
				Command:  "/controller set theme ",
				Editable: true,
				Options: []appviewmodel.ControllerConfigChoice{{
					Value: "light",
					Name:  "Light",
				}, {
					Value: "dark",
					Name:  "Dark",
				}},
			}},
		}},
		Actions: []appviewmodel.ControllerPanelAction{{
			ID:      "controller.handoff.local",
			Kind:    "handoff",
			Label:   "Return to local",
			Command: "/agent use local",
			Enabled: true,
		}},
	}
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		Command:         "controller",
		ControllerPanel: &panel,
	})
	model := NewModel(Config{})
	rows := block.Render(BlockRenderContext{Width: 96, Theme: model.theme})
	plain := renderedPlainText(rows)
	for _, want := range []string{"CONTROLLER", "ACP Controller", "reviewer", "Reasoning", "Theme", "controller.handoff.local", "/model use gpt-remote", "/controller set theme", "/agent use local"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered controller panel = %q, missing %q", plain, want)
		}
	}
	if !rowsContainCommandPanelInput(rows, "/model use gpt-remote ") {
		t.Fatalf("controller reasoning row missing model-use input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/controller set theme ") {
		t.Fatalf("controller config row missing controller-set input token: %#v", renderedPlainRows(rows))
	}
	if !rowsContainCommandPanelInput(rows, "/agent use local") {
		t.Fatalf("controller local handoff action missing command-panel input token: %#v", renderedPlainRows(rows))
	}
}

func renderedPlainText(rows []RenderedRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Plain)
	}
	return strings.Join(parts, "\n")
}

func rowsContainCommandPanelInput(rows []RenderedRow, input string) bool {
	for _, row := range rows {
		got, ok := commandPanelInputFromClickToken(row.ClickToken)
		if ok && got == input {
			return true
		}
	}
	return false
}
