package tuiapp

import (
	"context"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestSessionWorkbenchGroupsRowsAndKeepsSelectionOnRefresh(t *testing.T) {
	calls := 0
	model := NewModel(Config{NoAnimation: true, NoColor: true, SessionWorkbench: func(_ context.Context, req appserver.SessionWorkbenchRequest) (appserver.SessionWorkbenchPage, error) {
		calls++
		if calls == 1 {
			return appserver.SessionWorkbenchPage{
				NextCursor: "more",
				Sessions: []appserver.SessionWorkbenchRow{
					{SessionID: "first-id", WorkspaceKey: "a", CWD: "/workspace/a", Title: "近期活跃", Active: true, Unread: true},
					{SessionID: "second-id", WorkspaceKey: "b", CWD: "/workspace/b", Title: "New result", Unread: true},
					{SessionID: "third-id", WorkspaceKey: "a", CWD: "/workspace/a", Title: "Confirmed error", Failed: true},
				},
			}, nil
		}
		if req.Cursor == "more" {
			return appserver.SessionWorkbenchPage{Sessions: []appserver.SessionWorkbenchRow{
				{SessionID: "fourth-id", WorkspaceKey: "b", CWD: "/workspace/b", Title: "Earlier result"},
			}}, nil
		}
		return appserver.SessionWorkbenchPage{Refreshed: []appserver.SessionWorkbenchRow{
			{SessionID: "second-id", WorkspaceKey: "b", CWD: "/workspace/b", Title: "New result"},
		}}, nil
	}})
	model.Update(tea.WindowSizeMsg{Width: 124, Height: 22})
	openSessionPickerForTest(t, model)
	frame := ansi.Strip(model.View().Content)
	inOrder := func(values ...string) {
		t.Helper()
		previous := -1
		for _, value := range values {
			index := strings.Index(frame, value)
			if index <= previous {
				t.Fatalf("%q out of order in workbench:\n%s", value, frame)
			}
			previous = index
		}
	}
	inOrder("\n  a ", "近期活跃", "Active", "Confirmed error", "Failed", "\n  b ", "New result", "Ready", "Show more")
	if strings.Contains(frame, "first-id") || !strings.Contains(frame, "● 近期活跃") || !strings.Contains(frame, "Details") {
		t.Fatalf("unexpected workbench frame:\n%s", frame)
	}
	lines := strings.Split(frame, "\n")
	first, _, _ := strings.Cut(lines[2], "│")
	if strings.TrimSpace(first) != "a" {
		t.Fatalf("the workspaces should start after the divider: %q", first)
	}
	for i, line := range lines[1:] {
		if strings.HasPrefix(line, "  b ") {
			list, _, _ := strings.Cut(lines[i], "│")
			if strings.TrimSpace(list) != "" {
				t.Fatalf("workspace groups have no blank row:\n%s", frame)
			}
			break
		}
	}
	model.Update(keyPress("end"))
	_, load := model.Update(keyPress("enter"))
	model.Update(load())
	model.Update(keyPress("up"))
	before := model.sessionPicker.selectionID()
	_, refresh := model.Update(keyPress("r"))
	model.Update(refresh())
	if model.sessionPicker.selectionID() != before || model.sessionPicker.nextCursor != "" {
		t.Fatal("refresh disturbed the selection or reopened an exhausted page")
	}
	frame = ansi.Strip(model.View().Content)
	if strings.Contains(frame, "Ready") || !strings.Contains(frame, "Earlier result") {
		t.Fatalf("stale readiness or lost older page:\n%s", frame)
	}
}

func TestSessionWorkbenchSelectionContrast(t *testing.T) {
	const width, height = 124, 22
	rows := []ResumeCandidate{
		{SessionID: "one", Title: "Review the handoff", Workspace: "/very/long/workspace/path/clients/core", Failed: true, Unread: true},
		{SessionID: "two", Title: "Check the draft", Workspace: "/other/worktrees/core"},
	}
	for _, name := range []string{"catppuccin-mocha", "catppuccin-latte"} {
		t.Run(name, func(t *testing.T) {
			model := newSessionPickerTestModel(t, width, height, rows, nil)
			model.applyTheme(tuikit.ResolveSelectedTheme(name, nil, true, false, colorprofile.TrueColor))
			openSessionPickerForTest(t, model)
			frame := model.View().Content
			screen := uv.NewScreenBuffer(width, height)
			screen.Method = ansi.GraphemeWidth
			uv.NewStyledString(frame).Draw(screen, screen.Bounds())
			y := sessionPickerScreenRow(model.sessionPicker, 0)
			plain := ansi.Strip(frame)
			lines := strings.Split(plain, "\n")
			for _, label := range []string{"│  /very/long/workspace/path/", "│  clients/core", "/other/worktrees/core"} {
				if !strings.Contains(plain, label) {
					t.Fatalf("workspace locations missing:\n%s", plain)
				}
			}
			line, _, _ := strings.Cut(lines[y], "│")
			background := model.theme.ListSelectionStyle().GetBackground()
			if diffRenderedContrast(background, model.theme.AppBg) < 4.5 {
				t.Fatal("selection band blends into the list")
			}
			for x := sessionPickerInset; x < model.sessionPicker.listWidth-sessionPickerInset; x++ {
				cell := screen.CellAt(x, y)
				if !colorInSet(cell.Style.Bg, []color.Color{background}) {
					t.Fatalf("selection band breaks at column %d", x)
				}
			}
			for _, text := range []string{"●", "Failed"} {
				index := strings.Index(line, text)
				if index < 0 {
					t.Fatalf("selected row lost %q: %s", text, line)
				}
				cell := screen.CellAt(displayColumns(line[:index]), y)
				if cell.Style.Fg == nil || diffRenderedContrast(cell.Style.Fg, background) < 4.5 ||
					colorInSet(cell.Style.Fg, []color.Color{model.theme.ListSelectionStyle().GetForeground()}) {
					t.Fatalf("selected %s lost its readable semantic color", text)
				}
			}
		})
	}
}

func TestSessionWorkbenchWrapsAndScrollsDetails(t *testing.T) {
	const width, height = 124, 12
	path := `C:\` + strings.Repeat(`nested  workspace directory\`, 9) + strings.Repeat("深", 18) + `\last-directory`
	rows := []ResumeCandidate{
		{SessionID: "one", Title: "Inspect the long path", Workspace: path},
		{SessionID: "two", Title: "Another session", Workspace: `C:\short`},
	}
	model := newSessionPickerTestModel(t, width, height, rows, nil)
	openSessionPickerForTest(t, model)
	frame := ansi.Strip(model.View().Content)
	if !strings.Contains(frame, "Scroll details") {
		t.Fatalf("expected overflowing details:\n%s", frame)
	}
	part := wrapPickerPath(path, 33)
	if strings.Join(part, "") != path || part[0] != `C:\nested  workspace directory\` {
		t.Fatalf("path wrapping lost content or broke a fitting component: %q", part)
	}
	for _, line := range part {
		if displayColumns(line) > 33 {
			t.Fatalf("path line exceeds the pane width: %q", line)
		}
	}
	model.Update(tea.MouseWheelMsg{X: width - 5, Y: 5, Button: tea.MouseWheelDown})
	if model.sessionPicker.detailsOffset != 1 || model.sessionPicker.index != 0 {
		t.Fatal("scrolling details moved the list selection or left details stationary")
	}
	for range model.sessionPicker.detailsOverflow {
		model.Update(keyPress("shift+down"))
	}
	frame = ansi.Strip(model.View().Content)
	if !strings.Contains(frame, `\last-directory`) || !strings.Contains(frame, "│  Updated") {
		t.Fatalf("end of the workspace details is unreachable:\n%s", frame)
	}
	model.Update(keyPress("down"))
	frame = ansi.Strip(model.View().Content)
	if model.sessionPicker.detailsOffset != 0 || !strings.Contains(frame, `│  C:\short`) {
		t.Fatalf("new selection inherited the prior scroll offset:\n%s", frame)
	}
}

func TestSessionReadRequiresTheRenderedResultInTheFocusedConversation(t *testing.T) {
	var received []appserver.SessionReadReceipt
	model := NewModel(Config{
		NoAnimation: true, NoColor: true,
		AcknowledgeSessionRead: func(_ context.Context, id string, receipt appserver.SessionReadReceipt) error {
			if id != "result-session" {
				t.Fatalf("acknowledgment targeted %q", id)
			}
			received = append(received, receipt)
			return nil
		},
	})
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 25})
	model.currentSessionID = "result-session"
	env := eventstream.Envelope{
		SessionID: "result-session", TurnID: "result-turn", Kind: eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain, Final: true,
		Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 17}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "Visible response"},
		},
	}
	model.handleACPEventEnvelope(env)
	model.flushPendingViewportSync()
	if model.sessionRead.sequence != 17 {
		t.Fatal("visible result was not tracked")
	}
	picker := &sessionPickerState{}
	model.sessionPicker = picker
	model.View()
	check := sessionReadCheckMsg{generation: model.viewGeneration}
	if cmd := model.acknowledgeRenderedSessionResult(check); cmd != nil {
		t.Fatal("workbench acknowledged a hidden conversation")
	}
	model.sessionPicker = nil
	model.focused = false
	model.View()
	if cmd := model.acknowledgeRenderedSessionResult(check); cmd != nil {
		t.Fatal("unfocused conversation acknowledged a result")
	}
	model.focused = true
	model.View()
	cmd := model.acknowledgeRenderedSessionResult(check)
	if cmd == nil {
		t.Fatal("opening the rendered result did not acknowledge it")
	}
	model.Update(cmd())
	if len(received) != 1 || received[0].ThroughSeq != 17 || model.acknowledgeRenderedSessionResult(check) != nil {
		t.Fatalf("read receipts = %+v", received)
	}
}
