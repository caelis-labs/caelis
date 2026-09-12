package presets

import "strings"

type remoteScriptToken struct {
	text     string
	operator bool
}

// remoteScriptTokens keeps quoted words distinct from shell operators. Unlike
// shellishFields, it preserves the structure needed to trace a pipeline across
// newlines and groups without treating comments or quoted command text as code.
// Backslash and PowerShell backtick continuations are accepted; no expansion is
// evaluated here.
func remoteScriptTokens(command string) []remoteScriptToken {
	var tokens []remoteScriptToken
	var word strings.Builder
	started := false
	var quote byte
	flush := func() {
		if started {
			tokens = append(tokens, remoteScriptToken{text: word.String()})
			word.Reset()
			started = false
		}
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote == 0 && !started && strings.HasPrefix(command[i:], "<#") {
			end := strings.Index(command[i+2:], "#>")
			if end < 0 {
				break
			}
			i += end + 3
			continue
		}
		if quote != '\'' && (c == '\\' || c == '`') && i+1 < len(command) {
			next := command[i+1]
			if next == '\n' || next == '\r' {
				i++
				if next == '\r' && i+1 < len(command) && command[i+1] == '\n' {
					i++
				}
				continue
			}
			if strings.ContainsRune(" \\`\"'|&;(){}#", rune(next)) {
				word.WriteByte(next)
				started = true
				i++
				continue
			}
		}
		if quote != 0 {
			if c == quote {
				if quote == '\'' && i+1 < len(command) && command[i+1] == '\'' {
					word.WriteByte(c) // PowerShell's doubled single quote.
					i++
				} else {
					quote = 0
				}
			} else {
				word.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote, started = c, true
		case ' ', '\t':
			flush()
		case '#':
			if started {
				word.WriteByte(c)
				continue
			}
			for i+1 < len(command) && command[i+1] != '\n' && command[i+1] != '\r' {
				i++
			}
		case '\n', '\r', '|', '&', ';', '(', ')', '{', '}':
			if c == '&' && (strings.HasSuffix(word.String(), ">") || strings.HasSuffix(word.String(), "<") || i+1 < len(command) && command[i+1] == '>') {
				word.WriteByte(c) // Redirection (2>&1 / &>file), not a list boundary.
				started = true
				continue
			}
			flush()
			op := string(c)
			if c == '\r' {
				op = "\n"
				if i+1 < len(command) && command[i+1] == '\n' {
					i++
				}
			} else if i+1 < len(command) && (c == '|' && (command[i+1] == '|' || command[i+1] == '&') || c == '&' && command[i+1] == '&') {
				i++
				op += string(command[i])
			}
			tokens = append(tokens, remoteScriptToken{text: op, operator: true})
		default:
			word.WriteByte(c)
			started = true
		}
	}
	flush()
	return tokens
}
