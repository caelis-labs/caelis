package gatewayapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This is an ordinary MCP subprocess behind an explicit Host launch proxy. No
// desktop API or external provider is used by the deterministic HTTP fixture.
func TestNativeProfilePluginMCPHelper(t *testing.T) {
	if !slices.Contains(os.Args, "native-profile-plugin-helper") {
		return
	}
	marker := slices.Index(os.Args, "native-profile-plugin-helper")
	if marker+1 >= len(os.Args) {
		t.Fatal("missing synthetic remote audit path")
	}
	audit := os.Args[marker+1]
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "native-profile-plugin", Version: "0.2.0"}, nil)
	for _, name := range []string{"apps", "api", "result"} {
		mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: name, Description: "Synthetic read."}, func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "SYNTHETIC_READ"}}}, nil, nil
		})
	}
	type execInput struct {
		App    string `json:"app"`
		Source string `json:"source"`
	}
	mcpsdk.AddTool[execInput, any](server, &mcpsdk.Tool{Name: "exec", Description: "Synthetic app effect."}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in execInput) (*mcpsdk.CallToolResult, any, error) {
		file, err := os.OpenFile(audit, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, nil, err
		}
		if _, err = fmt.Fprintln(file, in.App); err != nil {
			_ = file.Close()
			return nil, nil, err
		}
		if err = file.Close(); err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "SYNTHETIC_EXEC:" + in.App}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}

type nativePluginProvider struct {
	mu       sync.Mutex
	requests []string
}

func (p *nativePluginProvider) snapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.requests, "\n")
}

func (p *nativePluginProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, string(raw))
	p.mu.Unlock()
	var payload struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	message := map[string]any{"role": "assistant", "content": "NATIVE_PLUGIN_DONE"}
	finish := "stop"
	if len(payload.Tools) == 1 && payload.Tools[0].Function.Name == "InspectToolSchema" {
		message["content"] = `{"tools":["desktop_world__apps","desktop_world__api","desktop_world__exec","desktop_world__result"]}`
	} else {
		phase := 0
		app := "fixture.app.one"
		for _, m := range payload.Messages {
			if m.Role == "user" {
				phase = 0
				app = "fixture.app.one"
				if strings.Contains(fmt.Sprint(m.Content), "SECOND_APP") {
					app = "fixture.app.two"
				}
			}
			if m.Role == "tool" {
				phase++
			}
		}
		name := "mcp_call"
		args := map[string]any{}
		switch phase {
		case 0:
			name = "tool_search"
			args = map[string]any{"query": "native desktop fixture", "limit": 4}
		case 1:
			name = "Skill"
			args = map[string]any{"name": "desktop-world:desktop-world"}
		case 2:
			args = map[string]any{"tool_name": "desktop_world__apps", "arguments": map[string]any{}}
		case 3:
			args = map[string]any{"tool_name": "desktop_world__api", "arguments": map[string]any{}}
		case 4, 5:
			args = map[string]any{"tool_name": "desktop_world__exec", "arguments": map[string]any{"app": app, "source": "return 'synthetic';"}}
		default:
			name = ""
		}
		if name != "" {
			encoded, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": fmt.Sprintf("native-%d", phase), "index": 0, "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}
			finish = "tool_calls"
		}
	}
	encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(encoded) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
}

func nativeProfilePackageHTTP(t *testing.T, version string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		".claude-plugin/plugin.json":    `{"name":"desktop-world","version":"` + version + `"}`,
		".mcp.json":                     `{"mcpServers":{"desktop-world":{"command":"unlaunched-native-default"}}}`,
		"skills/desktop-world/SKILL.md": "---\nname: desktop-world\ndescription: Synthetic native package instructions.\n---\nPAIRED_VERSION_" + version,
	} {
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func settleNativeProfileHTTP(t *testing.T, ctx context.Context, client *httpclient.Client, sid, operation, input string) int {
	t.Helper()
	if input != "" {
		if _, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sid, OperationID: operation}, Input: input, SourceKind: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	approvals := 0
	for {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
		if err != nil {
			t.Fatal(err)
		}
		if active := state.Approval.Active; active != nil {
			if active.Permission == nil || active.Permission.ToolCall.Name != "desktop_world__exec" || len(active.Permission.Options) != 2 {
				t.Fatalf("native app approval lost its identity/scope: %+v", active)
			}
			approvals++
			if _, err := client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("%s-approve-%d", operation, approvals), SessionID: sid}, Target: active.Target, ApprovalRequestID: string(active.RequestID), Outcome: "selected", OptionID: "allow_session", Approved: true}); err != nil {
				t.Fatal(err)
			}
		}
		if !state.Run.Active {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return approvals
}

