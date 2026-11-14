package gatewayapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func dtwIdentityFixture(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"plugin.json":                  `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"development"}`,
		"mcp.json":                     `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"` + name + `":{"type":"stdio","command":"./server"}}}`,
		"server":                       "synthetic fixture; never launched",
		"skills/" + name + "/SKILL.md": "---\nname: " + name + "\ndescription: Synthetic DTW identity fixture.\n---\n" + root,
	} {
		writeExtensionTestFile(t, filepath.Join(root, path), body)
	}
	return root
}

func TestDTWIdentityGatewayAssembly(t *testing.T) {
	for _, name := range []string{"dtw", "desktop-world"} {
		t.Run(name, func(t *testing.T) {
			root := dtwIdentityFixture(t, name)
			paths := applicationPluginPaths(t.TempDir(), "identity-session", t.TempDir())
			defaults := []plugin.Config{{ID: "saved-directory-alias", Root: root, Enabled: true}}
			profile := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: root, MCPServers: []sessionprofile.MCPServer{{Name: name, Transport: "stdio", Command: "verified-host-proxy", Args: []string{"generation"}, WorkDir: root}}}}}
			assertPair := func(got plugin.Contributions) {
				t.Helper()
				if len(got.Sources) != 1 || got.Sources[0].ID != "desktop-world" || got.Sources[0].Root != root || got.Sources[0].Version != "development" || len(got.MCPServerSpecs) != 1 || len(got.SkillBundles) != 1 {
					t.Fatalf("selected package identity/root = %+v", got)
				}
				spec := got.MCPServerSpecs[0]
				if spec.Name != name || spec.PluginID != "desktop-world" || spec.PackageRoot != root || spec.PackageVersion != "development" || spec.SourceTrusted || !reflect.DeepEqual(spec.ToolResourceParameters, map[string]string{"exec": "app"}) || len(spec.SourceTrustedTools) != 0 {
					t.Fatalf("service identity or app approval configuration = %+v", spec)
				}
				metas, err := applicationPluginSkills(got.SkillBundles)
				if err != nil || len(metas) != 1 || metas[0].Name != "desktop-world:"+name || metas[0].PluginID != "desktop-world" || metas[0].Namespace != "desktop-world" || metas[0].LocalName != name || metas[0].Path != filepath.Join(root, "skills", name, "SKILL.md") || got.SkillBundles[0].PluginRoot != root {
					t.Fatalf("Skill identity/root = %+v, %v", metas, err)
				}
			}
			selected, err := applicationRuntimePluginContributions(profile, paths, defaults...)
			if err != nil {
				t.Fatal(err)
			}
			assertPair(selected)
			spec := selected.MCPServerSpecs[0]
			if spec.Command != "verified-host-proxy" || !reflect.DeepEqual(spec.Args, []string{"generation"}) || spec.WorkDir != root || !spec.CleanEnvironment || spec.DataDir != "" || len(spec.Env) != 0 {
				t.Fatalf("Host proxy launch was not isolated: %+v", spec)
			}
			for _, optional := range [][]sessionprofile.Plugin{nil, {}} {
				got, err := applicationRuntimePluginContributions(sessionprofile.Profile{Plugins: optional}, paths, defaults...)
				if err != nil {
					t.Fatal(err)
				}
				assertPair(got)
				if got.MCPServerSpecs[0].Command != filepath.Join(root, "server") {
					t.Fatal("default assembly inherited the explicit profile proxy")
				}
			}
			ordinary, err := resolveGatewayPluginContributions(defaults, paths.ProductStoreDir, paths.WorkspaceDir)
			if err != nil {
				t.Fatal(err)
			}
			assertPair(ordinary)
			resource := &applicationMCPResource{plugins: profile.Plugins}
			if !resource.matches(profile) {
				t.Fatal("unchanged snapshot did not match its own connection")
			}
			otherName := "dtw"
			if name == "dtw" {
				otherName = "desktop-world"
			}
			for _, proxyName := range []string{otherName, "desktop_world", "dtw-extra"} {
				changed := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: root, MCPServers: []sessionprofile.MCPServer{{Name: proxyName, Transport: "stdio", Command: "verified-host-proxy", Args: []string{"generation"}, WorkDir: root}}}}}
				if _, err := applicationRuntimePluginContributions(changed, paths); err == nil {
					t.Fatalf("undeclared proxy %q matched service %q", proxyName, name)
				}
				if resource.matches(changed) {
					t.Fatalf("proxy identity change %q reused snapshot connection", proxyName)
				}
			}
			otherProfile := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: dtwIdentityFixture(t, otherName)}}}
			if resource.matches(otherProfile) {
				t.Fatal("old/new package selection shared a snapshot connection")
			}
		})
	}
}

