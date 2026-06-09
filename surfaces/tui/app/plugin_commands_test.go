package tuiapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

// pluginStubService implements the plugin-relevant subset of control.Service
// by embedding a bridgeTestDriver (which implements the full interface with
// no-op stubs) and overriding only the plugin methods we care about.
type pluginStubService struct {
	bridgeTestDriver
	listFn    func(context.Context) ([]control.PluginSnapshot, error)
	addPathFn func(context.Context, string) (control.PluginSnapshot, error)
	enableFn  func(context.Context, string) (control.PluginSnapshot, error)
	disableFn func(context.Context, string) (control.PluginSnapshot, error)
	removeFn  func(context.Context, string) error
	inspectFn func(context.Context, string) (control.PluginSnapshot, error)
}

func (s *pluginStubService) ListPlugins(ctx context.Context) ([]control.PluginSnapshot, error) {
	if s.listFn != nil {
		return s.listFn(ctx)
	}
	return nil, nil
}
func (s *pluginStubService) AddPluginPath(ctx context.Context, path string) (control.PluginSnapshot, error) {
	if s.addPathFn != nil {
		return s.addPathFn(ctx, path)
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

// capturedNotice captures the last notice text sent via sendNotice.
func captureNotices(t *testing.T) (func(), *[]string) {
	t.Helper()
	var notices []string
	return func() {}, &notices
}

// runPluginCmd invokes slashPluginWithContext with a no-op sender and returns
// the TaskResultMsg and the notice text collected.
func runPluginCmd(svc control.Service, args string) (TaskResultMsg, []string) {
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
// /plugin list
// ---------------------------------------------------------------------------

func TestSlashPluginListEmpty(t *testing.T) {
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			return nil, nil
		},
	}
	result, notices := runPluginCmd(svc, "list")

	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	if !result.SuppressTurnDivider {
		t.Error("expected SuppressTurnDivider = true")
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "none") {
		t.Errorf("expected 'none' in notice, got: %q", combined)
	}
	if !strings.Contains(combined, "add-path") {
		t.Errorf("expected 'add-path' hint in notice, got: %q", combined)
	}
}

func TestSlashPluginListWithPlugins(t *testing.T) {
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			return []control.PluginSnapshot{
				{ID: "myplugin", Name: "My Plugin", Version: "1.0.0", Enabled: true, Status: "active", Root: "/tmp/myplugin"},
				{ID: "disabled-plug", Name: "Disabled", Version: "2.0.0", Enabled: false, Status: "inactive", Root: "/tmp/disabled"},
			}, nil
		},
	}
	result, notices := runPluginCmd(svc, "list")

	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "myplugin") {
		t.Errorf("expected 'myplugin' in notice, got: %q", combined)
	}
	if !strings.Contains(combined, "disabled") {
		t.Errorf("expected 'disabled' in notice, got: %q", combined)
	}
	// Disabled plugin should show "disabled" status
	if !strings.Contains(combined, "disabled") {
		t.Errorf("expected disabled status indicator, got: %q", combined)
	}
}

func TestSlashPluginListDefaultsToList(t *testing.T) {
	called := false
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			called = true
			return nil, nil
		},
	}
	// Invoke with no args — should default to "list"
	runPluginCmd(svc, "")
	if !called {
		t.Error("expected ListPlugins to be called with empty args")
	}
}

func TestSlashPluginListError(t *testing.T) {
	svc := &pluginStubService{
		listFn: func(ctx context.Context) ([]control.PluginSnapshot, error) {
			return nil, errors.New("store unavailable")
		},
	}
	result, _ := runPluginCmd(svc, "list")
	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// /plugin add-path
// ---------------------------------------------------------------------------

func TestSlashPluginAddPathSuccess(t *testing.T) {
	svc := &pluginStubService{
		addPathFn: func(ctx context.Context, path string) (control.PluginSnapshot, error) {
			return control.PluginSnapshot{
				ID:      "newplug",
				Name:    "New Plug",
				Enabled: true,
				Status:  "active",
				Root:    path,
			}, nil
		},
	}
	result, notices := runPluginCmd(svc, "add-path /some/path")

	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "newplug") {
		t.Errorf("expected plugin ID in notice, got: %q", combined)
	}
	if !strings.Contains(combined, "added plugin") {
		t.Errorf("expected 'added plugin' in notice, got: %q", combined)
	}
}

func TestSlashPluginAddPathMissingArg(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "add-path")

	if result.Err != nil {
		t.Fatalf("missing arg should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage") {
		t.Errorf("expected usage hint in notice, got: %q", combined)
	}
}

