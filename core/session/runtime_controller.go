package session

import (
	"encoding/json"
	"maps"
	"strings"
)

const (
	RuntimeControllerMetaName    = "caelis.runtime.controller"
	RuntimeControllerMetaVersion = 1
)

// WithRuntimeControllerMeta returns meta with controller installed under the
// canonical caelis.runtime.controller namespace. The caller retains ownership
// of controller.
func WithRuntimeControllerMeta(meta map[string]any, controller map[string]any) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis := runtimeControllerNestedMap(out["caelis"])
	runtimeMeta := runtimeControllerNestedMap(caelis["runtime"])
	controllerMeta := maps.Clone(controller)
	if controllerMeta == nil {
		controllerMeta = map[string]any{}
	}
	controllerMeta["schema"] = RuntimeControllerMetaName
	controllerMeta["schema_version"] = RuntimeControllerMetaVersion
	runtimeMeta["controller"] = controllerMeta
	caelis["runtime"] = runtimeMeta
	if _, ok := caelis["version"]; !ok {
		caelis["version"] = RuntimeControllerMetaVersion
	}
	out["caelis"] = caelis
	return out
}

// RuntimeControllerMeta returns the canonical runtime controller metadata
// section.
func RuntimeControllerMeta(meta map[string]any) map[string]any {
	controller, ok := runtimeControllerMapAny(runtimeControllerNestedAny(meta, "caelis", "runtime", "controller"))
	if !ok || len(controller) == 0 {
		return nil
	}
	return controller
}

func runtimeControllerNestedMap(value any) map[string]any {
	if in, ok := runtimeControllerMapAny(value); ok {
		return maps.Clone(in)
	}
	return map[string]any{}
}

func runtimeControllerMapAny(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[strings.TrimSpace(key)] = item
		}
		return out, true
	default:
		raw, err := json.Marshal(value)
		if err != nil || len(raw) == 0 || string(raw) == "null" {
			return nil, false
		}
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil || len(out) == 0 {
			return nil, false
		}
		return out, true
	}
}

func runtimeControllerNestedAny(meta map[string]any, path ...string) any {
	var current any = meta
	for _, key := range path {
		mapped, ok := runtimeControllerMapAny(current)
		if !ok {
			return nil
		}
		current = mapped[strings.TrimSpace(key)]
	}
	return current
}
