package argparse

import (
	"fmt"
	"math"
	"strings"
)

// Bool reads one boolean arg by key.
func Bool(args map[string]any, key string, defaultValue bool) (bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return defaultValue, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("tool: arg %q must be boolean", key)
	}
	return value, nil
}

// String reads one string arg by key.
func String(args map[string]any, key string, required bool) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		if required {
			return "", fmt.Errorf("tool: missing required arg %q", key)
		}
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("tool: arg %q must be string", key)
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("tool: arg %q must be non-empty", key)
	}
	return value, nil
}

// Int reads one integer arg by key.
func Int(args map[string]any, key string, defaultValue int) (int, error) {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1

	raw, ok := args[key]
	if !ok || raw == nil {
		return defaultValue, nil
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case int8:
		return int(v), nil
	case int16:
		return int(v), nil
	case int32:
		return int(v), nil
	case int64:
		if v > int64(maxInt) || v < int64(minInt) {
			return 0, fmt.Errorf("tool: arg %q is out of int range", key)
		}
		return int(v), nil
	case uint:
		if v > uint(maxInt) {
			return 0, fmt.Errorf("tool: arg %q is out of int range", key)
		}
		return int(v), nil
	case uint8:
		return int(v), nil
	case uint16:
		return int(v), nil
	case uint32:
		if uint64(v) > uint64(maxInt) {
			return 0, fmt.Errorf("tool: arg %q is out of int range", key)
		}
		return int(v), nil
	case uint64:
		if v > uint64(maxInt) {
			return 0, fmt.Errorf("tool: arg %q is out of int range", key)
		}
		return int(v), nil
	case float32:
		if math.Trunc(float64(v)) != float64(v) {
			return 0, fmt.Errorf("tool: arg %q must be integer", key)
		}
		return int(v), nil
	case float64:
		if math.Trunc(v) != v {
			return 0, fmt.Errorf("tool: arg %q must be integer", key)
		}
		if v > float64(maxInt) || v < float64(minInt) {
			return 0, fmt.Errorf("tool: arg %q is out of int range", key)
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("tool: arg %q must be integer", key)
	}
}
