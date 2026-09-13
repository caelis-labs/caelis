package tuiapp

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestSplitStyledPhysicalLinesPreservesTerminalState(t *testing.T) {
	for _, styled := range []string{
		"plain\n\n  indented\n",
		"\x1b[38;2;10;20;30mwide 正文\ncombining e\u0301 👩‍💻\x1b[m",
		"\x1b[1;31mred\n\x1b[22mnot bold\n\x1b[39mdefault",
		"\x1b[48;5;24;4:3;58:2::10:20:30mbackground\n\nunderline\x1b[m\nplain",
		ansi.SetHyperlink("https://example.com", "id=message") + "link\ncontinued" + ansi.ResetHyperlink() + " plain",
		"\x1b[32m" + ansi.SetHyperlink("https://example.com") + "both\ncontinued" + ansi.ResetHyperlink() + "\x1b[m",
		"\x1b[31mred\x1b[m\nplain\n\x1b[34mblue\x1b[m",
		"\x1b[31mtrailing\n\n",
	} {
		t.Run(fmt.Sprintf("%q", styled), func(t *testing.T) {
			lines := splitStyledPhysicalLines(styled)
			plain := strings.Split(ansi.Strip(styled), "\n")
			if len(lines) != len(plain) {
				t.Fatalf("got %d rows, want %d", len(lines), len(plain))
			}
			continuous := vt.NewSafeEmulator(80, len(lines)+1)
			defer continuous.Close()
			if _, err := continuous.Write([]byte(strings.ReplaceAll(styled, "\n", "\r\n"))); err != nil {
				t.Fatal(err)
			}
			for y, line := range lines {
				if got := ansi.Strip(line); got != plain[y] {
					t.Fatalf("row %d text = %q, want %q", y, got, plain[y])
				}
				// Paint each row from a fresh terminal state, then an unstyled
				// sentinel to detect style or hyperlink leakage into the gutter.
				isolated := vt.NewSafeEmulator(80, 1)
				defer isolated.Close()
				if _, err := isolated.Write([]byte(line + "!")); err != nil {
					t.Fatal(err)
				}
				width := displayColumns(plain[y])
				for x := range width {
					got, want := isolated.CellAt(x, 0), continuous.CellAt(x, y)
					if got == nil || !got.Equal(want) {
						t.Fatalf("cell %d,%d = %#v, want %#v", x, y, got, want)
					}
				}
				sentinel := isolated.CellAt(width, 0)
				if sentinel == nil || sentinel.Content != "!" || !sentinel.Style.IsZero() || !sentinel.Link.IsZero() {
					t.Fatalf("row %d leaked terminal state: %#v", y, sentinel)
				}
			}
		})
	}
}

func TestSplitStyledPhysicalLinesPlainAllocations(t *testing.T) {
	// Plain rows need only the result slice, independent of the number of cells.
	plain := strings.Repeat("plain 正文 e\u0301\n\n  continuation\n", 256)
	want := strings.Split(plain, "\n")
	var got []string
	allocs := testing.AllocsPerRun(5, func() { got = splitStyledPhysicalLines(plain) })
	if !slices.Equal(got, want) {
		t.Fatal("plain physical rows changed")
	}
	if allocs > 1 {
		t.Fatalf("plain split allocated %.0f objects, want only the result slice", allocs)
	}
}

func BenchmarkWrapAgentMessageRows(b *testing.B) {
	for _, size := range []int{8 << 10, 64 << 10, 256 << 10} {
		for _, noColor := range []bool{false, true} {
			b.Run(fmt.Sprintf("%dKiB/no-color=%v", size>>10, noColor), func(b *testing.B) {
				ctx := BlockRenderContext{Theme: tuikit.ResolveThemeWithState(true, noColor, colorprofile.TrueColor)}
				body := strings.Repeat("message body ", size/len("message body "))
				row := renderAgentMessageRow("message", "reviewer[orbit]", body, ctx, "")
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for b.Loop() {
					wrapAgentMessageRows(row, 80)
				}
			})
		}
	}
}
