package tuiapp

import "fmt"

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func compactNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := firstNonEmpty(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
