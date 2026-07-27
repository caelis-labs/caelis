package tuiapp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestWelcomeVersionLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: "1.2.3", want: "v1.2.3"},
		{in: "v1.2.3", want: "v1.2.3"},
		{in: "  2.0  ", want: "v2.0"},
		{in: "", want: "v0.0.0"},
	} {
		if got := welcomeVersionLabel(tc.in); got != tc.want {
			t.Fatalf("welcomeVersionLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWelcomeBlockResponsiveRender(t *testing.T) {
	t.Parallel()
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	cases := []struct {
		name        string
		width       int
		height      int
		wantLogoRow string
		wantBrand   string
	}{
		{name: "fullscreen workspace", width: 190, height: 50, wantLogoRow: welcomeRelayLogoASCII[0], wantBrand: "CAELIS"},
		{name: "100x30 terminal workspace", width: 97, height: 25, wantLogoRow: welcomeRelayLogoASCII[0], wantBrand: "CAELIS"},
		{name: "80x24 terminal workspace", width: 77, height: 19, wantLogoRow: welcomeRelayLogoASCII[0], wantBrand: "CAELIS"},
		{name: "55x20 terminal workspace", width: 52, height: 15, wantLogoRow: welcomeRelayLogoASCII[0], wantBrand: "CAELIS"},
		{name: "35x16 terminal workspace", width: 33, height: 11, wantLogoRow: welcomeRelayCompactASCII[0], wantBrand: "CAELIS"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows := NewWelcomeBlock("v9.9.9").Render(BlockRenderContext{
				Width:     tc.width,
				Height:    tc.height,
				TermWidth: tc.width,
				Theme:     theme,
				Workspace: "/workspace/branch-that-must-not-render",
			})
			plain := strings.Join(rowPlainTexts(rows), "\n")
			for _, action := range welcomeActions {
				if got := strings.Count(plain, action.label); got != 1 {
					t.Fatalf("%s count = %d, want 1\n%s", action.label, got, plain)
				}
				if !strings.Contains(plain, welcomeActionMarker+action.label) {
					t.Fatalf("welcome action %q is missing its click marker\n%s", action.label, plain)
				}
			}
			if !strings.Contains(plain, tc.wantLogoRow) {
				t.Fatalf("welcome missing selected relay logo row %q\n%s", tc.wantLogoRow, plain)
			}
			if !strings.Contains(plain, tc.wantBrand) {
				t.Fatalf("welcome missing brand treatment %q\n%s", tc.wantBrand, plain)
			}
			if got := strings.Count(plain, "v9.9.9"); got != 1 {
				t.Fatalf("version count = %d, want 1\n%s", got, plain)
			}
			if got := strings.Count(plain, "type / for commands"); got != 1 {
				t.Fatalf("default tip count = %d, want 1\n%s", got, plain)
			}
			if strings.Contains(plain, "Click an action") {
				t.Fatalf("welcome still contains removed click hint\n%s", plain)
			}
			for _, unwanted := range []string{
				"Start a task",
				"One workspace",
				"/ commands",
				"@ context",
				"/resume continue",
				"branch-that-must-not-render",
				"enter",
				"ctrl+",
				"/connect",
				"/resume",
				"/quit",
			} {
				if strings.Contains(plain, unwanted) {
					t.Fatalf("welcome unexpectedly contains %q\n%s", unwanted, plain)
				}
			}
			for _, row := range rows {
				if got := displayColumns(row.Plain); got > tc.width {
					t.Fatalf("plain row width = %d, want <= %d: %q", got, tc.width, row.Plain)
				}
				if got := displayColumns(ansi.Strip(row.Styled)); got > tc.width {
					t.Fatalf("styled row width = %d, want <= %d: %q", got, tc.width, ansi.Strip(row.Styled))
				}
			}
		})
	}
}

func TestWelcomeDetailsUseThreeSeparatedRegions(t *testing.T) {
	t.Parallel()
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	styles := newWelcomePanelStyles(theme.Tokens())
	rows := buildWelcomeDetails(48, "v1.2.3", styles)
	if len(rows) != 8 {
		t.Fatalf("detail rows = %d, want 8", len(rows))
	}
	if got := strings.TrimSpace(rows[0].plain); got != "CAELIS  v1.2.3" {
		t.Fatalf("identity region = %q, want plain CAELIS and version", got)
	}
	if strings.TrimSpace(rows[1].plain) != "" || strings.TrimSpace(rows[3].plain) != "" {
		t.Fatalf("regions are not separated by blank rows: %#v", rowPlainTextsForWelcomePanel(rows))
	}
	if got := strings.TrimSpace(rows[2].plain); got != welcomeDefaultNotice {
		t.Fatalf("notice region = %q, want %q", got, welcomeDefaultNotice)
	}
	for i, action := range welcomeActions {
		if rows[i+4].token != action.token || !strings.Contains(rows[i+4].plain, welcomeActionMarker+action.label) {
			t.Fatalf("action row %d = %#v, want marked %q", i, rows[i+4], action.label)
		}
	}
	if strings.TrimSpace(rows[7].plain) != "" {
		t.Fatalf("action region should end with breathing room: %q", rows[7].plain)
	}
}

func TestWelcomePanelScalesWithinWideTallWorkspace(t *testing.T) {
	t.Parallel()
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	medium := buildWelcomePanel(BlockRenderContext{Width: 77, Height: 19, Theme: theme}, "v1.0.0")
	fullscreen := buildWelcomePanel(BlockRenderContext{Width: 190, Height: 50, Theme: theme}, "v1.0.0")
	if got := displayColumns(medium[0].plain); got != welcomePanelBaseWidth {
		t.Fatalf("medium panel width = %d, want %d", got, welcomePanelBaseWidth)
	}
	if got := displayColumns(fullscreen[0].plain); got != welcomePanelMaxWidth {
		t.Fatalf("fullscreen panel width = %d, want %d", got, welcomePanelMaxWidth)
	}
	if len(fullscreen) <= len(medium) {
		t.Fatalf("fullscreen panel rows = %d, want more breathing room than medium %d", len(fullscreen), len(medium))
	}
}

func TestWelcomeSideBySideRowsPadMismatchedColumns(t *testing.T) {
	t.Parallel()
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	styles := newWelcomePanelStyles(theme.Tokens())
	rightRows := []welcomePanelRow{
		welcomeTextCell("identity", 24, styles.brand),
		welcomeTextCell("", 24, styles.surface),
		welcomeActionCell(welcomeActions[0], 24, true, styles),
	}

	rows := welcomeSideBySideRows([]string{"logo"}, 4, rightRows, 24, 1, 2, styles)
	if len(rows) != len(rightRows) {
		t.Fatalf("right-longer rows = %d, want %d", len(rows), len(rightRows))
	}
	if rows[2].token != welcomeActionTokenConnect || rows[2].clickEndCol <= rows[2].clickStartCol {
		t.Fatalf("right-longer action geometry = %#v", rows[2])
	}

	rows = welcomeSideBySideRows([]string{"one", "two", "three"}, 5, rightRows[:1], 24, 1, 2, styles)
	if len(rows) != 3 {
		t.Fatalf("left-longer rows = %d, want 3", len(rows))
	}
	for i, row := range rows {
		if got := displayColumns(row.plain); got != 1+5+2+24+1 {
			t.Fatalf("row %d width = %d, want 33: %q", i, got, row.plain)
		}
	}
}

func TestWelcomeBlockActionRowsCarryStableTokens(t *testing.T) {
	t.Parallel()
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 97, height: 25},
		{width: 77, height: 19},
		{width: 52, height: 15},
		{width: 33, height: 11},
	} {
		rows := NewWelcomeBlock("v1.0.0").Render(BlockRenderContext{
			Width:  size.width,
			Height: size.height,
			Theme:  theme,
		})
		var got []string
		for _, row := range rows {
			if row.ClickToken == "" {
				continue
			}
			got = append(got, row.ClickToken)
			action, ok := welcomeActionForToken(row.ClickToken)
			if !ok {
				t.Fatalf("%dx%d token %q is outside welcome namespace", size.width, size.height, row.ClickToken)
			}
			if row.ClickEndCol <= row.ClickStartCol+displayColumns(action.label) {
				t.Fatalf("%dx%d action %q click bounds = [%d,%d), want full trailing row",
					size.width, size.height, action.label, row.ClickStartCol, row.ClickEndCol)
			}
		}
		want := []string{
			welcomeActionTokenConnect,
			welcomeActionTokenResume,
			welcomeActionTokenQuit,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%dx%d click tokens = %#v, want %#v", size.width, size.height, got, want)
		}
	}
}

