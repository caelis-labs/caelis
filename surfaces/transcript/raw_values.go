package transcript

import (
	"fmt"
	"strconv"
	"strings"
)

// rawDisplayString preserves string whitespace because terminal output fallback
// paths may carry intentional newlines or spacing. Callers trim only when they
// are testing presence rather than returning display bytes.
func rawDisplayString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func rawInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// rawIntOrZero is for fallback branches where zero and invalid both mean
// "do not synthesize display output" rather than a visible numeric value.
func rawIntOrZero(value any) int {
	parsed, _ := rawInt(value)
	return parsed
}

func rawBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

// firstRawNonEmpty preserves whitespace for terminal snippets. Presence checks
// that should ignore whitespace trim at the call site instead.
func firstRawNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
