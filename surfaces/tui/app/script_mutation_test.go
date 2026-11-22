package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestScriptMutationRowsUseFinalDiffCounts(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%v", replay), func(t *testing.T) {
			events := consecutiveScriptEvents()[:1]
			for _, name := range []string{"Write", "Patch"} {
				path := strings.ToLower(name) + "-with-a-long-file-name.txt"
				start := &session.Event{Type: session.EventTypeToolCall, SessionID: "fixture", Visibility: session.VisibilityMirror,
					Tool: &session.EventTool{
						ID: name, Name: name, Status: "pending", Input: map[string]any{"path": path},
						Parent: &session.EventParentTool{CallID: "script1", Name: "js_exec"},
					},
				}
				final := session.CloneEvent(start)
				final.Type = session.EventTypeToolResult
				final.Tool.Status = "completed"
				final.Tool.Output = map[string]any{"path": path, "changed": true}
				final.Meta = map[string]any{"caelis": map[string]any{"runtime": map[string]any{
					"tool": map[string]any{"added_lines": 2, "removed_lines": 1, "hunk": "@@ -1 +1,2 @@"},
				}}}
				events = append(events, start, final)
			}
			events = append(events, consecutiveScriptEvents()[5])
			m, block := groupedCanonicalModel(t, events, "main", replay)
			for _, width := range []int{24, 60} {
				ctx := m.blockRenderContext(width)
				ctx.Theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
				rows := block.Render(ctx)
				if len(rows) != 3 || !strings.HasPrefix(rows[0].Plain, "• js_exec") {
					t.Fatal(joinRenderedPlain(rows))
				}
				for i, name := range []string{"write", "patch"} {
					row := rows[i+1]
					prefix := name
					if width == 24 {
						prefix = name[:3]
					}
					if !strings.Contains(row.Plain, "Edit "+prefix) || !strings.HasSuffix(row.Plain, " +2 -1") || displayColumns(row.Plain) > width {
						t.Fatalf("width %d: %q", width, row.Plain)
					}
					if got := ansiTextForForeground(t, row.Styled, ctx.Theme.DiffAddFg); got != "+2" {
						t.Fatalf("addition style applied to %q, want +2", got)
					}
					if got := ansiTextForForeground(t, row.Styled, ctx.Theme.DiffRemoveFg); got != "-1" {
						t.Fatalf("removal style applied to %q, want -1", got)
					}
				}
			}
			block.(*MainACPTurnBlock).AddApprovalReviewEvent("Patch", "Patch", "patch-with-a-long-file-name.txt", "approved", "")
			ctx := m.blockRenderContext(60)
			ctx.Theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
			found := false
			for _, row := range block.Render(ctx) {
				if strings.Contains(row.Plain, "Edit patch-") {
					found = true
					if !strings.Contains(row.Plain, "+2 -1 [approved]") ||
						!strings.Contains(ansiTextForForeground(t, row.Styled, ctx.Theme.DiffAddFg), "+2") ||
						ansiTextForForeground(t, row.Styled, ctx.Theme.DiffRemoveFg) != "-1" {
						t.Fatalf("approval displaced the diff colors: %q", row.Styled)
					}
				}
			}
			if !found {
				t.Fatal("approved Patch row missing")
			}
		})
	}
}
