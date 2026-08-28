package acpmeta

import (
	"strings"

	"github.com/caelis-labs/caelis/internal/jsonvalue"
)

// ToolName reads the maintained exact tool identity at the external ACP Host
// boundary. It must not be inferred from a presentation title.
func ToolName(meta map[string]any) string {
	return jsonvalue.StringAt(meta, "caelis", "runtime", "tool", "name")
}

// WithToolName builds Host-private compatibility metadata for an exact tool
// identity while preserving unrelated provider and Caelis sections.
func WithToolName(meta map[string]any, toolName string) map[string]any {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return jsonvalue.CloneMap(meta)
	}
	out := jsonvalue.CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis := mapAt(out, "caelis")
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis["version"] = 1
	runtime := mapAt(caelis, "runtime")
	if runtime == nil {
		runtime = map[string]any{}
	}
	tool := mapAt(runtime, "tool")
	if tool == nil {
		tool = map[string]any{}
	}
	tool["name"] = toolName
	runtime["tool"] = tool
	caelis["runtime"] = runtime
	out["caelis"] = caelis
	return out
}

func mapAt(values map[string]any, key string) map[string]any {
	mapped, _ := values[key].(map[string]any)
	return jsonvalue.CloneMap(mapped)
}
