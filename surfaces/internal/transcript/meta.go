package transcript

import "github.com/caelis-labs/caelis/internal/jsonvalue"

func RuntimeToolMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func RuntimeTaskMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	taskMeta, _ := runtimeMeta["task"].(map[string]any)
	return taskMeta
}

func RuntimeMetaSection(meta map[string]any, section string) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	values, _ := runtimeMeta[section].(map[string]any)
	return values
}

func MetaInt64(meta map[string]any, path ...string) (int64, bool) {
	return jsonvalue.Int64At(meta, path...)
}

func MergeMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 {
		return CloneAnyMap(overlay)
	}
	out := CloneAnyMap(base)
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = MergeMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = jsonvalue.Clone(value)
	}
	return out
}

func CloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	if len(values) == 0 {
		return map[string]any{}
	}
	return jsonvalue.CloneMap(values)
}
