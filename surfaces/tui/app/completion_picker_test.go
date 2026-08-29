package tuiapp

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestSlashArgPickerHintHidesModelOperationalRemarks(t *testing.T) {
	candidates := []SlashArgCandidate{
		{
			Value:   "xai@default/xai/grok-4.6",
			Display: "xai/grok-4.6",
			Detail:  "endpoint:xai@default · https://cli-chat-proxy.grok.com/v1 · managed auth",
		},
	}
	if got := slashArgPickerHint("model use", candidates, 0); got != "" {
		t.Fatalf("slashArgPickerHint() = %q, want empty for unique model alias", got)
	}
}

func TestSlashArgPickerHintDisambiguatesDuplicateModelAliases(t *testing.T) {
	candidates := []SlashArgCandidate{
		{
			Value:   "xiaomi@default/xiaomi/mimo-v2.5",
			Display: "xiaomi/mimo-v2.5",
			Detail:  "endpoint:xiaomi@default · https://api.xiaomimimo.com/v1 · managed auth",
		},
		{
			Value:   "xiaomi@token-plan-cn/xiaomi/mimo-v2.5",
			Display: "xiaomi/mimo-v2.5",
			Detail:  "endpoint:xiaomi@token-plan-cn · token-plan-cn · https://token-plan-cn.xiaomimimo.com/v1 · managed auth",
		},
	}
	for _, command := range []string{"model", "model use"} {
		if got := slashArgPickerHint(command, candidates, 0); got != "default" {
			t.Fatalf("slashArgPickerHint(%q, default) = %q, want default", command, got)
		}
		if got := slashArgPickerHint(command, candidates, 1); got != "xiaomi@token-plan-cn" {
			t.Fatalf("slashArgPickerHint(%q, token-plan) = %q, want xiaomi@token-plan-cn", command, got)
		}
	}
}

func TestSanitizeCompletionHintDropsOperationalFragments(t *testing.T) {
	got := sanitizeCompletionHint("marketplace", "Official plugins · 12 plugins · https://github.com/example/market")
	if got != "Official plugins · 12 plugins" {
		t.Fatalf("sanitizeCompletionHint() = %q, want description without URL", got)
	}
	if got := sanitizeCompletionHint("xai/grok-4.6", "configured model alias"); got != "" {
		t.Fatalf("sanitizeCompletionHint() = %q, want generic filler dropped", got)
	}
}

func TestRenderSlashArgListHidesModelOperationalRemarks(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 140
	model.slashArgActive = true
	model.slashArgCommand = "model use"
	model.slashArgCandidates = []SlashArgCandidate{
		{
			Value:   "xai@default/xai/grok-4.6",
			Display: "xai/grok-4.6",
			Detail:  "endpoint:xai@default · https://cli-chat-proxy.grok.com/v1 · managed auth",
		},
		{
			Value:   "minimax@default/minimax/minimax-m3",
			Display: "minimax/minimax-m3",
			Detail:  "endpoint:minimax@default · https://api.minimaxi.com/anthropic",
		},
	}
	model.slashArgIndex = 0

	rendered := ansi.Strip(model.renderSlashArgList())
	for _, want := range []string{"xai/grok-4.6", "minimax/minimax-m3"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderSlashArgList() = %q, want %q", rendered, want)
		}
	}
	for _, unwanted := range []string{"endpoint:", "managed auth", "cli-chat-proxy.grok.com", "api.minimaxi.com"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("renderSlashArgList() = %q, should not paint %q", rendered, unwanted)
		}
	}
}

func TestRenderSlashArgListKeepsSelectedHintContrast(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands(), Wizards: DefaultWizards()})
	model.width = 120
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.theme.ModalBg = lipgloss.Color("#20283a")
	model.theme.CommandActive = lipgloss.Color("#30466f")
	model.theme.SelectionBg = model.theme.CommandActive
	model.theme.InvalidateTokens()
	model.themeCacheKey = ""
	model.slashArgActive = true
	model.slashArgCommand = "connect-provider"
	model.slashArgCandidates = []SlashArgCandidate{
		{Value: "codex", Display: "codex", Detail: "ChatGPT subscription models through Codex"},
		{Value: "grok", Display: "grok", Detail: "Grok models through an eligible xAI subscription"},
	}
	model.slashArgIndex = 0

	rendered := model.renderSlashArgList()
	identityWidth := completionTableIdentityWidth([]completionTableRow{
		{identity: "codex", hint: "ChatGPT subscription models through Codex"},
		{identity: "grok", hint: "Grok models through an eligible xAI subscription"},
	}, model.completionRowInnerWidth())
	line := styledLineContaining(t, rendered, "ChatGPT subscription models through Codex")
	plain := ansi.Strip(line)
	identityX := strings.Index(plain, "codex")
	hintX := strings.Index(plain, "ChatGPT")
	if identityX < 0 || hintX < 0 {
		t.Fatalf("selected completion row = %q", plain)
	}
	screen := uv.NewScreenBuffer(lipgloss.Width(line), 1)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(line).Draw(screen, screen.Bounds())
	identity := screen.CellAt(identityX, 0)
	hint := screen.CellAt(hintX, 0)
	if identity == nil || identity.Style.Attrs&uv.AttrBold == 0 {
		t.Fatalf("selected identity cell = %#v, want bold", identity)
	}
	if hint == nil || hint.Style.Attrs&uv.AttrBold != 0 {
		t.Fatalf("selected hint cell = %#v, want non-bold", hint)
	}
	if !colorInSet(identity.Style.Bg, []color.Color{model.theme.CommandActive}) ||
		!colorInSet(hint.Style.Bg, []color.Color{model.theme.CommandActive}) {
		t.Fatalf("selected row backgrounds = identity %v hint %v, want %v", identity.Style.Bg, hint.Style.Bg, model.theme.CommandActive)
	}
	if got := displayColumns(ansi.Cut(plain, identityX, identityX+identityWidth)); got != identityWidth {
		t.Fatalf("selected identity width = %d, want %d", got, identityWidth)
	}
}

func styledLineContaining(t *testing.T, rendered string, needle string) string {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(ansi.Strip(line), needle) {
			return line
		}
	}
	t.Fatalf("rendered output omitted line containing %q: %q", needle, ansi.Strip(rendered))
	return ""
}

func TestRenderSlashCommandListAlignsDescriptions(t *testing.T) {
	model := NewModel(Config{Commands: []string{"help", "connect", "status"}})
	model.width = 120
	model.slashCandidates = []string{"/help", "/connect", "/status"}
	model.slashIndex = 0

	rendered := ansi.Strip(model.renderSlashCommandList())
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	help := lineContaining(lines, "/help")
	connect := lineContaining(lines, "/connect")
	if help == "" || connect == "" {
		t.Fatalf("renderSlashCommandList() = %q, want command rows", rendered)
	}
	helpHint := strings.Index(help, "Show commands")
	connectHint := strings.Index(connect, "Connect a model")
	if helpHint < 0 || connectHint < 0 || helpHint != connectHint {
		t.Fatalf("command hint columns = %d and %d, want aligned\n%s", helpHint, connectHint, rendered)
	}
}
