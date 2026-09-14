package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestTeamEntryPhysicalFrames(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {35, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			service := &subagentDelegationStub{status: subagentTestStatus()}
			model := newWelcomeTestModel(t, size[0], size[1], Config{
				Version: "dev", Workspace: "/workspace/caelis", ControlService: service,
			})
			frames := []string{model.View().Content}
			for _, label := range []string{"Resume Session", "Switch Model", "Configure Team", "Connect Model / Agent"} {
				if !strings.Contains(ansi.Strip(frames[0]), label) {
					t.Fatalf("welcome frame omitted %q:\n%s", label, ansi.Strip(frames[0]))
				}
			}
			if strings.Contains(ansi.Strip(frames[0]), "Quit") {
				t.Fatal("welcome retained the Quit action")
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
			if !strings.Contains(ansi.Strip(frames[2]), "Team Configuration") {
				t.Fatalf("configuration frame omitted title:\n%s", ansi.Strip(frames[2]))
			}
			_, _ = model.Update(subagentSpecialKey(tea.KeyEscape))
			for _, ch := range "/ex" {
				_, _ = model.Update(keyPress(string(ch)))
			}
			frames = append(frames, model.View().Content)
			for _, frame := range frames[2:] {
				if strings.Contains(ansi.Strip(frame), "type / for commands") || strings.Contains(ansi.Strip(frame), "Configure Team") {
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
