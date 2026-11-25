// Package plugintest supplies fixed package loaders for Core Host tests.
package plugintest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DesktopWorld provides a paired Skill and a loopback MCP server for tests
// that exercise the Host's fixed package selection or tool routing.
func DesktopWorld(t *testing.T) (plugin.BuiltinPackage, string) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "dtw", Version: "fixture"}, nil)
	type query struct {
		Query string `json:"query,omitempty"`
		Limit int    `json:"limit,omitempty"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "apps"}, func(context.Context, *mcp.CallToolRequest, query) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture apps"}}}, nil, nil
	})
	type script struct {
		App    string `json:"app"`
		Source string `json:"source"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "exec"}, func(context.Context, *mcp.CallToolRequest, script) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture exec"}}}, nil, nil
	})
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(endpoint.Close)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json":         `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"fixture"}`,
		"mcp.json":            fmt.Sprintf(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"dtw":{"type":"streamable-http","url":%q}}}`, endpoint.URL),
		"skills/dtw/SKILL.md": "---\nname: dtw\ndescription: Desktop World fixture.\n---\nFixture instructions.\n",
	}
	for path, body := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return func(paths plugin.RuntimePaths) (plugin.Contributions, error) {
		result, err := plugin.ResolveContributions([]plugin.Config{{ID: "desktop-world", Root: root, Enabled: true}}, paths)
		if err == nil {
			result.Sources[0].Status = "builtin"
		}
		return result, err
	}, root
}

// EmptyPackage starts a Host with only the plugins configured by the test.
func EmptyPackage(plugin.RuntimePaths) (plugin.Contributions, error) {
	return plugin.Contributions{}, nil
}
