package tuiapp

import (
	"fmt"
	"image/color"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestSlashCompletionDescriptionContrast(t *testing.T) {
	const width, height = 120, 32
	const description = "Configure participant profiles and system Agents"
	for _, name := range []string{"catppuccin-mocha", "catppuccin-latte"} {
		t.Run(name, func(t *testing.T) {
			model := newCompletionMouseTestModel(t, width, height, Config{Commands: []string{"status", "team"}})
			model.applyTheme(tuikit.ResolveSelectedTheme(name, nil, true, false, colorprofile.TrueColor))
			model.setInputText("/")
			model.syncTextareaFromInput()
			model.refreshSlashCommands()
			for _, selected := range []bool{false, true} {
				if selected {
					_, _ = model.Update(keyPress("down"))
				}
				frame := model.View().Content
				screen := uv.NewScreenBuffer(width, height)
				screen.Method = ansi.GraphemeWidth
				uv.NewStyledString(frame).Draw(screen, screen.Bounds())
				for y, line := range strings.Split(ansi.Strip(frame), "\n") {
					detail := strings.Index(line, description)
					if detail < 0 {
						continue
					}
					command := strings.Index(line, "/team")
					if command < 0 {
						t.Fatalf("missing /team name on description row %q", line)
					}
					left := screen.CellAt(displayColumns(line[:command]), y)
					right := screen.CellAt(displayColumns(line[:detail]), y)
					wantBackground := model.theme.ModalBg
					if selected {
						wantBackground = model.theme.CommandActiveStyle().GetBackground()
					}
					wantDetail := model.theme.ReadableTextColor(model.theme.CommandSubText, wantBackground)
					if !colorInSet(left.Style.Bg, []color.Color{wantBackground}) || !colorInSet(right.Style.Bg, []color.Color{wantBackground}) ||
						!colorInSet(right.Style.Fg, []color.Color{wantDetail}) || colorInSet(left.Style.Fg, []color.Color{wantDetail}) {
						t.Fatalf("selected=%v: command and description styles = %+v, %+v", selected, left.Style, right.Style)
					}
					break
				}
				if !strings.Contains(ansi.Strip(frame), description) {
					t.Fatalf("missing /team description in frame:\n%s", ansi.Strip(frame))
				}
			}
		})
	}
}

func TestSlashCompletionOrdersCommandsByFunction(t *testing.T) {
	commands := append(DefaultCommands(), "btw", "codex-1")
	slices.Reverse(commands)
	model := newCompletionMouseTestModel(t, 120, 40, Config{Commands: commands})
	model.slashSkillCatalog = []CompletionCandidate{{Value: "lint"}, {Value: "brainstorm"}}
	model.Update(keyPress("/"))

	want := []string{
		"/help",
		"/resume", "/new", "/status", "/compact", "/btw",
		"/model", "/connect", "/disconnect",
		"/team", "/review",
		"/plugin", "/theme", "/experimental",
	}
	if runtime.GOOS == "windows" {
		want = append(want, "/doctor")
	}
	want = append(want, "/quit", "/breeze", "/codex-1", "/orbit", "/zenith", "/brainstorm", "/lint")
	if !slices.Equal(model.slashCandidates, want) {
		t.Fatalf("slash candidates = %q, want %q", model.slashCandidates, want)
	}
}

func TestSlashCompletionGroupsSearchAndInteraction(t *testing.T) {
	const detail = "Show current provider, model, session, sandbox, and store info"
	for _, size := range []struct {
		width, height, gaps int
		fullDetail          bool
	}{{120, 32, 2, true}, {70, 18, 2, false}, {120, 16, 0, true}} {
		width, height := size.width, size.height
		t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
			model := newCompletionMouseTestModel(t, width, height, Config{
				Commands:       []string{"connect", "help", "status", "breeze", "codex-1"},
				CommandDetails: map[string]string{"connect": detail},
				Wizards:        DefaultWizards(),
			})
			model.slashSkillCatalog = []CompletionCandidate{
				{Value: "alpha", Kind: "Skill", Detail: "Available skill"},
				{Value: "context7-mcp", Kind: "Skill", Detail: strings.Repeat("查阅文档 ", 15) + "END OF DESCRIPTION"},
			}
			open := func(query string) {
				model.setInputText(query)
				model.syncTextareaFromInput()
				model.refreshSlashCommands()
			}
			open("/")
			if got, want := model.slashCandidates, []string{"/help", "/status", "/connect", "/theme", "/breeze", "/codex-1", "/alpha", "/context7-mcp"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("candidates = %q, want %q", got, want)
			}

			open("/co")
			frame := model.View()
			if frame.MouseMode != tea.MouseModeAllMotion {
				t.Fatal("slash list does not request hover events")
			}
			body := ansi.Strip(model.renderInputOverlay())
			last := -1
			for _, text := range []string{"Commands", "/connect", "Agents", "/codex-1", "Skills", "/context7-mcp"} {
				index := strings.Index(body, text)
				if index <= last {
					t.Fatalf("%q missing or out of order:\n%s", text, body)
				}
				last = index
			}
			if strings.Contains(body, "Skill ·") || strings.Contains(body, "END OF DESCRIPTION") {
				t.Fatalf("skill description exceeds its available column:\n%s", body)
			}
			if strings.Contains(body, detail) != size.fullDetail {
				t.Fatalf("description visibility does not match the column budget:\n%s", body)
			}

			_, geometry, ok := model.activeCompletionGeometry()
			if !ok {
				t.Fatal("missing completion geometry")
			}
			rows := strings.Split(body, "\n")
			bodyTop := geometry.candidateTop - geometry.top
			title, _, titleFound := strings.Cut(rows[bodyTop], "Commands")
			candidate, _, candidateFound := strings.Cut(rows[bodyTop+1], "/connect")
			if !titleFound || !candidateFound {
				t.Fatalf("Commands heading is not immediately above its first candidate:\n%s", body)
			}
			candidateLine := strings.TrimSpace(strings.Trim(rows[bodyTop+1], "│"))
			if !strings.Contains(ansi.Strip(frame.Content), candidateLine) {
				t.Fatalf("full frame clips the rendered candidate %q", candidateLine)
			}
			if !size.fullDetail && !strings.HasSuffix(candidateLine, "...") {
				t.Fatalf("clipped description has no ellipsis: %q", candidateLine)
			}
			indent := displayColumns(candidate) - displayColumns(title)
			if indent != 2 {
				t.Fatalf("command indent = %d columns, want 2:\n%s", indent, body)
			}
			gaps := 0
			for offset := range geometry.groupedRows {
				row := strings.TrimSpace(strings.Trim(rows[bodyTop+offset], "│"))
				if row != "" && row != "Agents" {
					continue
				}
				if row == "" {
					gaps++
				}
				point := tea.Mouse{X: geometry.left + 3, Y: geometry.candidateTop + offset}
				_, _ = model.Update(tea.MouseMotionMsg(point))
				point.Button = tea.MouseLeft
				_, _ = model.Update(tea.MouseClickMsg(point))
				_, _ = model.Update(tea.MouseReleaseMsg(point))
			}
			if gaps != size.gaps {
				t.Fatalf("group gaps = %d, want %d:\n%s", gaps, size.gaps, body)
			}
			if model.slashIndex != 0 || model.textarea.Value() != "/co" {
				t.Fatal("group heading or gap changed selection or applied a command")
			}
			_, _ = model.Update(keyPress("down"))
			if selected := model.slashCandidates[model.slashIndex]; selected != "/codex-1" {
				t.Fatalf("Down selected %q, want first Agent handle", selected)
			}
			selectedFrame := model.View().Content
			updates := renderFullscreenFramesForTest(t, width, height, frame.Content, selectedFrame)
			assertPhysicalFullscreenFrame(t, width, height, selectedFrame, updates)

			clickCompletionCandidate(t, model, 2)
			if got := model.textarea.Value(); got != "/context7-mcp " {
				t.Fatalf("click in Skills group inserted %q", got)
			}

			open("/con")
			body = ansi.Strip(model.renderInputOverlay())
			if strings.Contains(body, "Agents") || !strings.Contains(body, "Commands") || !strings.Contains(body, "Skills") {
				t.Fatalf("search did not hide only the empty group:\n%s", body)
			}
		})
	}
}
