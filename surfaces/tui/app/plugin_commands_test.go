package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

// pluginStubService implements only the plugin capability needed by /plugin
// commands.
type pluginStubService struct {
	listFn              func(context.Context) ([]control.PluginSnapshot, error)
	addMarketplaceFn    func(context.Context, string) (control.MarketplaceSnapshot, error)
	listMarketplacesFn  func(context.Context) ([]control.MarketplaceSnapshot, error)
	updateMarketplaceFn func(context.Context, string) (control.MarketplaceSnapshot, error)
	removeMarketplaceFn func(context.Context, string) error
	addPathFn           func(context.Context, string) (control.PluginSnapshot, error)
	installFn           func(context.Context, string) (control.PluginSnapshot, error)
	enableFn            func(context.Context, string) (control.PluginSnapshot, error)
	disableFn           func(context.Context, string) (control.PluginSnapshot, error)
	removeFn            func(context.Context, string) error
	inspectFn           func(context.Context, string) (control.PluginSnapshot, error)
}

var _ control.PluginService = (*pluginStubService)(nil)

func (s *pluginStubService) ListPlugins(ctx context.Context) ([]control.PluginSnapshot, error) {
	if s.listFn != nil {
		return s.listFn(ctx)
	}
	return nil, nil
}
func (s *pluginStubService) AddMarketplace(ctx context.Context, source string) (control.MarketplaceSnapshot, error) {
	if s.addMarketplaceFn != nil {
		return s.addMarketplaceFn(ctx, source)
	}
	return control.MarketplaceSnapshot{}, nil
}
func (s *pluginStubService) ListMarketplaces(ctx context.Context) ([]control.MarketplaceSnapshot, error) {
	if s.listMarketplacesFn != nil {
		return s.listMarketplacesFn(ctx)
	}
	return nil, nil
}
func (s *pluginStubService) UpdateMarketplace(ctx context.Context, name string) (control.MarketplaceSnapshot, error) {
	if s.updateMarketplaceFn != nil {
		return s.updateMarketplaceFn(ctx, name)
	}
	return control.MarketplaceSnapshot{}, nil
}
func (s *pluginStubService) RemoveMarketplace(ctx context.Context, name string) error {
	if s.removeMarketplaceFn != nil {
		return s.removeMarketplaceFn(ctx, name)
	}
	return nil
}
func (s *pluginStubService) AddPluginPath(ctx context.Context, path string) (control.PluginSnapshot, error) {
	if s.addPathFn != nil {
		return s.addPathFn(ctx, path)
	}
	return control.PluginSnapshot{}, nil
}
func (s *pluginStubService) InstallPlugin(ctx context.Context, source string) (control.PluginSnapshot, error) {
	if s.installFn != nil {
		return s.installFn(ctx, source)
	}
	return control.PluginSnapshot{}, nil
}
func (s *pluginStubService) EnablePlugin(ctx context.Context, id string) (control.PluginSnapshot, error) {
	if s.enableFn != nil {
		return s.enableFn(ctx, id)
	}
	return control.PluginSnapshot{}, nil
}
func (s *pluginStubService) DisablePlugin(ctx context.Context, id string) (control.PluginSnapshot, error) {
	if s.disableFn != nil {
		return s.disableFn(ctx, id)
	}
	return control.PluginSnapshot{}, nil
}
func (s *pluginStubService) RemovePlugin(ctx context.Context, id string) error {
	if s.removeFn != nil {
		return s.removeFn(ctx, id)
	}
	return nil
}
func (s *pluginStubService) InspectPlugin(ctx context.Context, id string) (control.PluginSnapshot, error) {
	if s.inspectFn != nil {
		return s.inspectFn(ctx, id)
	}
	return control.PluginSnapshot{}, nil
}

// runPluginCmd invokes slashPluginWithContext with a no-op sender and returns
// the TaskResultMsg and the notice text collected.
func runPluginCmd(svc control.PluginService, args string) (TaskResultMsg, []string) {
	var notices []string
	send := func(msg tea.Msg) {
		if n, ok := msg.(LogChunkMsg); ok {
			notices = append(notices, n.Chunk)
		}
	}
	result := slashPluginWithContext(context.Background(), svc, send, args)
	return result, notices
}

// ---------------------------------------------------------------------------
// /plugin manage
// ---------------------------------------------------------------------------

func TestSlashPluginListShowsUsage(t *testing.T) {
	called := false
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			called = true
			return nil, nil
		},
	}
	result, notices := runPluginCmd(svc, "list")

	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if called {
		t.Fatal("/plugin list must not call ListPlugins")
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage: /plugin") || strings.Contains(combined, " list ") {
		t.Fatalf("/plugin list notice = %q, want usage without list action", combined)
	}
}

