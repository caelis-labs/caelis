package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestUnicodeNarrativePhysicalFrames(t *testing.T) {
	for _, width := range []int{24, 40, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			const height = 12
			model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
			var raw strings.Builder
			var frames, wants []string
			appendFrame := func(rows []RenderedRow, offset int) {
				styled, plain := make([]string, height), make([]string, height)
				for i := 0; i < height && offset+i < len(rows); i++ {
					row := rows[offset+i]
					assertSafeNarrativeLinks(t, row.Styled)
					if strings.Contains(row.Styled, "\n") || displayColumns(row.Styled) > width {
						t.Fatalf("narrative row exceeds physical width: %q", row.Styled)
					}
					styled[i], plain[i] = row.Styled, row.Plain
				}
				frame, _ := normalizeFullscreenFrameWithTopTrim(strings.Join(styled, "\n"), width, height)
				frames = append(frames, frame)
				wants = append(wants, trimPhysicalFramePadding(strings.Join(plain, "\n")))
			}
			// Promote complete paragraphs from the plain streaming tail into
			// Glamour, then finalize and scroll them off screen with later output.
			for i := range 8 {
				fmt.Fprintf(&raw, "第%d段 [当前执行](https://example.com/服务/执行.go#L195) 结束。\n\n", i)
				rows, _, _ := glamourStreamingNarrativeRowsObservedForKey(
					t.Name(), "narrative", raw.String(), "· ", tuikit.LineStyleAssistant, width, model.theme, nil,
				)
				appendFrame(rows, max(0, len(rows)-height))
			}
			rows := glamourNarrativeRows("narrative", raw.String(), "· ", tuikit.LineStyleAssistant, width, model.theme)
			var visible strings.Builder
			for _, row := range rows {
				visible.WriteString(strings.TrimSpace(row.Plain))
			}
			if strings.Count(visible.String(), "当前执行") != 8 || strings.Count(visible.String(), "结束。") != 8 {
				t.Fatalf("narrative text duplicated or lost: %q", visible.String())
			}
			for i := range height {
				text := fmt.Sprintf("后续输出 %d completed", i)
				rows = append(rows, RenderedRow{Styled: text, Plain: text})
			}
			for offset := 0; offset+height <= len(rows); offset++ {
				appendFrame(rows, offset)
			}
			terminal := vt.NewSafeEmulator(width, height)
			t.Cleanup(func() { _ = terminal.Close() })
			for i, output := range renderFullscreenFramesForTest(t, width, height, frames...) {
				if _, err := terminal.Write([]byte(output)); err != nil {
					t.Fatal(err)
				}
				if got := trimPhysicalFramePadding(ansi.Strip(terminal.Render())); got != wants[i] {
					t.Fatalf("physical frame %d mismatch:\ngot: %q\nwant: %q", i, got, wants[i])
				}
			}
		})
	}
}

func TestLongRichDiffPhysicalFrames(t *testing.T) {
	for _, width := range []int{40, 80, 220} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			const height = 32
			model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
			ctx := BlockRenderContext{Width: width, TermWidth: width, Theme: model.theme}
			var diff strings.Builder
			diff.WriteString("@@ -0,0 +1,229 @@\n")
			for i := range 229 {
				path := "app/gatewayapp/system_agent_runtime.go"
				if i%2 == 0 {
					path = "服务/执行.go"
				}
				fmt.Fprintf(&diff, "+第 %d 行 [当前执行](https://example.com/%s#L195-L274)：%s\n", i, path, strings.Repeat("审批与当前动作。", i%8))
			}
			rows := renderNumberedACPDiffPanelRows("diff", diff.String(), width, ctx)
			for i, row := range rows {
				if strings.Contains(row.Styled, "\n") || displayColumns(row.Styled) > width {
					t.Fatalf("diff row %d exceeds its physical row: %q", i, row.Styled)
				}
				if got := strings.TrimRight(ansi.Strip(row.Styled), " "); got != strings.TrimRight(row.Plain, " ") {
					t.Fatalf("diff row %d changed visible text: got %q, want %q", i, got, row.Plain)
				}
			}
			// Continue with ordinary output after the large diff, then scroll it off
			// screen. Compare physical replay to the plain rows, not ANSI-derived text.
			for i := range height {
				text := fmt.Sprintf("后续输出 %d approved / completed", i)
				rows = append(rows, RenderedRow{Styled: text, Plain: text})
			}
			var frames, wants []string
			for offset := 0; offset+height <= len(rows); offset++ {
				styled, plain := make([]string, height), make([]string, height)
				for i := range styled {
					styled[i], plain[i] = rows[offset+i].Styled, rows[offset+i].Plain
				}
				frame, _ := normalizeFullscreenFrameWithTopTrim(strings.Join(styled, "\n"), width, height)
				frames = append(frames, frame)
				wants = append(wants, trimPhysicalFramePadding(strings.Join(plain, "\n")))
			}
			terminal := vt.NewSafeEmulator(width, height)
			t.Cleanup(func() { _ = terminal.Close() })
			for i, output := range renderFullscreenFramesForTest(t, width, height, frames...) {
				if _, err := terminal.Write([]byte(output)); err != nil {
					t.Fatal(err)
				}
				if got := trimPhysicalFramePadding(ansi.Strip(terminal.Render())); got != wants[i] {
					t.Fatalf("physical frame %d mismatch:\ngot: %q\nwant: %q", i, got, wants[i])
				}
			}
		})
	}
}
