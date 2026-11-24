package gatewayapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type sharedMCPLaunch struct {
	WorkingDir string
	DataDir    string
	Secret     string
	Args       []string
}

func TestHostSharedMCPStdioHelper(t *testing.T) {
	index := slices.Index(os.Args, "caelis-shared-mcp-fixture")
	if index < 0 {
		return
	}
	service, audit, release := os.Args[index+1], os.Args[index+2], os.Args[index+3]
	record := func(action string) {
		file, err := os.OpenFile(audit, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprintf(file, "%s:%s:%d\n", service, action, os.Getpid()); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sharedMCPLaunch{
		WorkingDir: cwd, DataDir: os.Getenv("PLUGIN_DATA"), Secret: os.Getenv("CAELIS_SHARED_MCP_FIXTURE_SECRET"), Args: os.Args[index+4:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(audit), service+".launch.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	record("start")
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: service, Version: "1"}, nil)
	method := "lookup"
	if service == "dtw" {
		method = "apps"
	}
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: method, Description: "Synthetic directory " + service},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ map[string]any) (*mcpsdk.CallToolResult, any, error) {
			record("call")
			if release != "" {
				ticker := time.NewTicker(15 * time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(release); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						return nil, nil, ctx.Err()
					case <-ticker.C:
					}
				}
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "FIXTURE_MCP_RESULT_" + service}}}, nil, nil
		})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
	record("stop")
}

