package tuiapp

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestRenderSlashArgListUsesWizardHintInsteadOfInternalConnectPayload(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	def := connectModelWizard()
	model.wizard = &wizardRuntime{
		def:       &def,
		stepIndex: 5,
		state:     map[string]string{},
	}
	model.slashArgActive = true
	model.slashArgCommand = "connect-context:" + connectwizard.ConnectWizardState{
		Provider:       "minimax",
		BaseURL:        "https://api.minimaxi.com/anthropic",
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
		TokenRef:       "sk-secret",
		Model:          "MiniMax-M2.7-highspeed",
	}.EncodeCompletionState()
	model.slashArgCandidates = []SlashArgCandidate{{
		Value:   "204800",
		Display: "204800",
		Detail:  "context window tokens",
	}}

	rendered := model.renderSlashArgList()
	if strings.Contains(rendered, "sk-secret") {
		t.Fatalf("rendered slash arg list leaked api key: %q", rendered)
	}
	if strings.Contains(rendered, "/connect context_window_tokens") || strings.Contains(rendered, "/connect provider") {
		t.Fatalf("rendered slash arg list = %q, should not show wizard step header", rendered)
	}
	if !strings.Contains(rendered, "204800") || !strings.Contains(rendered, "context window tokens") {
		t.Fatalf("rendered slash arg list = %q, want candidate text and detail", rendered)
	}
}

func TestRenderSlashArgListDistinguishesCandidateTextFromDetail(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.theme.ModalBg = lipgloss.Color("#20283a")
	model.theme.CommandText = lipgloss.Color("#dce3f0")
	model.theme.HelpHintFg = lipgloss.Color("#8190aa")
	model.theme.InvalidateTokens()
	model.themeCacheKey = ""
	def := connectModelWizard()
	model.wizard = &wizardRuntime{
		def:       &def,
		stepIndex: 1,
		state:     map[string]string{"provider": "xiaomi"},
	}
	model.slashArgActive = true
	model.slashArgCommand = "connect-baseurl:xiaomi"
	model.slashArgCandidates = []SlashArgCandidate{
		{Value: "https://api.xiaomimimo.com/v1", Display: "api cn", Detail: "standard endpoint"},
		{Value: "https://token-plan-cn.xiaomimimo.com/v1", Display: "token plan cn", Detail: "coding-plan endpoint"},
	}
	model.slashArgIndex = 0

	rendered := model.renderSlashArgList()
	line := styledLineContaining(t, rendered, "token plan cn")
	plain := ansi.Strip(line)
	candidateX := strings.Index(plain, "token plan cn")
	detailX := strings.Index(plain, "coding-plan endpoint")
	if candidateX < 0 || detailX < 0 {
		t.Fatalf("unselected completion row = %q", plain)
	}
	screen := uv.NewScreenBuffer(lipgloss.Width(line), 1)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(line).Draw(screen, screen.Bounds())
	candidate := screen.CellAt(candidateX, 0)
	detail := screen.CellAt(detailX, 0)
	if candidate == nil || !colorInSet(candidate.Style.Fg, []color.Color{model.theme.CommandText}) {
		t.Fatalf("candidate foreground = %v, want %v", candidate, model.theme.CommandText)
	}
	if detail == nil || !colorInSet(detail.Style.Fg, []color.Color{model.theme.HelpHintFg}) {
		t.Fatalf("detail foreground = %v, want %v", detail, model.theme.HelpHintFg)
	}
}

func TestRenderSlashArgListAlignsProviderHints(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	model.width = 120
	model.slashArgActive = true
	model.slashArgCommand = "connect-provider"
	model.slashArgCandidates = []SlashArgCandidate{
		{Value: "codex", Display: "codex", Detail: "ChatGPT subscription models through Codex"},
		{Value: "grok", Display: "grok", Detail: "Grok models through an eligible xAI subscription"},
		{Value: "openai-compatible", Display: "openai-compatible", Detail: "OpenAI-compatible proxy or self-hosted endpoint"},
	}
	model.slashArgIndex = 0

	rendered := ansi.Strip(model.renderSlashArgList())
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	codex := lineContaining(lines, "codex")
	grok := lineContaining(lines, "Grok models")
	compat := lineContaining(lines, "openai-compatible")
	if codex == "" || grok == "" || compat == "" {
		t.Fatalf("renderSlashArgList() = %q, want provider rows", rendered)
	}
	codexHint := strings.Index(codex, "ChatGPT")
	grokHint := strings.Index(grok, "Grok models")
	compatHint := strings.Index(compat, "OpenAI-compatible")
	if codexHint < 0 || grokHint < 0 || compatHint < 0 || codexHint != grokHint || grokHint != compatHint {
		t.Fatalf("provider hint columns = %d, %d, %d, want aligned\n%s", codexHint, grokHint, compatHint, rendered)
	}
}

