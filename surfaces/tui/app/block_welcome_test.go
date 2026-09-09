package tuiapp

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

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
	}{
		{name: "fullscreen workspace", width: 190, height: 50, wantLogoRow: "█"},
		{name: "100x30 terminal workspace", width: 97, height: 25, wantLogoRow: "█"},
		{name: "80x24 terminal workspace", width: 77, height: 19, wantLogoRow: "█"},
		{name: "55x20 terminal workspace", width: 52, height: 15, wantLogoRow: "█"},
		{name: "35x16 terminal workspace", width: 33, height: 11, wantLogoRow: "CAELIS"},
	}
	for _, tc := range cases {
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
				t.Fatalf("welcome missing selected brand row %q\n%s", tc.wantLogoRow, plain)
			}
			if !strings.Contains(plain, "CAELIS") && !strings.Contains(plain, welcomeWordmarkASCII[0]) {
				t.Fatalf("welcome missing brand treatment\n%s", plain)
			}
			if got := strings.Count(plain, "v9.9.9"); got != 1 {
				t.Fatalf("version count = %d, want 1\n%s", got, plain)
			}
			if !strings.Contains(plain, "type / for commands") {
				t.Fatalf("default notice missing\n%s", plain)
			}

			if strings.Contains(plain, "Click an action") {
				t.Fatalf("welcome still contains removed click hint\n%s", plain)
			}
			for _, action := range welcomeActions {
				if strings.Contains(plain, action.command) {
					t.Fatalf("welcome retained command hint %q\n%s", action.command, plain)
				}
			}
			for _, border := range []string{"╭", "╮", "╰", "╯", "│"} {
				if strings.Contains(plain, border) {
					t.Fatalf("welcome retained outer frame %q\n%s", border, plain)
				}
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

func TestWelcomeAnnouncementHeightFollowsContent(t *testing.T) {
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	ctx := BlockRenderContext{Width: 80, Height: 24, Theme: theme}
	empty := buildWelcomePanel(ctx, "v1.2.3", newWelcomeAnnouncement(""))
	short := buildWelcomePanel(ctx, "v1.2.3", newWelcomeAnnouncement("New workspace flow available."))
	long := buildWelcomePanel(ctx, "v1.2.3", newWelcomeAnnouncement(strings.Repeat("Long announcement. ", 40)))
	if len(short) != len(empty) || len(long) <= len(short) || len(long) > ctx.Height {
		t.Fatalf("heights empty=%d short=%d long=%d", len(empty), len(short), len(long))
	}
	for _, rows := range [][]welcomePanelRow{empty, short, long} {
		plain := strings.Join(rowPlainTextsForWelcomePanel(rows), "\n")
		if strings.Count(plain, "v1.2.3") != 1 {
			t.Fatalf("missing version: %s", plain)
		}
		if strings.Count(plain, strings.Repeat("─", 64)) != 1 {
			t.Fatalf("missing shared divider: %s", plain)
		}
	}
}

func TestWelcomeUpdateAnnouncementUsesWarningEmphasisAndFitsCompactColumn(t *testing.T) {
	theme := tuikit.ResolveThemeFromOptions(false, colorprofile.TrueColor)
	styles := newWelcomePanelStyles(theme.Tokens())
	announcement := formatUpdateAnnouncement("1.2.0")
	if got, want := announcement.plainText(), "v1.2.0 available, press ctrl+u to update"; got != want {
		t.Fatalf("update announcement = %q, want %q", got, want)
	}

	rows := welcomeNoticeCells(announcement, 18, 3, styles)
	wantRows := []string{
		"v1.2.0 available,",
		"press ctrl+u to",
		"update",
	}
	if got := rowPlainTextsForWelcomePanel(rows); len(got) != len(wantRows) {
		t.Fatalf("compact update rows = %#v, want %#v", got, wantRows)
	} else {
		for i, want := range wantRows {
			if got := strings.TrimRight(got[i], " "); got != want {
				t.Fatalf("compact update row %d = %q, want %q", i, got, want)
			}
		}
	}
	for i, row := range rows {
		if got := displayColumns(row.plain); got != 18 {
			t.Fatalf("plain update row %d width = %d, want 18: %q", i, got, row.plain)
		}
		if got := displayColumns(ansi.Strip(row.styled)); got != 18 {
			t.Fatalf("styled update row %d width = %d, want 18: %q", i, got, ansi.Strip(row.styled))
		}
	}

	warningParts := make([]string, 0, len(rows))
	mutedParts := make([]string, 0, len(rows))
	for _, row := range rows {
		if text := ansiTextForForeground(t, row.styled, theme.Warning); text != "" {
			warningParts = append(warningParts, text)
		}
		if text := ansiTextForForeground(t, row.styled, theme.MutedText); text != "" {
			mutedParts = append(mutedParts, text)
		}
	}
	if got := strings.Join(warningParts, " "); got != "v1.2.0 available" {
		t.Fatalf("warning emphasis = %q, want version availability only", got)
	}
	if got := strings.Join(mutedParts, " "); got != ", press ctrl+u to update" {
		t.Fatalf("muted update detail = %q, want action copy", got)
	}
}

func TestWelcomeAnnouncementAllowsCustomEmphasizedPrefix(t *testing.T) {
	theme := tuikit.ResolveThemeFromOptions(false, colorprofile.TrueColor)
	styles := newWelcomePanelStyles(theme.Tokens())
	announcement := newWelcomeAnnouncementWithEmphasis("New: workspace flow", "New:")
	if got := announcement.plainText(); got != "New: workspace flow" {
		t.Fatalf("announcement text = %q, want caller-owned punctuation", got)
	}
	rows := welcomeNoticeCells(announcement, 34, 3, styles)
	styled := rows[0].styled
	if got := ansiTextForForeground(t, styled, theme.Warning); got != "New:" {
		t.Fatalf("warning emphasis = %q, want custom leading segment", got)
	}
	if got := ansiTextForForeground(t, styled, theme.MutedText); got != "workspace flow" {
		t.Fatalf("muted announcement detail = %q, want custom suffix", got)
	}
}

func TestWelcomeGenericAnnouncementsRemainMuted(t *testing.T) {
	theme := tuikit.ResolveThemeFromOptions(false, colorprofile.TrueColor)
	styles := newWelcomePanelStyles(theme.Tokens())
	for _, tc := range []struct {
		name         string
		announcement welcomeAnnouncement
	}{
		{name: "default", announcement: newWelcomeAnnouncement("")},
		{name: "configured", announcement: newWelcomeAnnouncement("New workspace flow available.")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := welcomeNoticeCells(tc.announcement, 34, 3, styles)
			var warningParts []string
			var mutedParts []string
			for _, row := range rows {
				if text := ansiTextForForeground(t, row.styled, theme.Warning); text != "" {
					warningParts = append(warningParts, text)
				}
				if text := ansiTextForForeground(t, row.styled, theme.MutedText); text != "" {
					mutedParts = append(mutedParts, text)
				}
			}
			if got := strings.Join(warningParts, " "); got != "" {
				t.Fatalf("warning emphasis = %q, want none", got)
			}
			if got, want := strings.Join(mutedParts, " "), tc.announcement.plainText(); got != want {
				t.Fatalf("muted announcement = %q, want %q", got, want)
			}
		})
	}
}