func TestWelcomeBlockUltraSmallFallbackKeepsActionsClickable(t *testing.T) {
	t.Parallel()
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	for width := 1; width <= 8; width++ {
		rows := NewWelcomeBlock("v1.0.0").Render(BlockRenderContext{
			Width:  width,
			Height: 3,
			Theme:  theme,
		})
		if len(rows) != len(welcomeActions) {
			t.Fatalf("width %d rows = %d, want %d action rows", width, len(rows), len(welcomeActions))
		}
		for i, row := range rows {
			action := welcomeActions[i]
			if row.ClickToken != action.token {
				t.Fatalf("width %d row %d token = %q, want %q", width, i, row.ClickToken, action.token)
			}
			if got := displayColumns(row.Plain); got > width {
				t.Fatalf("width %d row %d display width = %d: %q", width, i, got, row.Plain)
			}
			if row.ClickEndCol <= row.ClickStartCol {
				t.Fatalf("width %d row %d action %q has no click bounds: %#v", width, i, action.label, row)
			}
		}
	}
}

func TestWelcomeFramesFitRequiredTerminalSizes(t *testing.T) {
	t.Parallel()
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 100, height: 30},
		{width: 80, height: 24},
		{width: 55, height: 20},
		{width: 35, height: 16},
	} {
		model := newWelcomeTestModel(t, size.width, size.height, Config{})
		frame := model.View().Content
		lines := strings.Split(frame, "\n")
		if len(lines) != size.height {
			t.Fatalf("%dx%d frame rows = %d, want %d", size.width, size.height, len(lines), size.height)
		}
		for i, line := range lines {
			if got := displayColumns(ansi.Strip(line)); got > size.width {
				t.Fatalf("%dx%d frame row %d width = %d, want <= %d: %q", size.width, size.height, i, got, size.width, ansi.Strip(line))
			}
		}
		plain := ansi.Strip(frame)
		for _, action := range welcomeActions {
			if !strings.Contains(plain, action.label) {
				t.Fatalf("%dx%d frame missing %q\n%s", size.width, size.height, action.label, plain)
			}
		}
	}
}