func TestRenderSlashArgListNarrowWidthKeepsANSIIntact(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	model.width = 44
	model.slashArgActive = true
	model.slashArgCommand = "connect"
	model.slashArgCandidates = []SlashArgCandidate{
		{Value: "anthropic-compatible", Display: "anthropic-compatible", Detail: "OpenAI-compatible endpoint with custom base URL"},
		{Value: "openai-compatible", Display: "openai-compatible", Detail: "OpenAI-compatible endpoint with custom base URL"},
	}
	model.slashArgIndex = 0

	rendered := model.renderSlashArgList()
	plain := ansi.Strip(rendered)
	for _, fragment := range []string{"[38;", "[48;", "[0m", "\x1b"} {
		if strings.Contains(plain, fragment) {
			t.Fatalf("renderSlashArgList() plain output leaked ANSI fragment %q: raw=%q plain=%q", fragment, rendered, plain)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		if width := displayColumns(line); width > model.completionOverlayInnerWidth() {
			t.Fatalf("renderSlashArgList() row width = %d, want <= %d: %q", width, model.completionOverlayInnerWidth(), line)
		}
	}
}

func TestRenderSlashArgListUsesWideDisplayForBaseURL(t *testing.T) {
	const baseURL = "https://proxy.example.test/v1/organizations/acme/projects/caelis/openai-compatible"

	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	model.width = 140
	model.slashArgActive = true
	model.slashArgCommand = "connect-baseurl:openai-compatible"
	model.slashArgCandidates = []SlashArgCandidate{
		{Value: baseURL, Display: baseURL, Detail: "default base URL"},
	}
	model.slashArgIndex = 0

	rendered := ansi.Strip(model.renderSlashArgList())
	if !strings.Contains(rendered, baseURL) {
		t.Fatalf("renderSlashArgList() = %q, want full base URL %q", rendered, baseURL)
	}
	if !strings.Contains(rendered, "default base URL") {
		t.Fatalf("renderSlashArgList() = %q, want base URL detail", rendered)
	}
	limit := model.completionOverlayRenderedRowWidth()
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if width := displayColumns(line); width > limit {
			t.Fatalf("renderSlashArgList() row width = %d, want <= %d: %q", width, limit, line)
		}
	}
}

func TestRenderInputBarMasksConnectAPIKeyWithoutDuplicatePrompt(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	def := connectModelWizard()
	model.wizard = &wizardRuntime{
		def:       &def,
		stepIndex: 3,
		state:     map[string]string{"provider": "minimax"},
	}
	model.slashArgActive = true
	model.setInputText("sk-secret")
	model.syncTextareaFromInput()

	rendered := model.renderInputBar()
	if strings.Contains(rendered, "sk-secret") {
		t.Fatalf("rendered input bar leaked api key: %q", rendered)
	}
	if strings.Contains(rendered, "> >") {
		t.Fatalf("rendered input bar duplicated prompt: %q", rendered)
	}
	if strings.Contains(rendered, "/connect") {
		t.Fatalf("rendered input bar leaked /connect prefix: %q", rendered)
	}
}

func TestRenderInputBarHidesConnectPrefixForProviderStep(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	def := connectModelWizard()
	model.wizard = &wizardRuntime{
		def:       &def,
		stepIndex: 0,
		state:     map[string]string{},
	}
	model.slashArgActive = true
	model.setInputText("deepseek")
	model.syncTextareaFromInput()

	rendered := model.renderInputBar()
	if strings.Contains(rendered, "/connect") {
		t.Fatalf("rendered provider step leaked /connect prefix: %q", rendered)
	}
	if !strings.Contains(rendered, "deepseek") {
		t.Fatalf("rendered provider step missing visible query: %q", rendered)
	}
}

func TestRenderInputBarKeepsComposerBackgroundForConnectWizardInput(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	model.width = 80
	model.theme.ComposerBg = lipgloss.Color("#141414")
	model.theme.NoColor = false
	def := connectModelWizard()
	model.wizard = &wizardRuntime{
		def:       &def,
		stepIndex: 0,
		state:     map[string]string{},
	}
	model.slashArgActive = true
	model.setInputText("acp")
	model.syncTextareaFromInput()

	rendered := model.renderInputBar()
	wantPrompt := model.theme.PromptStyle().Background(model.theme.ComposerBg).Render("> ")
	wantInput := model.theme.TextStyle().Background(model.theme.ComposerBg).Render("acp")
	if !strings.Contains(rendered, wantPrompt) {
		t.Fatalf("rendered connect input missing composer background on prompt %q: %q", wantPrompt, rendered)
	}
	if !strings.Contains(rendered, wantInput) {
		t.Fatalf("rendered connect input missing composer background on text %q: %q", wantInput, rendered)
	}
}