func TestWelcomeActionRowsOmitCommandHints(t *testing.T) {
	t.Parallel()
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	styles := newWelcomePanelStyles(theme.Tokens())
	const width = 52
	rows := buildWelcomeActionRows(width, styles)
	for i, row := range rows {
		action := welcomeActions[i]
		if strings.Contains(row.plain, action.command) {
			t.Fatalf("action %q row retained command hint %q: %q", action.label, action.command, row.plain)
		}
		if got := displayColumns(row.plain); got != width {
			t.Fatalf("action %q row width = %d, want %d", action.label, got, width)
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
			clicked := sliceByDisplayColumns(row.Plain, row.ClickStartCol, row.ClickEndCol)
			if !strings.HasPrefix(clicked, welcomeActionMarker+action.label) {
				t.Fatalf("action bounds include another column: %q", clicked)
			}
			if row.ClickEndCol > size.width {
				t.Fatalf("click bounds overflow: %#v", row)
			}

		}
		want := []string{
			welcomeActionTokenResume,
			welcomeActionTokenModel,
			welcomeActionTokenConnect,
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
		if len(rows) != len(welcomeActions)+1 {
			t.Fatalf("width %d rows = %d, want %d action rows", width, len(rows), len(welcomeActions))
		}
		for i, row := range rows[1:] {
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
			label := fitWelcomeText(welcomeActionMarker+action.label, minInt(welcomePanelMaxWidth, model.viewport.Width()))
			if !strings.Contains(plain, strings.TrimSpace(label)) {
				t.Fatalf("%dx%d frame missing responsive action label %q\n%s", size.width, size.height, label, plain)
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

	t.Run("model opens the same picker", func(t *testing.T) {
		manual := newWelcomeTestModel(t, 80, 24, Config{})
		clicked := newWelcomeTestModel(t, 80, 24, Config{})

		submitManualWelcomeCommand(manual, "/model")
		_ = clickWelcomeAction(t, clicked, welcomeActionTokenModel)

		if !manual.slashArgActive || !clicked.slashArgActive {
			t.Fatalf("model picker active: manual=%v clicked=%v", manual.slashArgActive, clicked.slashArgActive)
		}
		if manual.slashArgCommand != "model" || clicked.slashArgCommand != "model" {
			t.Fatalf("model picker command: manual=%q clicked=%q, want model",
				manual.slashArgCommand, clicked.slashArgCommand)
		}
		if manual.textarea.Value() != clicked.textarea.Value() || clicked.textarea.Value() != "/model " {
			t.Fatalf("model input differs: manual=%q clicked=%q, want /model ",
				manual.textarea.Value(), clicked.textarea.Value())
		}
	})

	t.Run("quit uses the same immediate lifecycle path", func(t *testing.T) {
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

		if manualCmd == nil || clickedCmd == nil || !manual.quit || !clicked.quit {
			t.Fatalf("immediate quit state = manual(cmd:%v quit:%v) clicked(cmd:%v quit:%v)", manualCmd != nil, manual.quit, clickedCmd != nil, clicked.quit)
		}
		if len(manualSubmissions) != 0 || len(clickedSubmissions) != 0 {
			t.Fatalf("quit reached asynchronous ExecuteLine: manual=%#v clicked=%#v", manualSubmissions, clickedSubmissions)
		}
	})
}

func TestWelcomeMouseHitTestingUsesActionColumnAndRejectsMismatchedRelease(t *testing.T) {
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

	rowStartPoint := connectPoint
	rowStartPoint.X = model.mainColumnX() + tuikit.GutterNarrative + clickBounds.start
	clickWelcomePoint(model, rowStartPoint)
	if !model.isWizardActive() {
		t.Fatal("clicking the start of the action row did not open the connect wizard")
	}

	model = newWelcomeTestModel(t, 80, 24, Config{})
	connectPoint = welcomeActionMousePoint(t, model, welcomeActionTokenConnect)
	connectLine = welcomeActionContentLine(t, model, welcomeActionTokenConnect)
	clickBounds = model.viewportClickBounds[connectLine]
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

func TestWelcomeMouseHitTestingRejectsRightOutsidePaddedRows(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 80, height: 24},
		{width: 35, height: 16},
	} {
		model := newWelcomeTestModel(t, size.width, size.height, Config{})
		inside := welcomeActionMousePoint(t, model, welcomeActionTokenQuit)
		contentLine := welcomeActionContentLine(t, model, welcomeActionTokenQuit)
		clickBounds := model.viewportClickBounds[contentLine]
		outside := inside
		outside.X = model.mainColumnX() + tuikit.GutterNarrative + clickBounds.end + 3

		clickWelcomePoint(model, outside)
		if model.quit {
			t.Fatalf("%dx%d click right of padded /quit row triggered Quit", size.width, size.height)
		}

		_, _ = model.Update(tea.MouseClickMsg(outside))
		_, _ = model.Update(tea.MouseReleaseMsg(inside))
		if model.quit {
			t.Fatalf("%dx%d outside press and inside release triggered Quit", size.width, size.height)
		}
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
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.ContainsAny(plain, "█▀▄") {
		t.Fatalf("initial viewport missing responsive welcome logo\n%s", plain)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	model = updated.(*Model)
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.ContainsAny(plain, "█▀▄") {
		t.Fatalf("height-only resize reused the standard welcome layout\n%s", plain)
	}
	if strings.Contains(plain, "type / for commands") {
		t.Fatal("short terminal must prioritize identity and actions over the default hint")
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
	if strings.Contains(plain, welcomeWordmarkASCII[0]) ||
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
		welcomeActionTokenModel,
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

func TestWelcomeAnnouncementPhysicalFrames(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {80, 24}, {55, 20}, {35, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			model := newWelcomeTestModel(t, size[0], size[1], Config{Version: "dev", Workspace: "/workspace/caelis"})
			var frames []string
			for _, notice := range []string{"", strings.Repeat("公告示例：版本更新与工作区提示。", 30), ""} {
				model.setWelcomeNotice(notice)
				frames = append(frames, model.View().Content)
			}
			if !strings.Contains(ansi.Strip(frames[1]), "…") {
				t.Fatalf("long announcement has no truncation marker\n%s", ansi.Strip(frames[1]))
			}
			terminal := vt.NewSafeEmulator(size[0], size[1])
			t.Cleanup(func() { _ = terminal.Close() })
			for i, output := range renderFullscreenFramesForTest(t, size[0], size[1], frames...) {
				if _, err := terminal.Write([]byte(output)); err != nil {
					t.Fatal(err)
				}
				if got, want := trimPhysicalFramePadding(ansi.Strip(terminal.Render())), trimPhysicalFramePadding(ansi.Strip(frames[i])); got != want {
					t.Fatalf("physical frame %d differs\ngot:\n%s\nwant:\n%s", i, got, want)
				}
			}
			if frames[0] != frames[2] {
				t.Fatalf("clearing the announcement did not restore the original frame\nbefore:\n%s\nafter:\n%s", ansi.Strip(frames[0]), ansi.Strip(frames[2]))
			}
		})
	}
}