func TestWelcomeWorkspaceMetadataOnlyAppearsInFooter(t *testing.T) {
	t.Parallel()
	const workspace = "/workspace/caelis"
	model := newWelcomeTestModel(t, 80, 24, Config{Workspace: workspace})
	lines := strings.Split(ansi.Strip(model.View().Content), "\n")
	if got := strings.Count(strings.Join(lines, "\n"), workspace); got != 1 {
		t.Fatalf("workspace count = %d, want one footer copy", got)
	}
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], workspace) {
		t.Fatalf("workspace not confined to footer: %#v", lines)
	}
}

func TestWelcomeMouseActionsMatchManualSlashSubmission(t *testing.T) {
	t.Run("connect opens the same wizard", func(t *testing.T) {
		manual := newWelcomeTestModel(t, 80, 24, Config{})
		clicked := newWelcomeTestModel(t, 80, 24, Config{})

		submitManualWelcomeCommand(manual, "/connect")
		_ = clickWelcomeAction(t, clicked, welcomeActionTokenConnect)

		if !manual.isWizardActive() || !clicked.isWizardActive() {
			t.Fatalf("wizard active: manual=%v clicked=%v", manual.isWizardActive(), clicked.isWizardActive())
		}
		if manual.slashArgCommand != clicked.slashArgCommand || manual.textarea.Value() != clicked.textarea.Value() {
			t.Fatalf("connect state differs: manual=(%q,%q) clicked=(%q,%q)",
				manual.slashArgCommand, manual.textarea.Value(), clicked.slashArgCommand, clicked.textarea.Value())
		}
	})

	t.Run("resume opens the same picker", func(t *testing.T) {
		manual := newWelcomeTestModel(t, 80, 24, Config{})
		clicked := newWelcomeTestModel(t, 80, 24, Config{})

		submitManualWelcomeCommand(manual, "/resume")
		_ = clickWelcomeAction(t, clicked, welcomeActionTokenResume)

		if !manual.resumeActive || !clicked.resumeActive {
			t.Fatalf("resume picker active: manual=%v clicked=%v", manual.resumeActive, clicked.resumeActive)
		}
		if manual.textarea.Value() != clicked.textarea.Value() {
			t.Fatalf("resume input differs: manual=%q clicked=%q", manual.textarea.Value(), clicked.textarea.Value())
		}
	})

	t.Run("quit emits the same submission", func(t *testing.T) {
		var manualSubmissions []Submission
		manual := newWelcomeTestModel(t, 80, 24, Config{
			ExecuteLine: func(submission Submission) TaskResultMsg {
				manualSubmissions = append(manualSubmissions, submission)
				return TaskResultMsg{}
			},
		})
		var clickedSubmissions []Submission
		clicked := newWelcomeTestModel(t, 80, 24, Config{
			ExecuteLine: func(submission Submission) TaskResultMsg {
				clickedSubmissions = append(clickedSubmissions, submission)
				return TaskResultMsg{}
			},
		})

		manualCmd := submitManualWelcomeCommand(manual, "/quit")
		clickedCmd := clickWelcomeAction(t, clicked, welcomeActionTokenQuit)
		runWelcomeTestCmd(manual, manualCmd)
		runWelcomeTestCmd(clicked, clickedCmd)

		if !reflect.DeepEqual(clickedSubmissions, manualSubmissions) {
			t.Fatalf("clicked submissions = %#v, manual = %#v", clickedSubmissions, manualSubmissions)
		}
		if len(clickedSubmissions) != 1 || clickedSubmissions[0].Text != "/quit" {
			t.Fatalf("quit submissions = %#v, want one /quit", clickedSubmissions)
		}
	})
}

