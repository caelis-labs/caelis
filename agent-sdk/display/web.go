package display

import "strconv"

func WebSearchSummary(input map[string]any, output map[string]any) string {
	return firstNonEmpty(WebSearchDisplayArg(output), WebSearchDisplayArg(input))
}

func WebFetchSummary(input map[string]any, output map[string]any) string {
	return firstNonEmpty(WebFetchDisplayArg(input), WebFetchDisplayArg(output))
}

func WebSearchDisplayArg(raw map[string]any) string {
	query := firstNonEmpty(displayString(raw["query"]), displayString(raw["pattern"]), displayString(raw["text"]))
	if query == "" {
		return ""
	}
	return strconv.Quote(query)
}

func WebFetchDisplayArg(raw map[string]any) string {
	url := firstNonEmpty(displayString(raw["url"]), displayString(raw["uri"]), displayString(raw["href"]), displayString(raw["final_url"]))
	return truncateTailString(url, 120)
}