func TestSlashPluginManageEmpty(t *testing.T) {
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			return nil, nil
		},
	}
	result, notices := runPluginCmd(svc, "manage")

	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if !result.SuppressTurnDivider {
		t.Error("expected SuppressTurnDivider = true")
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "no installed plugins") {
		t.Errorf("expected empty-plugin notice, got: %q", combined)
	}
	if !strings.Contains(combined, "install") {
		t.Errorf("expected 'install' hint in notice, got: %q", combined)
	}
}

func TestSlashPluginManageOpensMultiSelectManager(t *testing.T) {
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			return []control.PluginSnapshot{
				{ID: "enabled-plug", Name: "Enabled", Enabled: true, Status: "active"},
				{ID: "disabled-plug", Name: "Disabled", Enabled: false, Status: "inactive"},
			}, nil
		},
	}
	var prompts []PromptRequestMsg
	var notices []string
	send := func(msg tea.Msg) {
		switch prompt := msg.(type) {
		case PromptRequestMsg:
			prompts = append(prompts, prompt)
		case LogChunkMsg:
			notices = append(notices, prompt.Chunk)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := slashPluginWithContext(ctx, svc, send, "manage")
	if result.Err != nil {
		t.Fatalf("slashPluginWithContext(manage) error = %v", result.Err)
	}
	if len(notices) != 0 {
		t.Fatalf("/plugin manage notices = %#v, want only prompt", notices)
	}
	if len(prompts) != 1 {
		t.Fatalf("prompt count = %d, want 1", len(prompts))
	}
	prompt := prompts[0]
	if !prompt.MultiSelect || !prompt.Filterable {
		t.Fatalf("prompt flags = multi %v filter %v, want true/true", prompt.MultiSelect, prompt.Filterable)
	}
	if prompt.Title != "Manage plugins" {
		t.Fatalf("prompt title = %q, want Manage plugins", prompt.Title)
	}
	if got := strings.Join(prompt.SelectedChoices, ","); got != "enabled-plug" {
		t.Fatalf("selected choices = %#v, want enabled-plug", prompt.SelectedChoices)
	}
	if len(prompt.Choices) != 2 || prompt.Choices[0].Value != "enabled-plug" || prompt.Choices[1].Value != "disabled-plug" {
		t.Fatalf("prompt choices = %#v, want plugin ids", prompt.Choices)
	}
}

func TestPluginManagePromptScrollsPastFirstPage(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 100
	model.height = 40
	choices := make([]PromptChoice, 0, 12)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("plugin-%02d", i)
		choices = append(choices, PromptChoice{Label: id, Value: id, Detail: "inactive"})
	}
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:       "Manage plugins",
		Choices:     choices,
		Filterable:  true,
		MultiSelect: true,
		Response:    make(chan PromptResponse, 1),
	})
	model.activePrompt.choiceIndex = 10
	model.syncPromptChoiceWindow()

	out := model.renderPromptModal()
	if !strings.Contains(out, "plugin-10") {
		t.Fatalf("rendered prompt = %q, want scrolled selection plugin-10", out)
	}
	if !strings.Contains(out, "earlier") {
		t.Fatalf("rendered prompt = %q, want earlier page hint", out)
	}
	if strings.Contains(out, "plugin-00") {
		t.Fatalf("rendered prompt = %q, should not stay on first page", out)
	}
}

func TestPluginManagePromptAllowsEmptySelection(t *testing.T) {
	response := make(chan PromptResponse, 1)
	model := NewModel(Config{Commands: DefaultCommands()})
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:               "Manage plugins",
		Choices:             []PromptChoice{{Label: "superpowers", Value: "superpowers", Detail: "active"}},
		SelectedChoices:     []string{"superpowers"},
		Filterable:          true,
		MultiSelect:         true,
		AllowEmptySelection: true,
		Response:            response,
	})

	model.handlePromptChoiceKey(keyPress("space"))
	if len(model.activePrompt.selected) != 0 {
		t.Fatalf("selected choices after toggle = %#v, want empty", model.activePrompt.selected)
	}
	model.handlePromptChoiceKey(keyPress("enter"))

	select {
	case got := <-response:
		if got.Err != nil {
			t.Fatalf("prompt response error = %v", got.Err)
		}
		if got.Line != "" {
			t.Fatalf("prompt response line = %q, want empty selection", got.Line)
		}
	default:
		t.Fatal("expected prompt response after confirming empty selection")
	}
}

