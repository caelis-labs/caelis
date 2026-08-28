package acppermission

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// permissionToolMeta writes only the Control-owned exact tool identity used to
// coordinate one ACP permission request. Display labels remain non-authoritative.
func permissionToolMeta(meta map[string]any, toolName string) map[string]any {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return clonePermissionMeta(meta)
	}
	out := clonePermissionMeta(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out["caelis"].(map[string]any)
	caelis = clonePermissionMeta(caelis)
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis["version"] = 1
	runtime, _ := caelis["runtime"].(map[string]any)
	runtime = clonePermissionMeta(runtime)
	if runtime == nil {
		runtime = map[string]any{}
	}
	tool, _ := runtime["tool"].(map[string]any)
	tool = clonePermissionMeta(tool)
	if tool == nil {
		tool = map[string]any{}
	}
	tool["name"] = toolName
	runtime["tool"] = tool
	caelis["runtime"] = runtime
	out["caelis"] = caelis
	return out
}

func permissionToolName(meta map[string]any) string {
	caelis, _ := meta["caelis"].(map[string]any)
	runtime, _ := caelis["runtime"].(map[string]any)
	tool, _ := runtime["tool"].(map[string]any)
	name, _ := tool["name"].(string)
	return strings.TrimSpace(name)
}

func mergePermissionMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(overlay) == 0 {
		return clonePermissionMeta(base)
	}
	out := clonePermissionMeta(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = mergePermissionMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = clonePermissionValue(value)
	}
	return out
}

func clonePermissionMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	return session.CloneState(meta)
}

func clonePermissionValue(value any) any {
	return session.CloneState(map[string]any{"value": value})["value"]
}
