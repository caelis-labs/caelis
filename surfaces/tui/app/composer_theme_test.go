package tuiapp

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/vt"
)

func TestThemeSwitchKeepsComposerSurfaceAndTextBackground(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	var frames []string
	var backgrounds []color.Color
	for _, name := range []string{"nord", "dracula", "catppuccin-latte", "catppuccin-mocha"} {
		m.submitThemeCommand("/theme " + name)
		m.setInputText("富文本 diff")
		m.syncTextareaFromInput()
		frames = append(frames, m.View().Content)
		backgrounds = append(backgrounds, colorprofile.ANSI256.Convert(m.theme.ComposerBg))
		if colorInSet(m.theme.ComposerBg, []color.Color{m.theme.AppBg}) {
			t.Fatalf("%s composer has no visible surface", name)
		}
	}
	screen := vt.NewSafeEmulator(80, 24)
	defer screen.Close()
	for i, update := range renderFullscreenFramesForTest(t, 80, 24, frames...) {
		if _, err := screen.Write([]byte(update)); err != nil {
			t.Fatal(err)
		}
		promptRow := -1
		for y := 0; y < 24; y++ {
			cell := screen.CellAt(m.composerInputColumnOffset(), y)
			if cell != nil && cell.Content == ">" {
				promptRow = y
				break
			}
		}
		if promptRow < 1 || promptRow >= 23 {
			t.Fatalf("frame %d missing composer prompt", i)
		}
		// The padding rows, prompt, committed CJK/ASCII text and empty cells
		// all use one background, including after an incremental theme repaint.
		for y := promptRow - 1; y <= promptRow+1; y++ {
			for x := m.composerOuterInset(); x < 80-m.composerOuterInset(); x++ {
				cell := screen.CellAt(x, y)
				// VT continuation cells have no independent style; a wide glyph
				// paints both columns using its leading cell's background.
				if cell != nil && cell.Width == 0 {
					continue
				}
				if cell == nil || !colorInSet(cell.Style.Bg, []color.Color{backgrounds[i]}) {
					t.Fatalf("frame %d composer background mismatch at %d,%d: %#v", i, x, y, cell)
				}
			}
		}
	}
}
