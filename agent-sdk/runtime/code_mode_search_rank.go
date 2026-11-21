package runtime

import (
	"slices"
	"strings"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// searchWords recognizes service/method separators and camel-case names.
func searchWords(value string) []string {
	var words []string
	start := -1
	var previous rune
	for index, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			if start >= 0 {
				words = append(words, strings.ToLower(value[start:index]))
			}
			start = -1
		} else if start < 0 {
			start = index
		} else if unicode.IsUpper(r) && unicode.IsLower(previous) {
			words = append(words, strings.ToLower(value[start:index]))
			start = index
		}
		previous = r
	}
	if start >= 0 {
		words = append(words, strings.ToLower(value[start:]))
	}
	return words
}

func searchToolScore(route codemode.ToolRoute, description string, terms []string) (int, bool) {
	if len(terms) == 0 {
		return 0, true
	}
	name := searchWords(route.Name)
	method := searchWords(route.Tool)
	server := searchWords(route.Server)
	words := searchWords(description)
	score := 0
	for _, term := range terms {
		switch {
		case slices.Contains(method, term), slices.Contains(server, term), slices.Contains(name, term):
			score += 4
		case slices.ContainsFunc(name, func(word string) bool { return strings.HasPrefix(word, term) }),
			slices.ContainsFunc(method, func(word string) bool { return strings.HasPrefix(word, term) }),
			slices.ContainsFunc(server, func(word string) bool { return strings.HasPrefix(word, term) }):
			score += 2
		case slices.Contains(words, term):
			score++
		default:
			return 0, false
		}
	}
	switch {
	case slices.Equal(terms, name):
		score += 4 * len(terms)
	case slices.Equal(terms, append(server, method...)):
		score += 4 * len(terms)
	case slices.Equal(terms, method):
		score += 3 * len(terms)
	case slices.Equal(terms, server):
		score += 2 * len(terms)
	}
	return score, true
}