func TestWelcomeMouseHitTestingRejectsInvalidRegionsAndMismatchedRelease(t *testing.T) {
	var submissions []Submission
	model := newWelcomeTestModel(t, 80, 24, Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submissions = append(submissions, submission)
			return TaskResultMsg{}
		},
		WriteClipboardText: func(string) error { return nil },
	})
	connectPoint := welcomeActionMousePoint(t, model, welcomeActionTokenConnect)
	connectLine := welcomeActionContentLine(t, model, welcomeActionTokenConnect)
	clickBounds := model.viewportClickBounds[connectLine]
	if !clickBounds.valid() {
		t.Fatalf("connect row has no cached click bounds: %#v", clickBounds)
	}

	logoPoint := connectPoint
	logoPoint.X = model.mainColumnX() + tuikit.GutterNarrative + maxInt(0, clickBounds.start-8)
	clickWelcomePoint(model, logoPoint)
	if model.isWizardActive() {
		t.Fatal("clicking the logo side opened the connect wizard")
	}

	borderPoint := connectPoint
	borderPoint.X = model.mainColumnX() + tuikit.GutterNarrative + maxInt(0, clickBounds.start-1)
	clickWelcomePoint(model, borderPoint)
	if model.isWizardActive() {
		t.Fatal("clicking outside the action range opened the connect wizard")
	}

	resumePoint := welcomeActionMousePoint(t, model, welcomeActionTokenResume)
	_, _ = model.Update(tea.MouseClickMsg(connectPoint))
	_, _ = model.Update(tea.MouseReleaseMsg(resumePoint))
	if model.isWizardActive() || model.resumeActive || len(submissions) != 0 {
		t.Fatalf("mismatched release triggered action: wizard=%v resume=%v submissions=%#v", model.isWizardActive(), model.resumeActive, submissions)
	}

	_, _ = model.Update(tea.MouseClickMsg(connectPoint))
	rightRelease := connectPoint
	rightRelease.Button = tea.MouseRight
	_, _ = model.Update(tea.MouseReleaseMsg(rightRelease))
	if model.isWizardActive() || len(submissions) != 0 {
		t.Fatal("non-left release triggered a welcome action")
	}
}

