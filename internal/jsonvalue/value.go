// Package jsonvalue owns small repository-private operations on JSON-shaped
// values used outside the reusable Agent SDK boundary.
package jsonvalue

import (
	"encoding/json"
	"maps"
	"math"
	"strings"
)

// Clone recursively isolates maps and slices while preserving scalar types.
func Clone(value any) any {
	return clone(value)
}

// CloneMap recursively isolates maps and slices while preserving scalar types.
func CloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := maps.Clone(in)
	for key, value := range out {
		out[key] = clone(value)
	}
	return out
}

// MergeMap recursively overlays extra onto an isolated copy of base.
func MergeMap(base map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return CloneMap(base)
	}
	out := CloneMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range extra {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = MergeMap(baseMap, overlayMap)
				continue
			}
		}
		out[key] = clone(value)
	}
	return out
}

// StringAt returns a trimmed string from one complete object path.
func StringAt(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

// Int64At returns an exact integer together with whether the path contains a
// supported canonical JSON numeric representation.
func Int64At(values map[string]any, path ...string) (int64, bool) {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		var present bool
		current, present = mapped[key]
		if !present {
			return 0, false
		}
	}
	switch value := current.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		const int64Limit = 1 << 63
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
			value < -int64Limit || value >= int64Limit {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func clone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = clone(item)
		}
		return out
	default:
		return value
	}
}