func TestPluginManagerSelectionUpdatesEnabledPlugins(t *testing.T) {
	var enabled []string
	var disabled []string
	svc := &pluginStubService{
		enableFn: func(ctx context.Context, id string) (control.PluginSnapshot, error) {
			enabled = append(enabled, id)
			return control.PluginSnapshot{ID: id, Enabled: true}, nil
		},
		disableFn: func(ctx context.Context, id string) (control.PluginSnapshot, error) {
			disabled = append(disabled, id)
			return control.PluginSnapshot{ID: id, Enabled: false}, nil
		},
	}
	responses := make(chan PromptResponse, 1)
	responses <- PromptResponse{Line: "disabled-plug"}
	close(responses)

	awaitPluginManagerSelection(context.Background(), svc, nil, []control.PluginSnapshot{
		{ID: "enabled-plug", Enabled: true},
		{ID: "disabled-plug", Enabled: false},
	}, responses)

	if strings.Join(enabled, ",") != "disabled-plug" {
		t.Fatalf("enabled = %#v, want disabled-plug", enabled)
	}
	if strings.Join(disabled, ",") != "enabled-plug" {
		t.Fatalf("disabled = %#v, want enabled-plug", disabled)
	}
}

func TestPluginManagerSelectionSurvivesCommandContextCancellation(t *testing.T) {
	disabled := make(chan string, 1)
	svc := &pluginStubService{
		disableFn: func(ctx context.Context, id string) (control.PluginSnapshot, error) {
			disabled <- id
			return control.PluginSnapshot{ID: id, Enabled: false}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	var prompt PromptRequestMsg
	send := func(msg tea.Msg) {
		if next, ok := msg.(PromptRequestMsg); ok {
			prompt = next
		}
	}
	sendPluginManagerPrompt(ctx, svc, send, []control.PluginSnapshot{
		{ID: "superpowers", Enabled: true, Status: "active"},
	})
	if prompt.Response == nil {
		t.Fatal("sendPluginManagerPrompt did not send a prompt")
	}

	cancel()
	prompt.Response <- PromptResponse{Line: ""}

	select {
	case got := <-disabled:
		if got != "superpowers" {
			t.Fatalf("disabled plugin = %q, want superpowers", got)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin manager did not process selection after command context cancellation")
	}
}

func TestSlashPluginBareCommandOnlyShowsUsage(t *testing.T) {
	called := false
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			called = true
			return nil, nil
		},
	}
	result, notices := runPluginCmd(svc, "")
	if result.Err != nil {
		t.Fatalf("bare command should not return error, got %v", result.Err)
	}
	if called {
		t.Error("bare /plugin must not execute list")
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage: /plugin") || !strings.Contains(combined, "install") {
		t.Fatalf("bare /plugin notice = %q, want usage with install", combined)
	}
}

func TestSlashPluginManageError(t *testing.T) {
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			return nil, errors.New("store unavailable")
		},
	}
	result, _ := runPluginCmd(svc, "manage")
	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// /plugin install
// ---------------------------------------------------------------------------

func TestSlashPluginInstallSuccess(t *testing.T) {
	var gotSource string
	svc := &pluginStubService{
		installFn: func(ctx context.Context, source string) (control.PluginSnapshot, error) {
			gotSource = source
			return control.PluginSnapshot{
				ID:      "mcp-server-dev",
				Name:    "MCP Server Dev",
				Enabled: true,
				Status:  "active",
			}, nil
		},
	}
	result, notices := runPluginCmd(svc, "install mcp-server-dev@claude-plugins-official")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if gotSource != "mcp-server-dev@claude-plugins-official" {
		t.Fatalf("InstallPlugin source = %q, want mcp-server-dev@claude-plugins-official", gotSource)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "installed plugin mcp-server-dev") {
		t.Fatalf("install notice = %q, want installed plugin", combined)
	}
}

func TestSlashPluginInstallMissingSource(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "install")
	if result.Err != nil {
		t.Fatalf("missing source should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage") || !strings.Contains(combined, "plugin@marketplace") {
		t.Fatalf("install missing source notice = %q, want usage", combined)
	}
}

// ---------------------------------------------------------------------------
// /plugin marketplace
// ---------------------------------------------------------------------------

func TestSlashPluginMarketplaceAddSuccess(t *testing.T) {
	var gotSource string
	svc := &pluginStubService{
		addMarketplaceFn: func(ctx context.Context, source string) (control.MarketplaceSnapshot, error) {
			gotSource = source
			return control.MarketplaceSnapshot{Name: "demo-market", Source: source, PluginCount: 2}, nil
		},
	}
	result, notices := runPluginCmd(svc, "marketplace add acme/plugins")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if gotSource != "acme/plugins" {
		t.Fatalf("AddMarketplace source = %q, want acme/plugins", gotSource)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "added marketplace demo-market") || !strings.Contains(combined, "Plugins:     2") {
		t.Fatalf("marketplace add notice = %q", combined)
	}
}

