package tuiapp

import "strings"

func filterByPrefix[T any](query string, candidates []T, terms func(T) []string) []T {
	query = strings.ToLower(strings.TrimSpace(query))
	if len(candidates) == 0 {
		return nil
	}
	filtered := make([]T, 0, len(candidates))
	for _, candidate := range candidates {
		if query != "" && !candidateMatchesPrefix(query, terms(candidate)...) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func normalizeFilteredSelection(current int, query string, previousQuery string, count int) int {
	if count <= 0 {
		return 0
	}
	if query != previousQuery {
		return 0
	}
	if current < 0 {
		return 0
	}
	if current >= count {
		return count - 1
	}
	return current
}

func candidateMatchesPrefix(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}
