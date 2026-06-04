package tuiapp

import (
	"strings"
	"testing"
)

func TestFitHeaderRowPartsPreservesWorkspaceGitBranch(t *testing.T) {
	t.Parallel()

	workspace := `D:\xue\code\storage [⎇ feature/windows-paths*]`
	model := "xiaomi/mimo-v2.5-pro [high]"
	left, right := fitHeaderRowParts(58, workspace, model)

	if !strings.Contains(left, "[⎇ ") || !strings.Contains(left, "*]") {
		t.Fatalf("left header = %q, want visible git branch", left)
	}
	if right == "" {
		t.Fatalf("right header is empty, want model text preserved")
	}
}

func TestWorkspaceDisplayPreservesBranchAcrossPlainRefresh(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{
		Workspace: `D:\xue\code\storage [⎇ release/std-1.8.x-0603-xyz]`,
	})
	m.handleStatusRefreshResultMsg(StatusRefreshResultMsg{
		Workspace:    `D:\xue\code\storage`,
		HasWorkspace: true,
		Status:       StatusViewModel{Workspace: `D:\xue\code\storage`},
		HasView:      true,
	})

	got := m.headerWorkspaceText()
	want := `D:\xue\code\storage [⎇ release/std-1.8.x-0603-xyz]`
	if got != want {
		t.Fatalf("headerWorkspaceText() = %q, want preserved branch %q", got, want)
	}
}

func TestWorkspaceDisplayDropsBranchWhenWorkspaceChanges(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{
		Workspace: `D:\xue\code\storage [⎇ release/std-1.8.x-0603-xyz]`,
	})
	m.handleStatusRefreshResultMsg(StatusRefreshResultMsg{
		Workspace:    `D:\xue\code\cmpdts`,
		HasWorkspace: true,
		Status:       StatusViewModel{Workspace: `D:\xue\code\cmpdts`},
		HasView:      true,
	})

	got := m.headerWorkspaceText()
	want := `D:\xue\code\cmpdts`
	if got != want {
		t.Fatalf("headerWorkspaceText() = %q, want new workspace %q", got, want)
	}
}

func TestWindowTitleUsesWorkspaceName(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{
		Workspace: `D:\xue\code\storage [⎇ release/std-1.8.x-0603-xyz]`,
	})
	m.statusModel = "xiaomi/mimo-v2.5-pro [high]"

	got := m.windowTitle()
	if got != "storage" {
		t.Fatalf("windowTitle() = %q, want workspace basename", got)
	}
	if strings.Contains(got, "xiaomi") || strings.Contains(got, "mimo") {
		t.Fatalf("windowTitle() = %q, should not include model text", got)
	}
}

func TestWindowTitleShowsRunningTick(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{
		Workspace: `D:\xue\code\storage`,
	})
	m.running = true

	got := m.windowTitle()
	if !strings.Contains(got, "storage") {
		t.Fatalf("windowTitle() = %q, want workspace name", got)
	}
	if got == "storage" {
		t.Fatalf("windowTitle() = %q, want running tick", got)
	}
}