func TestSessionNativePluginsHTTPBridgeChildrenAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "remote-exec-audit")
	provider := &nativePluginProvider{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	info, err := client.Initialize(ctx)
	if err != nil || !slices.Contains(info.Capabilities, sessionprofile.CapabilityNativePlugins) {
		t.Fatalf("native package capability missing: %v", err)
	}
	installed := nativeProfilePackageHTTP(t, "0.1.3")
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddPluginPath(ctx, appserver.AddPluginPathRequest{WriteBase: appserver.WriteBase{OperationID: "standalone-plugin", ExpectedRevision: &status.Configuration.Revision}, Path: installed}); err != nil {
		t.Fatal(err)
	}
	selected := nativeProfilePackageHTTP(t, "0.2.0")
	profile := applicationHTTPProfile()
	profile.Instructions = "Use the selected synthetic plugin."
	profile.Tools = nil
	profile.Plugins = []sessionprofile.Plugin{{Root: selected, MCPServers: []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestNativeProfilePluginMCPHelper$", "--", "native-profile-plugin-helper", audit}, WorkDir: selected}}}}
	// Generated client values must carry the same selection through public JSON.
	bytes, _ := json.Marshal(profile)
	var wire generated.SessionProfile
	if err := json.Unmarshal(bytes, &wire); err != nil || len(wire.Plugins) != 1 {
		t.Fatalf("generated native selection missing: %v", err)
	}
	bytes, _ = json.Marshal(wire)
	if err := json.Unmarshal(bytes, &profile); err != nil {
		t.Fatal(err)
	}
	create := func(id string) string {
		t.Helper()
		result, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: id}, Profile: profilePointer(profile)})
		if err != nil {
			t.Fatal(err)
		}
		return result.SessionID
	}
	parent := create("native-parent")
	passive, err := client.SessionMCPStatus(ctx, parent)
	if err != nil || len(passive.Plugins) != 1 || passive.Plugins[0].Status != "inactive" || len(passive.Servers) != 0 {
		t.Fatalf("passive package status started a manager: %+v %v", passive, err)
	}
	if n := settleNativeProfileHTTP(t, ctx, client, parent, "native-parent-first", "FIRST_APP"); n != 1 {
		t.Fatalf("first app approvals=%d", n)
	}
	observed, err := client.SessionMCPStatus(ctx, parent)
	if err != nil || len(observed.Plugins) != 1 || observed.Plugins[0].Root != selected || observed.Plugins[0].Version != "0.2.0" || len(observed.Servers) != 1 || observed.Servers[0].PluginID != "desktop-world" || len(observed.Servers[0].Tools) != 4 || len(observed.Skills) != 1 || observed.Skills[0].Name != "desktop-world:desktop-world" {
		t.Fatalf("unpaired/duplicate native capability: %+v %v", observed, err)
	}
	if n := settleNativeProfileHTTP(t, ctx, client, parent, "native-parent-reuse", "FIRST_APP"); n != 0 {
		t.Fatalf("same app reapproved: %d", n)
	}
	if n := settleNativeProfileHTTP(t, ctx, client, parent, "native-parent-other", "SECOND_APP"); n != 1 {
		t.Fatalf("another app inherited a grant: %d", n)
	}
	sibling := create("native-sibling")
	if n := settleNativeProfileHTTP(t, ctx, client, sibling, "native-sibling-first", "FIRST_APP"); n != 1 {
		t.Fatalf("another Session inherited app grant: %d", n)
	}
	// The same app and remote schema cannot reuse a grant across package sources.
	replacement := nativeProfilePackageHTTP(t, "0.2.1")
	selection := []sessionprofile.Plugin{{Root: replacement, MCPServers: []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestNativeProfilePluginMCPHelper$", "--", "native-profile-plugin-helper", audit}, WorkDir: replacement}}}}
	current, err := client.SessionConfiguration(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(parent, sessionprofile.UpdateConfigurationRequest{OperationID: "native-change-source", ExpectedConfigurationRevision: current.Revision, Patch: sessionprofile.ConfigurationPatch{Plugins: &selection}}))); err != nil {
		t.Fatal(err)
	}
	if n := settleNativeProfileHTTP(t, ctx, client, parent, "native-new-source", "FIRST_APP"); n != 1 {
		t.Fatalf("new source inherited old app grant: %d", n)
	}
	selected = replacement
	child, task := applicationProfileChild(t, ctx, client, parent)
	childProfile, err := client.SessionProfile(ctx, child)
	if err != nil || len(childProfile.Profile.Plugins) != 1 || childProfile.Profile.Plugins[0].Root != selected {
		t.Fatalf("native child lost selected plugin: %+v %v", childProfile, err)
	}
	// Native participants present their approval in the parent's existing FIFO;
	// reading child.Run.Active is not an observation of that background Task.
	var childApproval *appserver.ActiveApproval
	for childApproval == nil {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: parent})
		if err != nil {
			t.Fatal(err)
		}
		childApproval = state.Approval.Active
		if childApproval == nil {
			select {
			case <-ctx.Done():
				t.Fatal("child app approval missing")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	if childApproval.ScopeID != task || childApproval.Permission == nil || childApproval.Permission.ToolCall.Name != "desktop_world__exec" || len(childApproval.Permission.Options) != 2 {
		t.Fatalf("child approval escaped original owner: %+v", childApproval)
	}
	if _, err := client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: "native-child-approve", SessionID: parent}, Target: childApproval.Target, ApprovalRequestID: string(childApproval.RequestID), Outcome: "selected", OptionID: "allow_session", Approved: true}); err != nil {
		t.Fatal(err)
	}
	// Only the child's canonical assistant event settles its original turn.
	// A phrase in a Surface snapshot may already occur in inherited context.
	for {
		loaded, err := host.stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: child}})
		if err != nil {
			t.Fatal(err)
		}
		done := false
		for _, e := range loaded.Events {
			if e.Type == session.EventTypeAssistant && strings.Contains(session.EventText(e), "NATIVE_PLUGIN_DONE") {
				done = true
			}
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("child original canonical completion missing")
		case <-time.After(10 * time.Millisecond):
		}
	}
	for index, sid := range []string{parent, sibling, child} {
		loaded, err := host.stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: sid}})
		if err != nil {
			t.Fatal(err)
		}
		executions := 0
		for _, e := range loaded.Events {
			if e.Type == session.EventTypeToolResult && e.Tool != nil && e.Tool.Name == "desktop_world__exec" {
				if e.Tool.Status != "completed" || e.Tool.ModelCall == nil || e.Tool.ModelCall.Name != "mcp_call" || e.Tool.ModelCall.ID != e.Tool.ID {
					t.Fatalf("canonical exec lost real/envelope pairing: %+v", e.Tool)
				}
				executions++
			}
		}
		want := 2
		if sid == parent {
			want = 8
		}
		if executions != want {
			for _, e := range loaded.Events {
				if e.Tool != nil {
					t.Logf("session_index=%d event=%s name=%s status=%s", index, e.Type, e.Tool.Name, e.Tool.Status)
				}
			}
			t.Fatalf("Session index=%d %s real exec results=%d want=%d event_count=%d", index, sid, executions, want, len(loaded.Events))
		}
	}
	if !strings.Contains(provider.snapshot(), "PAIRED_VERSION_0.2.0") || !strings.Contains(provider.snapshot(), "PAIRED_VERSION_0.2.1") || !strings.Contains(provider.snapshot(), "SYNTHETIC_EXEC:") {
		t.Fatal("model context did not receive paired Skill and original MCP results")
	}
	remoteAudit, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Fields(string(remoteAudit))); got != 12 {
		t.Fatalf("independent remote exec count=%d want=12", got)
	}
	t.Log("native HTTP: sessions=3 real_exec=12 app_approvals=5; child approval uses original root FIFO; standalone plugin unchanged")
	registered, err := client.ListPlugins(ctx, appserver.PluginRequest{})
	if err != nil || len(registered) != 1 || registered[0].Root != installed || registered[0].Version != "0.1.3" {
		t.Fatalf("profile overwrote standalone registration: %+v %v", registered, err)
	}
	cfg, err := client.SessionConfiguration(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	empty := []sessionprofile.Plugin{}
	cleared, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(parent, sessionprofile.UpdateConfigurationRequest{OperationID: "native-clear", ExpectedConfigurationRevision: cfg.Revision, Patch: sessionprofile.ConfigurationPatch{Plugins: &empty}})))
	if err != nil || len(cleared.Profile.Plugins) != 0 {
		t.Fatalf("native clear failed: %+v %v", cleared, err)
	}
	passive, err = client.SessionMCPStatus(ctx, parent)
	if err != nil || len(passive.Plugins)+len(passive.Servers)+len(passive.Skills) != 0 {
		t.Fatalf("cleared native source remained: %+v %v", passive, err)
	}
}