func TestWelcomeMouseActionAcceptsX10ReleaseWithoutButtonIdentity(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})
	point := welcomeActionMousePoint(t, model, welcomeActionTokenConnect)

	_, _ = model.Update(tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	_, _ = model.Update(tea.MouseReleaseMsg(point))

	if !model.isWizardActive() {
		t.Fatal("X10-style MouseNone release did not activate the left-pressed welcome action")
	}
}

func TestWelcomeClickExecutesOnceAndTracksViewportOffset(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 80, height: 24},
		{width: 55, height: 20},
		{width: 35, height: 16},
	} {
		logs := make([]string, 24)
		for i := 0; i < 24; i++ {
			logs[i] = "note: retained log"
		}
		model := newWelcomeTestModel(t, size.width, size.height, Config{InitialLogs: logs})
		model.syncViewportContent()
		model.viewport.SetYOffset(1)
		model.setViewportFollowState(viewportPinnedHistory)

		point := welcomeActionMousePoint(t, model, welcomeActionTokenConnect)
		_, _ = model.Update(tea.MouseClickMsg(point))
		_, _ = model.Update(tea.MouseReleaseMsg(point))
		_, _ = model.Update(tea.MouseReleaseMsg(point))
		if !model.isWizardActive() {
			t.Fatalf("%dx%d offset click did not open connect wizard", size.width, size.height)
		}
	}
}

func TestWelcomeHeightOnlyResizeRebuildsResponsiveLayoutAndClickTargets(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})
	if model.viewport.Height() < welcomeStandardMinHeight {
		t.Fatalf("initial viewport height = %d, want standard welcome layout", model.viewport.Height())
	}
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, welcomeRelayLogoASCII[0]) {
		t.Fatalf("initial viewport missing standard welcome logo\n%s", plain)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	model = updated.(*Model)
	if model.viewport.Height() >= welcomeStandardMinHeight {
		t.Fatalf("resized viewport height = %d, want fallback welcome layout", model.viewport.Height())
	}
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, welcomeRelayLogoASCII[0]) || strings.Contains(plain, "┌") {
		t.Fatalf("height-only resize reused the standard welcome layout\n%s", plain)
	}
	for _, action := range welcomeActions {
		if !strings.Contains(plain, action.label) {
			t.Fatalf("height-only resize lost action %q\n%s", action.label, plain)
		}
	}

	_ = clickWelcomeAction(t, model, welcomeActionTokenResume)
	if !model.resumeActive {
		t.Fatal("height-only resize left stale welcome click targets")
	}
}

func TestWelcomeKeyboardDoesNotActivateActions(t *testing.T) {
	var submissions []Submission
	model := newWelcomeTestModel(t, 80, 24, Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submissions = append(submissions, submission)
			return TaskResultMsg{}
		},
	})
	for _, keyName := range []string{"tab", "up", "down", "enter"} {
		_, _ = model.Update(keyPress(keyName))
	}
	if model.isWizardActive() || model.resumeActive || len(submissions) != 0 {
		t.Fatalf("keyboard activated welcome action: wizard=%v resume=%v submissions=%#v", model.isWizardActive(), model.resumeActive, submissions)
	}
	_, _ = model.Update(keyPress("x"))
	if got := model.textarea.Value(); got != "x" {
		t.Fatalf("composer input = %q, want keyboard focus to remain in composer", got)
	}
}

