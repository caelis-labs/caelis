package promptrefs

import (
	"strings"
	"unicode"
)

type Kind string

const (
	KindSkill Kind = "skill"
	KindFile  Kind = "file"
)

type Token struct {
	Kind  Kind
	Start int
	End   int
	Value string
}

func ScanSubmissionReferences(text string) []Token {
	input := []rune(text)
	tokens := make([]Token, 0, 2)
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '$':
			if !referenceBoundary(input, i) {
				continue
			}
			end := i + 1
			for end < len(input) && IsSkillQueryRune(input[end]) {
				end++
			}
			if end == i+1 {
				continue
			}
			tokens = append(tokens, Token{
				Kind:  KindSkill,
				Start: i,
				End:   end,
				Value: string(input[i+1 : end]),
			})
			i = end - 1
		case '#':
			if !referenceBoundary(input, i) {
				continue
			}
			end := i + 1
			for end < len(input) && IsMentionQueryRune(input[end]) {
				end++
			}
			if end == i+1 {
				continue
			}
			tokens = append(tokens, Token{
				Kind:  KindFile,
				Start: i,
				End:   end,
				Value: string(input[i+1 : end]),
			})
			i = end - 1
		}
	}
	return tokens
}

func MentionQueryAtCursorWithPrefix(input []rune, cursor int) (int, int, string, string, bool) {
	if len(input) == 0 {
		return 0, 0, "", "", false
	}
	cursor = normalizeCursor(input, cursor)
	start := cursor
	for start > 0 && IsMentionQueryRune(input[start-1]) {
		start--
	}
	if start == 0 || (input[start-1] != '@' && input[start-1] != '#') {
		return 0, 0, "", "", false
	}
	at := start - 1
	if !referenceBoundary(input, at) {
		return 0, 0, "", "", false
	}
	end := cursor
	for end < len(input) && IsMentionQueryRune(input[end]) {
		end++
	}
	return at, end, string(input[start:end]), string(input[at]), true
}

func SkillQueryAtCursor(input []rune, cursor int) (int, int, string, bool) {
	if len(input) == 0 {
		return 0, 0, "", false
	}
	cursor = normalizeCursor(input, cursor)
	start := cursor
	for start > 0 && IsSkillQueryRune(input[start-1]) {
		start--
	}
	if start == 0 || input[start-1] != '$' {
		return 0, 0, "", false
	}
	dollar := start - 1
	if !referenceBoundary(input, dollar) {
		return 0, 0, "", false
	}
	end := cursor
	for end < len(input) && IsSkillQueryRune(input[end]) {
		end++
	}
	return dollar, end, string(input[start:end]), true
}

func IsMentionQueryRune(r rune) bool {
	if r == '_' || r == '-' || r == '.' || r == '/' || r == '\\' {
		return true
	}
	return isASCIILetterOrDigit(r)
}

func IsSkillQueryRune(r rune) bool {
	if r == '_' || r == '-' || r == ':' {
		return true
	}
	return isASCIILetterOrDigit(r)
}

func normalizeCursor(input []rune, cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > len(input) {
		return len(input)
	}
	return cursor
}

func referenceBoundary(input []rune, index int) bool {
	if index <= 0 {
		return true
	}
	prev := input[index-1]
	if unicode.IsSpace(prev) {
		return true
	}
	return strings.ContainsRune(`([{,;:"'`, prev)
}

func isASCIILetterOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
