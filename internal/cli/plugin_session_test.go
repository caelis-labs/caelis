package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
	"github.com/caelis-labs/caelis/internal/testenv"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCLIPluginWorkerProcess(t *testing.T) {
	mode := os.Getenv("CAELIS_TEST_PLUGIN_WORKER")
	if mode == "" {
		return
	}
	if mode == "private" {
		marker := os.Getenv("CAELIS_TEST_PLUGIN_MARKER")
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			os.Exit(1)
		}
		if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(1)
		}
		_, _ = fmt.Fprintln(os.Stdout, os.Getpid())
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	path, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	worker := exec.Command(filepath.Join(filepath.Dir(path), "..", "libexec", filepath.Base(path)), "-test.run=^TestCLIPluginWorkerProcess$")
	worker.Env = append(os.Environ(), "CAELIS_TEST_PLUGIN_WORKER=private")
	input, err := worker.StdinPipe()
	if err != nil {
		os.Exit(1)
	}
	output, err := worker.StdoutPipe()
	if err != nil || worker.Start() != nil {
		os.Exit(1)
	}
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() {
		_ = input.Close()
		_ = worker.Wait()
		os.Exit(1)
	}
	pid := scanner.Text()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "dtw-fixture", Version: "1.0.0"}, nil)
	reply := func() (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: pid}}}, nil, nil
	}
	mcpsdk.AddTool[struct{}, any](server, &mcpsdk.Tool{Name: "apps", Description: "Fixture app inventory."}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return reply()
	})
	type apiInput struct {
		Methods []string `json:"methods,omitempty"`
	}
	mcpsdk.AddTool[apiInput, any](server, &mcpsdk.Tool{Name: "api", Description: "Fixture method discovery."}, func(context.Context, *mcpsdk.CallToolRequest, apiInput) (*mcpsdk.CallToolResult, any, error) {
		return reply()
	})
	type execInput struct {
		App    string `json:"app"`
		Source string `json:"source"`
	}
	mcpsdk.AddTool[execInput, any](server, &mcpsdk.Tool{Name: "exec", Description: "Fixture app script; reports the private worker PID without desktop access."}, func(context.Context, *mcpsdk.CallToolRequest, execInput) (*mcpsdk.CallToolResult, any, error) {
		return reply()
	})
	type resultInput struct {
		App         string `json:"app"`
		ExecutionID string `json:"execution_id,omitempty"`
	}
	mcpsdk.AddTool[resultInput, any](server, &mcpsdk.Tool{Name: "result", Description: "Fixture receipt readback."}, func(context.Context, *mcpsdk.CallToolRequest, resultInput) (*mcpsdk.CallToolResult, any, error) {
		return reply()
	})
	err = server.Run(context.Background(), &mcpsdk.StdioTransport{})
	_ = input.Close()
	_ = worker.Wait()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func copyCLIPluginFile(t *testing.T, source, target string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCLIPluginFreshSessionAppToolsAndWorker(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, "桌面 plugin with spaces")
	for _, path := range []string{".claude-plugin/plugin.json", ".mcp.json", "skills/desktop-world/SKILL.md", "skills/desktop-world/PATTERNS.md"} {
		copyCLIPluginFile(t, filepath.Join("testdata", "dtw-agent-plugin", path), filepath.Join(root, path), 0o600)
	}
	name := "dtw"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, dir := range []string{"bin", "libexec"} {
		copyCLIPluginFile(t, os.Args[0], filepath.Join(root, dir, name), 0o700)
	}
	// Only the launch arguments/environment differ from the packaged DTW
	// manifest. The fixture never performs desktop operations.
	marker := filepath.Join(workspace, "worker.pid")
	writeCLIPluginFile(t, root, ".mcp.json", fmt.Sprintf(`{"mcpServers":{"desktop-world":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/%s","args":["-test.run=^TestCLIPluginWorkerProcess$"],"env":{"CAELIS_TEST_PLUGIN_WORKER":"mcp","CAELIS_TEST_PLUGIN_MARKER":%q}}}}`, name, marker))
	exerciseCLIPluginSessionLifecycle(t, root)
	data, err := os.ReadFile(marker)
	pid, parseErr := strconv.Atoi(string(data))
	if err != nil || parseErr != nil || pid <= 0 || pid == os.Getpid() {
		t.Fatalf("private worker startup evidence = %q, %v, %v", data, err, parseErr)
	}
}

// This gate must use an independently verified platform package. A successful
// ready DTW MCP connection proves startup; desktop input/permissions remain
// separate physical acceptance. Ordinary Cloud tests use the fixture above.
func TestCLIPluginDesktopWorldNativeCandidate(t *testing.T) {
	root := os.Getenv("CAELIS_DTW_CANDIDATE_ROOT")
	if root == "" {
		t.Skip("set CAELIS_DTW_CANDIDATE_ROOT to a verified native DTW Plugin")
	}
	exerciseCLIPluginSessionLifecycle(t, root)
}

