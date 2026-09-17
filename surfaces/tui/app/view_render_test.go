package tuiapp

import (
	"strings"
	"testing"
	"time"
)

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

func TestWindowTitleSpinnerLeavesTimeForTerminalDebounce(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{})
	if interval := m.spinner.Spinner.FPS; interval <= 75*time.Millisecond {
		t.Fatalf("spinner interval = %s, must exceed the terminal title's 75ms debounce", interval)
	}
}

func TestWindowTitleShowsRunningTick(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{
		Workspace: `D:\xue\code\storage`,
	})
	m.liveTurn.Active = true

	got := m.windowTitle()
	if !strings.Contains(got, "storage") {
		t.Fatalf("windowTitle() = %q, want workspace name", got)
	}
	if got == "storage" {
		t.Fatalf("windowTitle() = %q, want running tick", got)
	}
}