func TestServeSharesLatestMCPAcrossNativeAndApplicationSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, store, workspace, other := filepath.Join(root, "home"), filepath.Join(root, "store"), filepath.Join(root, "workspace"), filepath.Join(root, "other")
	audit, release := filepath.Join(root, "audit"), filepath.Join(root, "release")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAELIS_SHARED_MCP_FIXTURE_SECRET", "synthetic-only")
	write := func(path string, value any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".new", raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{workspace, other} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	args := func(service, gate string) []string {
		return []string{"-test.run=^TestHostSharedMCPStdioHelper$", "--", "caelis-shared-mcp-fixture", service, audit, gate}
	}
	writeContext7 := func(service, gate string) {
		t.Helper()
		write(filepath.Join(home, ".agents", "mcp.json"), map[string]any{"mcpServers": map[string]any{
			"context7": map[string]any{"command": os.Args[0], "args": args(service, gate)},
		}})
	}
	writeContext7("old", release)
	dtw := writeSharedMCPDesktopFixture(t, root, args("dtw", ""))
	write(filepath.Join(store, "config.json"), gatewayapp.AppConfig{
		SchemaVersion: 2, Plugins: []gatewayapp.PluginConfig{{ID: "desktop-world", Root: dtw, Enabled: true}},
	})
	provider := &atomicCapabilityProvider{scripts: map[string]string{
		"SHARED_MCP_PIN":        `return await tools.search({name:"context7__lookup"});`,
		"SHARED_MCP_HOLD":       `return await tools.call("context7__lookup",{});`,
		"SHARED_MCP_STALE":      `try { await tools.call("context7__lookup",{}); } catch(e) { return "OBSERVED_CODE_"+e.code; } return "UNEXPECTED_DISPATCH";`,
		"SHARED_MCP_REDISCOVER": `await tools.search({name:"context7__lookup"}); return await tools.call("context7__lookup",{});`,
	}}
	host := startApplicationHTTPHost(t, store, workspace, provider)
	defer func() {
		_ = os.WriteFile(release, nil, 0o600)
		if host != nil {
			host.close(t)
		}
	}()
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client := host.host
	native, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "create-native"}, WorkspaceKey: host.stack.Workspace().Key, CWD: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	createApplication := func(operation, cwd string, servers ...sessionprofile.MCPServer) string {
		t.Helper()
		profile := applicationHTTPProfile()
		profile.Workspace.CWD, profile.MCPServers = cwd, servers
		created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: operation}, Profile: &profile})
		if err != nil {
			t.Fatal(err)
		}
		return created.SessionID
	}
	appA := createApplication("create-app-a", workspace)
	appB := createApplication("create-app-b", other)
	private := createApplication("create-app-private", other, sessionprofile.MCPServer{
		Name: "context7", Transport: "stdio", Command: os.Args[0], Args: args("private", ""), WorkDir: other,
	})
	ids := []string{native.SessionID, appA, appB, private}
	var closeFeeds []func()
	for _, id := range ids {
		feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		closeFeeds = append(closeFeeds, func() { _ = feed.Subscription.Close() })
	}
	closeSubscriptions := func() {
		for _, closeFeed := range closeFeeds {
			closeFeed()
		}
		closeFeeds = nil
	}
	defer closeSubscriptions()
	prompt := func(id, operation, input string) error {
		result, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: id, OperationID: operation}, Input: input, SourceKind: "user"})
		if err != nil {
			return fmt.Errorf("prompt %s: %w", operation, err)
		}
		if result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted {
			return fmt.Errorf("prompt %s: %s", operation, result.Outcome)
		}
		return nil
	}
	begin, activated := make(chan struct{}), make(chan error, len(ids))
	for i, id := range ids {
		go func() {
			<-begin
			activated <- prompt(id, fmt.Sprintf("activate-%d", i), "BASIC")
		}()
	}
	close(begin)
	for range ids {
		select {
		case err := <-activated:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	assertCatalog := func(id, service string) {
		t.Helper()
		status, err := client.SessionMCPStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(status.Servers, func(s sessionprofile.MCPServerStatus) bool {
			return s.Name == "context7" && s.Ready && len(s.ToolDetails) == 1 && s.ToolDetails[0].Description == "Synthetic directory "+service
		}) {
			t.Fatalf("Session %s did not have the current %s directory: %+v", id, service, status.Servers)
		}
	}
	assertAudit := func(marker string, count int) {
		t.Helper()
		if raw := atomicAudit(audit); strings.Count(raw, marker+":") != count {
			t.Fatalf("%s count = %d, want %d, audit:\n%s", marker, strings.Count(raw, marker+":"), count, raw)
		}
	}
	for _, id := range ids {
		waitApplicationHTTPIdle(t, ctx, client, id)
		waitAtomicApplicationMCPRunning(t, ctx, client, id, "context7")
		waitAtomicApplicationMCPRunning(t, ctx, client, id, "dtw")
	}
	for _, id := range ids[:3] {
		assertCatalog(id, "old")
	}
	assertCatalog(private, "private")
	for _, service := range []string{"dtw", "old", "private"} {
		assertAudit(service+":start", 1)
		assertAudit(service+":stop", 0)
	}
	checkLaunch := func(service, cwd, data, secret string, expectedArgs []string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, service+".launch.json"))
		if err != nil {
			t.Fatal(err)
		}
		var launch sharedMCPLaunch
		err = json.Unmarshal(raw, &launch)
		dataDir := launch.DataDir
		if dataDir != "" {
			dataDir = filepath.Dir(dataDir)
		}
		if err != nil || launch.WorkingDir != cwd || dataDir != data || launch.Secret != secret || !slices.Equal(launch.Args, expectedArgs) {
			t.Fatalf("%s launch = %+v, error = %v", service, launch, err)
		}
	}
	dtwArgs := []string{}
	if !builtinComputerUseSupported() {
		dtwArgs = []string{"--atomic"}
	}
	checkLaunch("dtw", dtw, filepath.Join(store, "plugins", "data"), "", dtwArgs)
	checkLaunch("old", filepath.Join(home, ".agents"), "", "synthetic-only", nil)
	checkLaunch("private", other, "", "synthetic-only", nil)

	for _, id := range []string{appA, appB} {
		promptAtomicCapabilityClient(t, ctx, client, id, "pin-"+id, "SHARED_MCP_PIN")
	}
	// The first call has crossed the real Session's approval gate before reload.
	if err := prompt(appA, "borrow-old", "SHARED_MCP_HOLD"); err != nil {
		t.Fatal(err)
	}
	for {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: appA})
		if err != nil {
			t.Fatal(err)
		}
		if pending := state.Approval.Active; pending != nil {
			if pending.Permission == nil || pending.Permission.ToolCall.Name != "context7__lookup" {
				t.Fatalf("unexpected MCP approval: %+v", pending)
			}
			if _, err := client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{
				WriteBase: appserver.WriteBase{SessionID: appA, OperationID: "approve-old"}, Target: pending.Target,
				ApprovalRequestID: string(pending.RequestID), Outcome: "selected", OptionID: "allow_once", Approved: true,
			}); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !state.Run.Active {
			t.Fatalf("old MCP call completed without an approval or dispatch: %s", provider.lastRequest())
		}
		select {
		case <-ctx.Done():
			t.Fatal("old MCP call did not request approval")
		case <-time.After(15 * time.Millisecond):
		}
	}
	waitAtomicAudit(t, audit, "old:call:")

	writeContext7("new", "")
	waitAtomicAudit(t, audit, "new:start:")
	waitAtomicApplicationMCPRunning(t, ctx, client, appB, "context7")
	for _, id := range ids[:3] {
		assertCatalog(id, "new")
	}
	assertCatalog(private, "private")
	assertAudit("new:start", 1)
	assertAudit("old:stop", 0)
	promptAtomicCapabilityClientWithMCPApproval(t, ctx, client, appB, "stale", "SHARED_MCP_STALE", "")
	if got := provider.lastRequest(); !strings.Contains(got, "OBSERVED_CODE_stale_revision") {
		t.Fatal("historical binding did not return stale_revision")
	}
	assertAudit("old:call", 1)
	assertAudit("new:call", 0)
	if approvals := promptAtomicCapabilityClientWithMCPApproval(t, ctx, client, appB, "rediscover", "SHARED_MCP_REDISCOVER", "allow_once"); approvals != 1 {
		t.Fatalf("rediscovery needed %d approvals, want its own Session approval", approvals)
	}
	if !strings.Contains(provider.lastRequest(), "FIXTURE_MCP_RESULT_new") {
		t.Fatal("rediscovery did not reach the replacement")
	}
	assertAudit("old:stop", 0)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, appA)
	waitAtomicAuditCount(t, audit, "old:stop:", 1)
	if !strings.Contains(provider.lastRequest(), "FIXTURE_MCP_RESULT_old") {
		t.Fatal("the borrowed call did not return from the original connection")
	}
	if err := closeProfileTestSession(ctx, client, appA); err != nil {
		t.Fatal(err)
	}
	assertAudit("dtw:stop", 0)
	assertAudit("new:stop", 0)
	assertCatalog(appB, "new")
	assertCatalog(native.SessionID, "new")
	assertCatalog(private, "private")
	if approvals := promptAtomicCapabilityClientWithMCPApproval(t, ctx, client, private, "private-call", "SHARED_MCP_REDISCOVER", "allow_once"); approvals != 1 {
		t.Fatalf("private profile needed %d approvals", approvals)
	}
	if !strings.Contains(provider.lastRequest(), "FIXTURE_MCP_RESULT_private") {
		t.Fatal("private profile dispatched outside its selected connection")
	}
	closeSubscriptions()
	for _, service := range []string{"dtw", "old", "new", "private"} {
		waitAtomicAuditCount(t, audit, service+":stop:", 1)
		assertAudit(service+":start", 1)
		atomicAssertLiveService(t, audit, service, 0)
	}
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: appB})
	if err != nil {
		t.Fatal(err)
	}
	closeFeeds = append(closeFeeds, func() { _ = feed.Subscription.Close() })
	if err := prompt(appB, "reactivate", "BASIC"); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, appB)
	waitAtomicApplicationMCPRunning(t, ctx, client, appB, "dtw")
	waitAtomicApplicationMCPRunning(t, ctx, client, appB, "context7")
	assertAudit("dtw:start", 2)
	assertAudit("new:start", 2)
	atomicAssertLiveService(t, audit, "dtw", 1)
	atomicAssertLiveService(t, audit, "new", 1)
	closeSubscriptions()
	host.close(t)
	host = nil
	for _, service := range []string{"dtw", "new"} {
		assertAudit(service+":stop", 2)
		atomicAssertLiveService(t, audit, service, 0)
	}
	t.Logf("stdio process receipts:\n%s", atomicAudit(audit))
}

func writeSharedMCPDesktopFixture(t *testing.T, root string, args []string) string {
	t.Helper()
	plugin := filepath.Join(root, "desktop-fixture")
	writeAtomicSkill(t, filepath.Join(plugin, "skills", "dtw"), "dtw", "Synthetic DTW instructions")
	executable := "fixture"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(filepath.Join(plugin, executable), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	if err := target.Close(); copyErr != nil || err != nil {
		t.Fatalf("copy stdio fixture: %v, %v", copyErr, err)
	}
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"fixture"}`
	spec, err := json.Marshal(map[string]any{
		"$schema":    "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
		"mcpServers": map[string]any{"dtw": map[string]any{"type": "stdio", "command": "./" + executable, "args": args}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"plugin.json": []byte(manifest), "mcp.json": spec} {
		if err := os.WriteFile(filepath.Join(plugin, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return plugin
}
