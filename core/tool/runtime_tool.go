package tool

import (
	"maps"
	"strings"
)

const (
	RuntimeToolMetaName    = "caelis.runtime.tool"
	RuntimeToolMetaVersion = 1
)

// WithRuntimeToolMeta returns meta with tool installed under the canonical
// caelis.runtime.tool namespace. The caller retains ownership of toolMeta.
func WithRuntimeToolMeta(meta map[string]any, toolMeta map[string]any) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis := cloneNestedMap(out["caelis"])
	runtimeMeta := cloneNestedMap(caelis["runtime"])
	nextToolMeta := cloneNestedMap(runtimeMeta["tool"])
	for key, value := range toolMeta {
		key = strings.TrimSpace(key)
		if key == "" || value == nil {
			continue
		}
		nextToolMeta[key] = value
	}
	nextToolMeta["schema"] = RuntimeToolMetaName
	nextToolMeta["schema_version"] = RuntimeToolMetaVersion
	runtimeMeta["tool"] = nextToolMeta
	caelis["runtime"] = runtimeMeta
	if _, ok := caelis["version"]; !ok {
		caelis["version"] = RuntimeToolMetaVersion
	}
	out["caelis"] = caelis
	return out
}

// RuntimeToolMeta returns the canonical runtime tool metadata section.
func RuntimeToolMeta(meta map[string]any) map[string]any {
	toolMeta, ok := mapAny(nestedAny(meta, "caelis", "runtime", "tool"))
	if !ok || len(toolMeta) == 0 {
		return nil
	}
	return toolMeta
}

// RuntimeToolValue returns one value from the canonical runtime tool metadata.
func RuntimeToolValue(meta map[string]any, key string) any {
	toolMeta := RuntimeToolMeta(meta)
	if len(toolMeta) == 0 {
		return nil
	}
	return toolMeta[strings.TrimSpace(key)]
}
