package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeLargeCatalogStartup(t *testing.T) {
	for _, count := range []int{1200, 2000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := &jsExecSource{}
			var calls []string
			const server = "catalog_service_with_original_names"
			const plugin = "catalog_plugin_with_original_names"
			var routes []codemode.ToolRoute
			for i := range count {
				remote := fmt.Sprintf("catalog_operation_%04d_with_original_name", i)
				def := fixtureMCPDefinition("large-catalog-v1")
				def.Name = server + "__" + remote
				def.Metadata[tool.MetadataMCPServer] = server
				def.Metadata[tool.MetadataMCPTool] = remote
				def.Metadata[tool.MetadataPluginID] = plugin
				def.Metadata[tool.MetadataMCPSourceTrusted] = true
				routes = append(routes, codeModeToolRoute(def))
				source.items = append(source.items, tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
					calls = append(calls, call.Name)
					if call.Parent == nil || call.Parent.Kind != codemode.ToolKind || call.TargetBinding == nil {
						t.Errorf("lost Runtime admission identity: %+v", call)
					}
					return tool.Result{}, nil
				}})
			}
			raw, _ := json.Marshal(routes)
			t.Logf("catalog routes=%d actual JSON bytes=%d reply budget=%d", count, len(raw), codemode.DefaultLimits().ReplyBytes)
			first, last := routes[0], routes[len(routes)-1]
			code := fmt.Sprintf(`await %s.%s({});await tools.plugins.%s.%s.%s({});await tools.call(%q,{});return null;`, server, first.Tool, plugin, server, last.Tool, last.Name)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(code)
				}
				return nil
			}}
			var metrics []gojaexec.Metrics
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{Observe: func(m gojaexec.Metrics) { metrics = append(metrics, m) }}}}, DeferredTools: source}, nil, nil)
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					t.Errorf("script failed: %+v", event.Tool.Output)
				}
			}
			if strings.Join(calls, ",") != strings.Join([]string{first.Name, last.Name, last.Name}, ",") {
				t.Errorf("canonical calls=%v", calls)
			}
			if len(metrics) != 1 || !metrics[0].Reaped {
				t.Errorf("worker metrics=%+v", metrics)
			}
		})
	}
}
