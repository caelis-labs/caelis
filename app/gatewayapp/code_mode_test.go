package gatewayapp

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestSelectedCodeModeToolsAreProductPresentationPolicy(t *testing.T) {
	makeTool := func(name, server, remote string) tool.Tool {
		return tool.NamedTool{Def: tool.Definition{Name: name, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: server, tool.MetadataMCPTool: remote}}}
	}
	candidates := []tool.Tool{nil, makeTool("docs_alias", "context7", "query-docs"), makeTool("dtw_alias", "desktop_world", "exec"), makeTool("other_exec", "other", "exec"), makeTool("unselected", "context7", "other"), tool.NamedTool{Def: tool.Definition{Name: "native", Metadata: map[string]any{tool.MetadataMCPTool: "query-docs"}}}}
	if got := selectedCodeModeTools(candidates); !reflect.DeepEqual(got, []string{"docs_alias", "dtw_alias"}) {
		t.Fatalf("selected canonical names=%v", got)
	}
}