func exerciseCLIPluginSessionLifecycle(t *testing.T, pluginRoot string) {
	t.Helper()
	resetCLIPluginEnv(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	started := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			http.NotFound(w, request)
			return
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"plugin-test\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	store := t.TempDir()
	key, cwd, err := workspaceAddressFromCWD(workspace)
	if err != nil {
		t.Fatal(err)
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: defaultAppName, UserID: defaultPrincipalID, StoreDir: store,
		WorkspaceKey: key, WorkspaceCWD: cwd, SkillDirs: []string{},
		Sandbox:                   gatewayapp.SandboxConfig{RequestedType: "host"},
		ResolveProviderHTTPClient: gatewayapptest.StaticProviderHTTPClient(provider.Client()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if _, err := gatewayapptest.ConnectModel(ctx, host, gatewayapp.ModelConfig{Provider: "custom-chat", Model: "fixture", BaseURL: provider.URL, Token: "fixture-token", ContextWindowTokens: 128000}); err != nil {
		t.Fatal(err)
	}
	server, err := local.NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := server.Bind(appserver.Principal{ID: defaultPrincipalID})
	if err != nil {
		t.Fatal(err)
	}
	opener := func(_ context.Context, cfg gatewayapp.Config, options productClientOptions) (*productClients, error) {
		if cfg.StoreDir != store || options.Mode != productClientModeManaged {
			t.Fatalf("plugin command did not select the local managed Host: %+v, %+v", cfg, options)
		}
		return &productClients{Clients: clients, Mode: productClientModeManaged}, nil
	}
	call := func(action, target string) pluginCommandOutput {
		t.Helper()
		result, err := runCLIPluginJSON(t, ctx, opener, action, target, "--json", "--store-dir", store)
		if err != nil {
			t.Fatalf("%s = %+v, %v", action, result, err)
		}
		return result
	}
	other := t.TempDir()
	writeCLIPluginFile(t, other, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"unrelated"}`)
	call("install", other)
	type runningSession struct {
		id   string
		done <-chan error
	}
	begin := func() runningSession {
		t.Helper()
		id, err := createOrResumeHeadlessSession(ctx, clients.Sessions, host.Workspace(), "")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			var out bytes.Buffer
			_, err := runHeadless(ctx, clients.Sessions, host.Workspace(), id, "wait for fixture", outputText, &out)
			done <- err
		}()
		select {
		case <-started:
		case err := <-done:
			t.Fatalf("Session %s ended before activation: %v", id, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return runningSession{id: id, done: done}
	}
	finish := func(done <-chan error) {
		t.Helper()
		release <- struct{}{}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	checkSkill := func(id string, found bool) {
		t.Helper()
		result, err := clients.Completion.ResolveSkill(ctx, appserver.CompletionRequest{SessionID: id, Name: "desktop-world:desktop-world"})
		if err != nil || (result.Canonical != "") != found {
			t.Fatalf("Session %s Skill = %+v, %v; found=%t", id, result, err, found)
		}
	}
	before := begin()
	checkSkill(before.id, false)
	for range 2 {
		installed := call("install", pluginRoot)
		if installed.ID != "desktop-world" || installed.Plugin == nil || !installed.Plugin.Enabled {
			t.Fatalf("DTW install = %+v", installed)
		}
	}
	checkSkill(before.id, false)
	checkCLIPluginSessionAbsent(t, ctx, host, before.id)
	finish(before.done)
	after := begin()
	checkSkill(after.id, true)
	// Host Plugin reads are registry observations. Read the actual activated
	// Session for its MCP readiness; this lease stays inside test assembly.
	lease, err := host.ControlRuntimes().Acquire(ctx, appserver.Principal{ID: defaultPrincipalID}, appserver.ActionSessionInspect, after.id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close(context.Background())
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		installed, err := lease.PluginReads().Inspect(ctx, "desktop-world")
		if err != nil {
			t.Fatal(err)
		}
		servers := installed.MCPServers
		if len(servers) == 1 && servers[0].Status == "running" && hasDTWAppTools(servers[0].Tools) {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("DTW app tools did not become ready: %+v", servers)
		}
	}
	for range 2 {
		call("remove", "desktop-world")
	}
	checkSkill(after.id, true)
	pinned, err := lease.PluginReads().Inspect(ctx, "desktop-world")
	if err != nil || len(pinned.MCPServers) != 1 || pinned.MCPServers[0].Status != "running" || !hasDTWAppTools(pinned.MCPServers[0].Tools) {
		t.Fatalf("remove changed active Session MCP snapshot: %+v, %v", pinned, err)
	}
	finish(after.done)
	if err := lease.Close(ctx); err != nil {
		t.Fatal(err)
	}
	removed := begin()
	checkSkill(removed.id, false)
	checkCLIPluginSessionAbsent(t, ctx, host, removed.id)
	finish(removed.done)
	if !call("status", "unrelated").Installed || call("status", "desktop-world").Installed {
		t.Fatal("remove changed unrelated registration or retained DTW")
	}
}

func hasDTWAppTools(names []string) bool {
	ordered := slices.Clone(names)
	slices.Sort(ordered)
	return slices.Equal(ordered, []string{"api", "apps", "exec", "result"})
}

func checkCLIPluginSessionAbsent(t *testing.T, ctx context.Context, host *gatewayapp.Stack, id string) {
	t.Helper()
	lease, err := host.ControlRuntimes().Acquire(ctx, appserver.Principal{ID: defaultPrincipalID}, appserver.ActionSessionInspect, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close(context.Background())
	plugins, err := lease.PluginReads().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, installed := range plugins {
		if installed.ID == "desktop-world" {
			t.Fatalf("DTW entered a Session snapshot that should not have it: %+v", installed)
		}
	}
}
