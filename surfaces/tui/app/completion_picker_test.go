package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
	if got := slashArgPickerHint("model use", candidates, 0); got != "default" {
		t.Fatalf("slashArgPickerHint(default) = %q, want default", got)
	}
	if got := slashArgPickerHint("model use", candidates, 1); got != "xiaomi@token-plan-cn" {
		t.Fatalf("slashArgPickerHint(token-plan) = %q, want xiaomi@token-plan-cn", got)
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
	identityPad := padRightDisplay("codex", identityWidth)
	base := model.theme.CommandActiveStyle().Padding(0, 0).UnsetBold()
	wantIdentity := base.Bold(true).Render(" " + identityPad)
	wantHint := strings.TrimSuffix(base.Render("  ChatGPT"), "\x1b[m")
	if !strings.Contains(rendered, wantIdentity) {
		t.Fatalf("selected identity style missing: raw=%q want=%q", rendered, wantIdentity)
	}
	if !strings.Contains(rendered, wantHint) {
		t.Fatalf("selected hint style missing: raw=%q want=%q", rendered, wantHint)
	}
	flat := model.theme.CommandActiveStyle().Render(identityPad + "  ChatGPT subscription models through Codex")
	if strings.Contains(rendered, flat) {
		t.Fatalf("selected row still uses a flattened CommandActiveStyle paint: %q", rendered)
	}
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
