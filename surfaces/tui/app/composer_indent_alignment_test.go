package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/internal/evalharness"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func TestComposerSitsOneColumnPastTranscriptGutter(t *testing.T) {
	t.Parallel()
	for _, noColor := range []bool{true, false} {
		noColor := noColor
		t.Run(fmt.Sprintf("NoColor=%v", noColor), func(t *testing.T) {
			t.Parallel()
			model := NewModel(Config{
				AppName:     "CAELIS",
				Version:     "dev",
				Workspace:   "/tmp/workspace",
				ModelAlias:  "glm-4.5",
				Commands:    DefaultCommands(),
				Wizards:     DefaultWizards(),
				NoColor:     noColor,
				NoAnimation: true,
			})
			updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
			model = updated.(*Model)

			for _, env := range []eventstream.Envelope{
				{
					Kind:      eventstream.KindSessionUpdate,
					SessionID: "sess",
					Final:     true,
					Update: schema.ContentChunk{
						SessionUpdate: schema.UpdateUserMessage,
						Content:       schema.TextContent{Type: "text", Text: "hello user prompt"},
					},
				},
				{
					Kind:      eventstream.KindSessionUpdate,
					SessionID: "sess",
					Update: schema.ToolCall{
						SessionUpdate: schema.UpdateToolCall,
						ToolCallID:    "call-1",
						Title:         "ls -la ~",
						Kind:          schema.ToolKindExecute,
						Status:        schema.ToolStatusInProgress,
						RawInput:      map[string]any{"command": "ls -la ~"},
						Meta:          acpToolNameMeta("RUN_COMMAND"),
					},
				},
				{
					Kind:      eventstream.KindSessionUpdate,
					SessionID: "sess",
					Final:     true,
					Update: schema.ToolCallUpdate{
						SessionUpdate: schema.UpdateToolCallInfo,
						ToolCallID:    "call-1",
						Title:         stringPtr("ls -la ~"),
						Kind:          stringPtr(schema.ToolKindExecute),
						Status:        stringPtr(schema.ToolStatusCompleted),
						RawInput:      map[string]any{"command": "ls -la ~"},
						RawOutput:     map[string]any{"exit_code": 0},
						Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RUN_COMMAND"), "call-1", "ok\n"),
					},
				},
				completedRegressionTurn("sess", ""),
			} {
				updated, _ = model.Update(env)
				model = updated.(*Model)
			}

			wantContentCol := tuikit.GutterNarrative + 1
			if tuikit.InputInset != wantContentCol {
				t.Fatalf("InputInset=%d, want GutterNarrative+1=%d", tuikit.InputInset, wantContentCol)
			}
			if got := model.composerInputColumnOffset(); got != wantContentCol {
				t.Fatalf("composerInputColumnOffset()=%d, want %d", got, wantContentCol)
			}
			if tuikit.StatusInset != tuikit.InputInset {
				t.Fatalf("StatusInset=%d, want InputInset=%d", tuikit.StatusInset, tuikit.InputInset)
			}
			if model.composerChrome().active {
				// Gray bar is intentionally wider than ">": outer < content col.
				if outer := model.composerOuterInset(); outer >= model.composerInputColumnOffset() {
					t.Fatalf("chrome outer=%d contentCol=%d; want outer < content (gray wider than prompt)",
						outer, model.composerInputColumnOffset())
				}
				if pad := model.composerChrome().horizontalInset(); pad <= 0 {
					t.Fatalf("chrome horizontal pad=%d, want > 0 so gray is wider than prompt", pad)
				}
			}

			frame := evalharness.NormalizeFrame(model.View().Content)
			plain := ansi.Strip(frame)
			var transcriptIndent, inputIndent, statusIndent int
			var foundTranscript, foundInput, foundStatus bool
			for _, line := range strings.Split(plain, "\n") {
				trimmed := strings.TrimSpace(line)
				switch {
				case !foundTranscript && (strings.Contains(trimmed, "hello user") || strings.Contains(trimmed, "Ran ls")):
					transcriptIndent = leadingSpaces(line)
					foundTranscript = true
				case !foundInput && strings.Contains(trimmed, "Type a message"):
					inputIndent = leadingSpaces(line)
					foundInput = true
				case !foundStatus && strings.Contains(trimmed, "not configured"):
					statusIndent = leadingSpaces(line)
					foundStatus = true
				}
			}
			if !foundTranscript || !foundInput || !foundStatus {
				t.Fatalf("missing rows transcript=%v input=%v status=%v\n%s", foundTranscript, foundInput, foundStatus, plain)
			}
			if transcriptIndent != tuikit.GutterNarrative {
				t.Fatalf("transcript indent=%d, want GutterNarrative=%d", transcriptIndent, tuikit.GutterNarrative)
			}
			if inputIndent != wantContentCol {
				t.Fatalf("input indent=%d, want GutterNarrative+1=%d", inputIndent, wantContentCol)
			}
			if statusIndent != wantContentCol {
				t.Fatalf("status indent=%d, want InputInset=%d", statusIndent, wantContentCol)
			}
			if inputIndent != transcriptIndent+1 {
				t.Fatalf("input indent=%d, transcript indent=%d; want input one column right of transcript",
					inputIndent, transcriptIndent)
			}
		})
	}
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
		} else {
			break
		}
	}
	return n
}