func TestAcceptedOrdinarySubmissionDismissesWelcomeAndKeepsInitialLogs(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{
		InitialLogs: []string{"note: sandbox ready"},
	})
	updated, _ := model.submitInteractiveLine("inspect the repository", "inspect the repository", nil)
	model = updated.(*Model)

	if got := len(model.doc.FindByKind(BlockWelcome)); got != 0 {
		t.Fatalf("welcome blocks after submit = %d, want 0", got)
	}
	rows := model.doc.RenderAll(BlockRenderContext{
		Width:  77,
		Height: 19,
		Theme:  tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
	plainRows := rowPlainTexts(rows)
	plain := strings.Join(plainRows, "\n")
	if strings.Contains(plain, welcomeRelayLogoASCII[0]) ||
		strings.Contains(plain, welcomeRelayCompactASCII[0]) ||
		strings.Contains(plain, welcomeActions[0].label) {
		t.Fatalf("submitted workspace still contains welcome content\n%s", plain)
	}
	if !strings.Contains(plain, "sandbox ready") || !strings.Contains(plain, "inspect the repository") {
		t.Fatalf("submitted workspace lost logs or user message\n%s", plain)
	}
	if len(plainRows) == 0 || strings.TrimSpace(plainRows[0]) == "" {
		t.Fatalf("welcome cleanup left leading blank rows: %#v", plainRows)
	}
}

func TestAttachmentOnlySubmissionDismissesWelcome(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})
	updated, _ := model.submitInteractiveLine("", "[image #1]", []Attachment{{Name: "image.png"}})
	model = updated.(*Model)
	if got := len(model.doc.FindByKind(BlockWelcome)); got != 0 {
		t.Fatalf("welcome blocks after attachment submit = %d, want 0", got)
	}
}

func TestLocalSlashWelcomeLifecycleFollowsTranscriptAppend(t *testing.T) {
	for _, token := range []string{
		welcomeActionTokenConnect,
		welcomeActionTokenResume,
	} {
		model := newWelcomeTestModel(t, 80, 24, Config{
			ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} },
		})
		_ = clickWelcomeAction(t, model, token)
		if got := len(model.doc.FindByKind(BlockWelcome)); got != 1 {
			t.Fatalf("%s click left %d welcome blocks, want 1", token, got)
		}
	}

	quitModel := newWelcomeTestModel(t, 80, 24, Config{
		ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} },
	})
	_ = clickWelcomeAction(t, quitModel, welcomeActionTokenQuit)
	if got := len(quitModel.doc.FindByKind(BlockWelcome)); got != 0 {
		t.Fatalf("quit transcript append left %d welcome blocks, want 0", got)
	}
	if plain := strings.Join(rowPlainTexts(quitModel.doc.RenderAll(quitModel.blockRenderContext(77))), "\n"); !strings.Contains(plain, "/quit") {
		t.Fatalf("quit click did not append its normal transcript line\n%s", plain)
	}

	called := false
	model := newWelcomeTestModel(t, 80, 24, Config{
		ExecuteLine: func(Submission) TaskResultMsg {
			called = true
			return TaskResultMsg{}
		},
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	cmd := clickWelcomeAction(t, model, welcomeActionTokenQuit)
	runWelcomeTestCmd(model, cmd)
	if called {
		t.Fatal("rejected running slash submission executed")
	}
	if got := len(model.doc.FindByKind(BlockWelcome)); got != 1 {
		t.Fatalf("rejected submission left %d welcome blocks, want 1", got)
	}
}

func TestSlashTranscriptOutputDismissesWelcomeAndKeepsInitialLogs(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{
		InitialLogs: []string{"note: sandbox ready"},
	})
	if got := len(model.doc.FindByKind(BlockWelcome)); got != 1 {
		t.Fatalf("welcome blocks before status output = %d, want 1", got)
	}

	updated, _ := model.handleSlashCommandResultMsg(SlashCommandResultMsg{
		Result: controlprompt.SlashCommandResult{
			Kind: controlprompt.SlashCommandResultStatus,
			Status: controlstatus.StatusSnapshot{
				Session: controlstatus.StatusSession{ID: "s-welcome"},
			},
		},
	})
	model = updated.(*Model)

	if got := len(model.doc.FindByKind(BlockWelcome)); got != 0 {
		t.Fatalf("welcome blocks after status output = %d, want 0", got)
	}
	plainRows := rowPlainTexts(model.doc.RenderAll(model.blockRenderContext(77)))
	plain := strings.Join(plainRows, "\n")
	if !strings.Contains(plain, "sandbox ready") || !strings.Contains(plain, "Status") || !strings.Contains(plain, "s-welcome") {
		t.Fatalf("status append lost initial or slash transcript content\n%s", plain)
	}
	if len(plainRows) == 0 || strings.TrimSpace(plainRows[0]) == "" {
		t.Fatalf("status dismissal left leading welcome spacing: %#v", plainRows)
	}
}