func TestDTWIdentityMCPBindingsAndInflightResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseOld := func() { releaseOnce.Do(func() { close(release) }) }
	// Unblock the accepted request before cleanup closes the HTTP transports.
	t.Cleanup(releaseOld)
	var oldCalls, newCalls atomic.Int32
	managers := make(map[string]*mcp.Manager)
	tools := make(map[string]tool.Tool)
	for _, name := range []string{"desktop-world", "dtw"} {
		server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "development"}, nil)
		server.AddTool(&mcpsdk.Tool{Name: "exec", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"app": map[string]any{"type": "string"}}, "required": []string{"app"}}}, func(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			if name == "desktop-world" {
				if oldCalls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			} else {
				newCalls.Add(1)
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: name + " original receipt"}}}, nil
		})
		endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
		t.Cleanup(endpoint.Close)
		root := dtwIdentityFixture(t, name)
		assembled, err := applicationRuntimePluginContributions(sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: root}}}, applicationPluginPaths(t.TempDir(), name, t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		// Replace only the test transport; identity and policy come from gateway assembly.
		spec := assembled.MCPServerSpecs[0]
		spec.Transport, spec.URL, spec.Command = mcp.TransportStreamableHTTP, endpoint.URL, ""
		manager, err := mcp.NewManager(ctx, []mcp.ServerSpec{spec}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		select {
		case <-manager.Initialized():
		case <-ctx.Done():
			t.Fatal("MCP initialization timed out")
		}
		item, found, err := manager.Lookup(ctx, mcp.ServerToolNamePrefix(name)+"exec")
		if err != nil || !found {
			t.Fatalf("assembled service missing from real MCP catalog: %v, %v", found, err)
		}
		managers[name], tools[name] = manager, item
	}
	// Cleanup runs in reverse registration order, so release before closing either manager.
	t.Cleanup(releaseOld)
	old, fresh := tools["desktop-world"], tools["dtw"]
	oldBinding, newBinding := tool.BindingForDefinition(old.Definition()), tool.BindingForDefinition(fresh.Definition())
	if oldBinding.Identity == newBinding.Identity {
		t.Fatal("server rename transferred the old tool identity")
	}
	for _, pair := range []struct{ current, other string }{{"desktop-world", "dtw"}, {"dtw", "desktop-world"}} {
		manager := managers[pair.current]
		if err := manager.CheckToolBinding(ctx, tool.BindingForDefinition(tools[pair.current].Definition())); err != nil {
			t.Fatalf("own activation binding rejected: %v", err)
		}
		if err := manager.CheckToolBinding(ctx, tool.BindingForDefinition(tools[pair.other].Definition())); err == nil {
			t.Fatal("old/new activations recognized each other's binding")
		}
		if _, found, err := manager.Lookup(ctx, tools[pair.other].Definition().Name); err != nil || found {
			t.Fatalf("old/new catalog names aliased: found=%v, err=%v", found, err)
		}
	}
	call := tool.Call{ID: "accepted-before-upgrade", Name: old.Definition().Name, Input: []byte(`{"app":"synthetic.app"}`)}
	done := make(chan struct {
		result tool.Result
		err    error
	}, 1)
	go func() {
		result, err := old.Call(ctx, call)
		done <- struct {
			result tool.Result
			err    error
		}{result, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("old activation did not accept the call")
	}
	if err := managers["desktop-world"].RevokeServer("desktop-world", "desktop-world"); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Call(ctx, call); err == nil {
		t.Fatal("revoked old handle admitted another dispatch")
	}
	freshResult, err := fresh.Call(ctx, tool.Call{ID: "new-activation", Name: fresh.Definition().Name, Input: call.Input})
	if err != nil || freshResult.IsError || freshResult.Name != "dtw__exec" {
		t.Fatalf("new activation positive control = %+v, %v", freshResult, err)
	}
	releaseOld()
	select {
	case got := <-done:
		if got.err != nil || got.result.IsError || got.result.ID != call.ID || got.result.Name != call.Name || len(got.result.Content) != 1 || got.result.Content[0].Text == nil || got.result.Content[0].Text.Text != "desktop-world original receipt" {
			t.Fatalf("old accepted result lost its original identity: %+v, %v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("old accepted call did not finish")
	}
	if oldCalls.Load() != 1 || newCalls.Load() != 1 {
		t.Fatalf("call was redispatched: old=%d new=%d", oldCalls.Load(), newCalls.Load())
	}
}