func TestSlashPluginAddPathError(t *testing.T) {
	svc := &pluginStubService{
		addPathFn: func(ctx context.Context, path string) (control.PluginSnapshot, error) {
			return control.PluginSnapshot{}, errors.New("path does not exist")
		},
	}
	result, _ := runPluginCmd(svc, "add-path /bad/path")
	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// /plugin enable
// ---------------------------------------------------------------------------

func TestSlashPluginEnableSuccess(t *testing.T) {
	svc := &pluginStubService{
		enableFn: func(ctx context.Context, id string) (control.PluginSnapshot, error) {
			return control.PluginSnapshot{ID: id, Enabled: true, Status: "active"}, nil
		},
	}
	result, notices := runPluginCmd(svc, "enable myplugin")

	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "enabled plugin myplugin") {
		t.Errorf("expected 'enabled plugin myplugin' in notice, got: %q", combined)
	}
}

func TestSlashPluginEnableMissingID(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "enable")
	if result.Err != nil {
		t.Fatalf("missing id should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage") {
		t.Errorf("expected usage hint, got: %q", combined)
	}
}

func TestSlashPluginEnableError(t *testing.T) {
	svc := &pluginStubService{
		enableFn: func(ctx context.Context, id string) (control.PluginSnapshot, error) {
			return control.PluginSnapshot{}, errors.New("plugin not found: nosuch")
		},
	}
	result, _ := runPluginCmd(svc, "enable nosuch")
	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// /plugin disable
// ---------------------------------------------------------------------------

func TestSlashPluginDisableSuccess(t *testing.T) {
	svc := &pluginStubService{
		disableFn: func(ctx context.Context, id string) (control.PluginSnapshot, error) {
			return control.PluginSnapshot{ID: id, Enabled: false, Status: "inactive"}, nil
		},
	}
	result, notices := runPluginCmd(svc, "disable myplugin")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "disabled plugin myplugin") {
		t.Errorf("expected 'disabled plugin myplugin' in notice, got: %q", combined)
	}
}

// ---------------------------------------------------------------------------
// /plugin remove
// ---------------------------------------------------------------------------

func TestSlashPluginRemoveSuccess(t *testing.T) {
	svc := &pluginStubService{
		removeFn: func(ctx context.Context, id string) error { return nil },
	}
	result, notices := runPluginCmd(svc, "remove myplugin")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "removed plugin myplugin") {
		t.Errorf("expected 'removed plugin myplugin' in notice, got: %q", combined)
	}
}

func TestSlashPluginRemoveMissingID(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "remove")
	if result.Err != nil {
		t.Fatalf("missing id should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage") {
		t.Errorf("expected usage hint, got: %q", combined)
	}
}

func TestSlashPluginRemoveError(t *testing.T) {
	svc := &pluginStubService{
		removeFn: func(ctx context.Context, id string) error { return errors.New("plugin not found: x") },
	}
	result, _ := runPluginCmd(svc, "remove x")
	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// /plugin inspect
// ---------------------------------------------------------------------------

func TestSlashPluginInspectSuccess(t *testing.T) {
	svc := &pluginStubService{
		inspectFn: func(ctx context.Context, id string) (control.PluginSnapshot, error) {
			return control.PluginSnapshot{
				ID:          id,
				Name:        "My Plugin",
				Version:     "3.0.0",
				Enabled:     true,
				Status:      "active",
				Root:        "/tmp/myplugin",
				Description: "A great plugin",
				Skills:      []string{"skill-a"},
				Hooks:       []string{"SessionStart"},
			}, nil
		},
	}
	result, notices := runPluginCmd(svc, "inspect myplugin")
	if result.Err != nil {
		t.Fatalf("expected no error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	for _, want := range []string{"myplugin", "My Plugin", "3.0.0", "skill-a", "SessionStart"} {
		if !strings.Contains(combined, want) {
			t.Errorf("expected %q in inspect output, got: %q", want, combined)
		}
	}
}

func TestSlashPluginInspectMissingID(t *testing.T) {
	svc := &pluginStubService{}
	result, notices := runPluginCmd(svc, "inspect")
	if result.Err != nil {
		t.Fatalf("missing id should not return error, got %v", result.Err)
	}
	combined := strings.Join(notices, "\n")
	if !strings.Contains(combined, "usage") {
		t.Errorf("expected usage hint, got: %q", combined)
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
// formatPluginList and formatPluginDetail
// ---------------------------------------------------------------------------

func TestFormatPluginListEmpty(t *testing.T) {
	out := formatPluginList(nil)
	if !strings.Contains(out, "none") {
		t.Errorf("formatPluginList(nil): expected 'none', got: %q", out)
	}
}

func TestFormatPluginListWithEntries(t *testing.T) {
	plugins := []control.PluginSnapshot{
		{ID: "plug1", Name: "Plug One", Version: "1.0.0", Enabled: true, Status: "active", Root: "/p1", Description: "First plugin"},
		{ID: "plug2", Name: "Plug Two", Version: "2.0.0", Enabled: false, Status: "inactive", Root: "/p2"},
	}
	out := formatPluginList(plugins)
	for _, want := range []string{"plug1", "plug2", "active", "disabled", "/p1", "/p2"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatPluginList: expected %q, got: %q", want, out)
		}
	}
}

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