func TestGatewayUserEchoDismissesWelcome(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})
	updated, _ := model.Update(UserMessageMsg{Text: "message accepted by server"})
	model = updated.(*Model)
	if got := len(model.doc.FindByKind(BlockWelcome)); got != 0 {
		t.Fatalf("welcome blocks after gateway echo = %d, want 0", got)
	}
	if !strings.Contains(strings.Join(rowPlainTexts(model.doc.RenderAll(model.blockRenderContext(77))), "\n"), "message accepted by server") {
		t.Fatal("gateway echo did not retain the user message")
	}
}

func TestRenderCompletionTextLineWithoutDetailMatchesOverlayWidth(t *testing.T) {
	model := NewModel(Config{})
	model.width = 79
	model.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)

	line := model.renderCompletionTextLine("short-name", "", false)
	if got := displayColumns(line); got != model.completionOverlayRenderedRowWidth() {
		t.Fatalf("row width = %d, want %d: %q", got, model.completionOverlayRenderedRowWidth(), line)
	}
}

func newWelcomeTestModel(t *testing.T, width int, height int, cfg Config) *Model {
	t.Helper()
	cfg.ShowWelcomeCard = true
	cfg.NoColor = true
	cfg.NoAnimation = true
	if cfg.Commands == nil {
		cfg.Commands = DefaultCommands()
	}
	if cfg.Wizards == nil {
		cfg.Wizards = DefaultWizards()
	}
	model := NewModel(cfg)
	model.Init()
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(*Model)
}

func submitManualWelcomeCommand(model *Model, command string) tea.Cmd {
	model.setInputText(command)
	model.syncTextareaFromInput()
	_, cmd := model.Update(keyPress("enter"))
	return cmd
}

func welcomeActionContentLine(t *testing.T, model *Model, token string) int {
	t.Helper()
	for i, got := range model.viewportClickTokens {
		if got == token {
			return i
		}
	}
	t.Fatalf("viewport click tokens missing %q: %#v", token, model.viewportClickTokens)
	return -1
}

func welcomeActionMousePoint(t *testing.T, model *Model, token string) tea.Mouse {
	t.Helper()
	contentLine := welcomeActionContentLine(t, model, token)
	if contentLine >= len(model.viewportClickBounds) {
		t.Fatalf("viewport click bounds missing line %d for %q", contentLine, token)
	}
	clickBounds := model.viewportClickBounds[contentLine]
	if !clickBounds.valid() {
		t.Fatalf("no click bounds for %q in %q", token, model.viewportPlainLines[contentLine])
	}
	y := contentLine - model.viewportVisibleOffset() - maxInt(0, model.frameTopTrim)
	if y < 0 || y >= model.viewport.Height() {
		t.Fatalf("welcome action %q line %d is outside viewport offset %d height %d", token, contentLine, model.viewportVisibleOffset(), model.viewport.Height())
	}
	return tea.Mouse{
		Button: tea.MouseLeft,
		X:      model.mainColumnX() + tuikit.GutterNarrative + clickBounds.end - 1,
		Y:      y,
	}
}

func clickWelcomeAction(t *testing.T, model *Model, token string) tea.Cmd {
	t.Helper()
	return clickWelcomePoint(model, welcomeActionMousePoint(t, model, token))
}

func clickWelcomePoint(model *Model, point tea.Mouse) tea.Cmd {
	_, _ = model.Update(tea.MouseClickMsg(point))
	_, cmd := model.Update(tea.MouseReleaseMsg(point))
	return cmd
}

func runWelcomeTestCmd(model *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	findAndRunTaskResult(cmd(), model)
}

func rowPlainTexts(rows []RenderedRow) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.Plain
	}
	return out
}

func rowPlainTextsForWelcomePanel(rows []welcomePanelRow) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.plain
	}
	return out
}
