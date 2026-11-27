package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/caelis-labs/caelis/control/uipreferences"
)

func TestTeamEntryPhysicalFrames(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {35, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			service := &subagentDelegationStub{status: subagentTestStatus()}
			model := newWelcomeTestModel(t, size[0], size[1], Config{
				Version: "dev", Workspace: "/workspace/caelis", ControlService: service,
			})
			frames := []string{model.View().Content}
			for _, label := range []string{"Resume Session", "Connect Model", "Switch Model", "Quit"} {
				if !strings.Contains(ansi.Strip(frames[0]), label) {
					t.Fatalf("welcome frame omitted %q:\n%s", label, ansi.Strip(frames[0]))
				}
			}
			if strings.Contains(ansi.Strip(frames[0]), "Configure Team") {
				t.Fatal("welcome retained the Team action")
			}

			for _, ch := range "/sub" {
				_, _ = model.Update(keyPress(string(ch)))
			}
			frames = append(frames, model.View().Content)
			if !strings.Contains(ansi.Strip(frames[1]), "/team (subagent)") {
				t.Fatalf("alias completion frame omitted canonical label:\n%s", ansi.Strip(frames[1]))
			}
			_, cmd := model.Update(subagentSpecialKey(tea.KeyEnter))
			if cmd == nil || model.subagentOverlay == nil {
				t.Fatal("alias completion did not open team configuration")
			}
			_, _ = model.Update(cmd())
			frames = append(frames, model.View().Content)
			if plain := ansi.Strip(frames[2]); !strings.Contains(plain, "/team") || !strings.Contains(plain, "Guardian") {
				t.Fatalf("configuration frame omitted title:\n%s", ansi.Strip(frames[2]))
			}
			g := model.subagentOverlay.geometry
			rows := strings.Split(ansi.Strip(frames[2]), "\n")
			bottom := size[1] - model.bottomSectionHeight()
			headerY := g.y
			if model.overlayUsesBorder() {
				headerY++
			}
			if g.x != inputHorizontalInset || g.y+g.height != bottom ||
				!strings.Contains(rows[headerY], "/team") ||
				g.rows[3] < 0 || !strings.Contains(rows[g.rows[3]], "Guardian") ||
				!strings.Contains(strings.Join(rows[bottom:], "\n"), "> /team ") {
				t.Fatalf("team picker did not paint above the composer at its hit-test coordinates: %+v\n%s", g, strings.Join(rows, "\n"))
			}
			_, _ = model.Update(subagentSpecialKey(tea.KeyEscape))
			for _, ch := range "/ex" {
				_, _ = model.Update(keyPress(string(ch)))
			}
			frames = append(frames, model.View().Content)
			for _, frame := range frames[2:] {
				if strings.Contains(ansi.Strip(frame), "type / for commands") || strings.Contains(ansi.Strip(frame), "Resume Session") {
					t.Fatalf("configuration entry retained stale welcome content:\n%s", ansi.Strip(frame))
				}
			}
			if !strings.Contains(ansi.Strip(frames[3]), "/quit (exit)") {
				t.Fatalf("exit completion frame omitted canonical label:\n%s", ansi.Strip(frames[3]))
			}

			terminal := vt.NewSafeEmulator(size[0], size[1])
			t.Cleanup(func() { _ = terminal.Close() })
			for i, output := range renderFullscreenFramesForTest(t, size[0], size[1], frames...) {
				if _, err := terminal.Write([]byte(output)); err != nil {
					t.Fatal(err)
				}
				got := trimPhysicalFramePadding(ansi.Strip(terminal.Render()))
				want := trimPhysicalFramePadding(ansi.Strip(frames[i]))
				if got != want {
					t.Fatalf("physical frame %d differs:\ngot:\n%s\nwant:\n%s", i, got, want)
				}
				t.Logf("frame %d:\n%s", i, got)
			}
		})
	}
}

func TestTeamPickerUsesMainPaneComposer(t *testing.T) {
	model, _ := newPaneTestModel(t)
	model.cfg.ControlService = &subagentDelegationStub{status: subagentTestStatus()}
	runTeaCmds(t, model, model.setSubagentLayout(uipreferences.Up))
	model.setInputText("saved draft")
	model.syncTextareaFromInput()
	load := model.openSubagentOverlay()
	if load == nil {
		t.Fatal("team overlay did not open")
	}
	_, _ = model.Update(load())
	selectSubagentTestRow(t, model, "handle:orbit")
	_, _ = model.Update(subagentSpecialKey(tea.KeyEnter))
	for _, ch := range "sol" {
		_, _ = model.Update(keyPress(string(ch)))
	}
	frame := model.View()
	geometry := model.subagentOverlay.geometry
	main := model.workspaceLayout().main
	rows := strings.Split(ansi.Strip(frame.Content), "\n")
	if geometry.x != main.x+inputHorizontalInset || geometry.y < main.y ||
		geometry.y+geometry.height != main.y+main.height-model.bottomSectionHeight() ||
		frame.Cursor == nil || !main.contains(frame.Cursor.X, frame.Cursor.Y) ||
		!strings.Contains(rows[frame.Cursor.Y], "> /team sol") {
		t.Fatalf("team picker or search cursor outside main pane: overlay %+v, pane %+v, cursor %+v\n%s", geometry, main, frame.Cursor, strings.Join(rows, "\n"))
	}
	_, _ = model.Update(subagentSpecialKey(tea.KeyEscape))
	_, _ = model.Update(subagentSpecialKey(tea.KeyEscape))
	if model.subagentOverlay != nil || !strings.Contains(ansi.Strip(model.View().Content), "> saved draft") {
		t.Fatal("closing team picker did not restore the composer draft")
	}
}
