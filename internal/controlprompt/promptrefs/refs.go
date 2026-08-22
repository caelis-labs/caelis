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

// ScanSubmissionReferences finds user-visible /skill and @file tokens, plus
// internal $canonical skill tokens produced by line-leading skill rewrite.
func ScanSubmissionReferences(text string) []Token {
	input := []rune(text)
	tokens := make([]Token, 0, 2)
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '$', '/':
			end, value, ok := scanSkillReference(input, i)
			if !ok {
				continue
			}
			tokens = append(tokens, Token{
				Kind:  KindSkill,
				Start: i,
				End:   end,
				Value: value,
			})
			i = end - 1
		case '@':
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

func scanSkillReference(input []rune, start int) (int, string, bool) {
	if start < 0 || start >= len(input) {
		return 0, "", false
	}
	prefix := input[start]
	if prefix != '$' && prefix != '/' {
		return 0, "", false
	}
	if prefix == '/' {
		if !slashSkillReferenceBoundary(input, start) {
			return 0, "", false
		}
	} else if !referenceBoundary(input, start) {
		return 0, "", false
	}
	end := start + 1
	for end < len(input) && IsSkillQueryRune(input[end]) {
		end++
	}
	if end == start+1 {
		return 0, "", false
	}
	if prefix == '/' && skillTokenLooksLikePath(input, end) {
		return 0, "", false
	}
	if end < len(input) && !IsSkillReferenceTerminator(input[end]) {
		return 0, "", false
	}
	return end, string(input[start+1 : end]), true
}

func skillTokenLooksLikePath(input []rune, end int) bool {
	if end >= len(input) {
		return false
	}
	return input[end] == '/' || input[end] == '\\'
}

func slashSkillReferenceBoundary(input []rune, index int) bool {
	if index <= 0 {
		return true
	}
	prev := input[index-1]
	if unicode.IsSpace(prev) {
		return true
	}
	return strings.ContainsRune(`([{,;"'`, prev)
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
	if start == 0 || input[start-1] != '@' {
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

// IsSkillReferenceTerminator reports whether r may legally follow a /skill
// token. Unicode letters, numbers, and combining marks are treated as part of
// surrounding prose rather than silently truncating to an ASCII Skill name.
func IsSkillReferenceTerminator(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
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
