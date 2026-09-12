package tuiapp

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/vt"
)

const diffThemeFixture = "message.go +3 -2\n@@ -1,4 +1,5 @@\n func message() string {\n-    // old label\n-    return \"old value\"\n+    // new label\n+    return \"new value\"\n+    // added explanation that wraps in the narrow side-by-side panel\n }\n"

func TestRichDiffRenderedThemeColors(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		for _, background := range []color.Color{color.Black, color.White} {
			for _, option := range tuikit.ThemeOptions(profile, false) {
				for _, width := range []int{80, 120} {
					t.Run(fmt.Sprintf("%s/%s/%v/%d", option.Name, profile, background, width), func(t *testing.T) {
						theme := tuikit.ResolveSelectedTheme(option.Name, background, background == color.Black, false, profile)
						rows := renderNumberedACPDiffPanelRows("diff", diffThemeFixture, width, BlockRenderContext{Theme: theme})
						screen := vt.NewSafeEmulator(width, len(rows)+1)
						defer screen.Close()
						var frame strings.Builder
						for _, row := range rows {
							frame.WriteString(tuikit.PaintLineBackground(row.Styled, width, theme.AppBg) + "\r\n")
						}
						if _, err := screen.Write([]byte(frame.String())); err != nil {
							t.Fatal(err)
						}
						seen := make([]bool, 4)
						for y := 1; y < len(rows); y++ {
							for x := 0; x < width; x++ {
								cell := screen.CellAt(x, y)
								if cell == nil {
									continue
								}
								for i, bg := range []color.Color{theme.DiffAddBg, theme.DiffAddStrongBg, theme.DiffRemoveBg, theme.DiffRemoveStrongBg} {
									seen[i] = seen[i] || colorInSet(cell.Style.Bg, []color.Color{bg})
								}
								// Check actual source glyphs, including comments and intraline
								// changes, after ANSI resets, wrapping and base painting.
								if len(cell.Content) != 1 || cell.Content[0] < 'A' || cell.Content[0] > 'z' {
									continue
								}
								bg := cell.Style.Bg
								if bg == nil {
									bg = background
								}
								if cell.Style.Fg == nil || diffRenderedContrast(cell.Style.Fg, bg) < 4.5 {
									t.Fatalf("unreadable source glyph %q at %d,%d: fg=%v bg=%v", cell.Content, x, y, cell.Style.Fg, bg)
								}
							}
						}
						for i, present := range seen {
							if !present {
								t.Errorf("missing rendered diff background %d", i)
							}
						}
					})
				}
			}
		}
	}
}

func TestRichDiffWrappedIntralineStyle(t *testing.T) {
	theme := tuikit.ResolveSelectedTheme("catppuccin-mocha", nil, true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Theme: theme}
	text := strings.Repeat("changed ", 12)
	line := &diffPanelLine{Kind: diffPanelLineAdd, Marker: '+', NewNo: 1, Text: text, Changed: []diffTextSpan{{0, len(text)}}}
	line.StyledText = renderDiffToken(text, 0, theme.TextStyle(), line, ctx)
	rows := renderDiffPanelCell(line, 0, 1, 32, ctx)
	if len(rows) < 2 {
		t.Fatal("fixture must wrap")
	}
	for _, row := range rows[1:] {
		screen := vt.NewSafeEmulator(32, 1)
		if _, err := screen.Write([]byte(row.Styled)); err != nil {
			t.Fatal(err)
		}
		for x, ch := range row.Plain {
			if ch == ' ' {
				continue
			}
			cell := screen.CellAt(x, 0)
			if cell == nil || cell.Style.Fg == nil || !colorInSet(cell.Style.Bg, []color.Color{theme.DiffAddStrongBg}) {
				t.Fatalf("wrapped changed text lost its syntax or intraline background at %d: %#v", x, cell)
			}
		}
		screen.Close()
	}
}

func diffRenderedContrast(a, b color.Color) float64 {
	luminance := func(c color.Color) float64 {
		r, g, b, _ := c.RGBA()
		linear := func(v uint32) float64 {
			x := float64(v) / 65535
			if x <= .04045 {
				return x / 12.92
			}
			return math.Pow((x+.055)/1.055, 2.4)
		}
		return .2126*linear(r) + .7152*linear(g) + .0722*linear(b)
	}
	x, y := luminance(a), luminance(b)
	return (max(x, y) + .05) / (min(x, y) + .05)
}