func TestSlashPluginMarketplaceList(t *testing.T) {
	svc := &pluginStubService{
		listMarketplacesFn: func(ctx context.Context) ([]control.MarketplaceSnapshot, error) {
			return []control.MarketplaceSnapshot{{Name: "demo-market", Description: "Demo", PluginCount: 1}}, nil
		},
	}
	result, notices := runPluginCmd(svc, "marketplace list")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "demo-market (1 plugin) - Demo") {
		t.Fatalf("marketplace list notice = %q", combined)
	}
}

func TestSlashPluginMarketplaceUpdateSuccess(t *testing.T) {
	var gotName string
	svc := &pluginStubService{
		updateMarketplaceFn: func(ctx context.Context, name string) (control.MarketplaceSnapshot, error) {
			gotName = name
			return control.MarketplaceSnapshot{Name: name, PluginCount: 3}, nil
		},
	}
	result, notices := runPluginCmd(svc, "marketplace update demo-market")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if gotName != "demo-market" {
		t.Fatalf("UpdateMarketplace name = %q, want demo-market", gotName)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "updated marketplace demo-market") {
		t.Fatalf("marketplace update notice = %q", combined)
	}
}

func TestSlashPluginMarketplaceRemoveSuccess(t *testing.T) {
	var gotName string
	svc := &pluginStubService{
		removeMarketplaceFn: func(ctx context.Context, name string) error {
			gotName = name
			return nil
		},
	}
	result, notices := runPluginCmd(svc, "marketplace rm demo-market")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if gotName != "demo-market" {
		t.Fatalf("RemoveMarketplace name = %q, want demo-market", gotName)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "removed marketplace demo-market") {
		t.Fatalf("marketplace rm notice = %q", combined)
	}
}

func TestSlashPluginMarketplaceMissingActionShowsUsage(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "marketplace")
	if result.Err != nil {
		t.Fatalf("missing marketplace action should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage: /plugin marketplace") {
		t.Fatalf("marketplace usage notice = %q", combined)
	}
}

// ---------------------------------------------------------------------------
// /plugin rm
// ---------------------------------------------------------------------------

func TestSlashPluginRmSuccess(t *testing.T) {
	svc := &pluginStubService{
		removeFn: func(ctx context.Context, id string) error { return nil },
	}
	result, notices := runPluginCmd(svc, "rm myplugin")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "removed plugin myplugin") {
		t.Errorf("expected 'removed plugin myplugin' in notice, got: %q", combined)
	}
}

func TestSlashPluginRmMissingID(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "rm")
	if result.Err != nil {
		t.Fatalf("missing id should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage") {
		t.Errorf("expected usage hint, got: %q", combined)
	}
}

func TestSlashPluginRmError(t *testing.T) {
	svc := &pluginStubService{
		removeFn: func(ctx context.Context, id string) error { return errors.New("plugin not found: x") },
	}
	result, _ := runPluginCmd(svc, "rm x")
	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// /plugin unknown subcommand
// ---------------------------------------------------------------------------

func TestSlashPluginUnknownSubcommand(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "frobnicate")
	if result.Err != nil {
		t.Fatalf("unknown subcommand should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage") {
		t.Errorf("expected usage in notice for unknown subcommand, got: %q", combined)
	}
}

// ---------------------------------------------------------------------------
// formatPluginDetail
// ---------------------------------------------------------------------------

func TestFormatPluginDetailFields(t *testing.T) {
	p := control.PluginSnapshot{
		ID:          "myplugin",
		Name:        "My Plugin",
		Version:     "1.2.3",
		Enabled:     true,
		Status:      "active",
		Root:        "/tmp/p",
		Description: "A useful plugin",
		Skills:      []string{"skill-a", "skill-b"},
		Hooks:       []string{"SessionStart"},
		Warning:     "",
	}
	out := formatPluginDetail(p)
	for _, want := range []string{"myplugin", "My Plugin", "1.2.3", "active", "/tmp/p", "A useful plugin", "skill-a", "skill-b", "SessionStart"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatPluginDetail: expected %q, got: %q", want, out)
		}
	}
}

func TestFormatPluginDetailWithWarning(t *testing.T) {
	p := control.PluginSnapshot{
		ID:      "broken",
		Status:  "error",
		Enabled: true,
		Warning: "manifest parse failed",
	}
	out := formatPluginDetail(p)
	if !strings.Contains(out, "manifest parse failed") {
		t.Errorf("formatPluginDetail: expected warning, got: %q", out)
	}
}

func TestFormatPluginDetailDisabledStatus(t *testing.T) {
	p := control.PluginSnapshot{
		ID:      "myplugin",
		Enabled: false,
		Status:  "inactive",
	}
	out := formatPluginDetail(p)
	if !strings.Contains(out, "disabled") {
		t.Errorf("formatPluginDetail with disabled: expected 'disabled', got: %q", out)
	}
}
