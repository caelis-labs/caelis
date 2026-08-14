package tuiapp

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type shellTokenClass int

const (
	shellTokenSpace shellTokenClass = iota
	shellTokenCommand
	shellTokenKeyword
	shellTokenEnv
	shellTokenFlag
	shellTokenOperator
	shellTokenRedirect
	shellTokenPath
	shellTokenVariable
	shellTokenArg
	shellTokenQuoted
)

type shellCommandToken struct {
	Text  string
	Class shellTokenClass
}

func styleShellCommandText(ctx BlockRenderContext, text string) string {
	text = sanitizeRenderableText(text)
	tokens := shellCommandTokens(text)
	if len(tokens) == 0 {
		return ctx.Theme.ToolArgsStyle().Render(text)
	}
	var styled strings.Builder
	for _, token := range tokens {
		if token.Text == "" {
			continue
		}
		if token.Class == shellTokenSpace {
			styled.WriteString(token.Text)
			continue
		}
		style := shellTokenStyle(ctx, token.Class)
		styled.WriteString(style.Render(tuikit.LinkifyText(token.Text, ctx.Theme.LinkStyle())))
	}
	return styled.String()
}

func shellTokenStyle(ctx BlockRenderContext, class shellTokenClass) lipgloss.Style {
	palette := tuikit.SyntaxPaletteForTheme(ctx.Theme)
	switch class {
	case shellTokenCommand:
		return shellSyntaxStyle(palette.Function).Bold(true)
	case shellTokenKeyword:
		return shellSyntaxStyle(palette.Keyword).Bold(true)
	case shellTokenEnv:
		return shellSyntaxStyle(palette.Variable)
	case shellTokenFlag:
		return shellSyntaxStyle(palette.Number)
	case shellTokenOperator:
		return shellSyntaxStyle(palette.Operator)
	case shellTokenRedirect:
		return shellSyntaxStyle(palette.Number)
	case shellTokenPath:
		return shellSyntaxStyle(palette.Path)
	case shellTokenVariable:
		return shellSyntaxStyle(palette.Variable)
	case shellTokenQuoted:
		return shellSyntaxStyle(palette.String)
	default:
		return ctx.Theme.ToolArgsStyle()
	}
}

func shellSyntaxStyle(fg color.Color) lipgloss.Style {
	style := lipgloss.NewStyle()
	if fg != nil {
		style = style.Foreground(fg)
	}
	return style
}

func shellCommandTokens(text string) []shellCommandToken {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if text == "" {
		return nil
	}
	tokens := make([]shellCommandToken, 0, len(strings.Fields(text))*2+1)
	expectCommand := true
	afterRedirect := false
	for i := 0; i < len(text); {
		if isShellWhitespace(text[i]) {
			start := i
			for i < len(text) && isShellWhitespace(text[i]) {
				i++
			}
			tokens = append(tokens, shellCommandToken{Text: text[start:i], Class: shellTokenSpace})
			continue
		}
		if op, n := shellOperatorAt(text[i:]); n > 0 {
			class := shellTokenOperator
			if shellOperatorIsRedirect(op) {
				class = shellTokenRedirect
				afterRedirect = true
			} else {
				afterRedirect = false
			}
			tokens = append(tokens, shellCommandToken{Text: op, Class: class})
			i += n
			if shellOperatorStartsCommand(op) {
				expectCommand = true
			}
			continue
		}
		start := i
		quoted := false
		if text[i] == '\'' || text[i] == '"' || text[i] == '`' {
			quoted = true
			quote := text[i]
			i++
			for i < len(text) {
				if text[i] == '\\' && i+1 < len(text) {
					i += 2
					continue
				}
				i++
				if text[i-1] == quote {
					break
				}
			}
		} else {
			for i < len(text) && !isShellWhitespace(text[i]) {
				if _, n := shellOperatorAt(text[i:]); n > 0 {
					break
				}
				i++
			}
		}
		raw := text[start:i]
		class := classifyShellToken(raw, quoted, expectCommand, afterRedirect)
		switch {
		case class == shellTokenCommand:
			expectCommand = false
		case class == shellTokenKeyword:
			expectCommand = shellKeywordExpectsCommand(raw)
		case class != shellTokenEnv && class != shellTokenSpace && expectCommand:
			expectCommand = false
		}
		afterRedirect = false
		tokens = append(tokens, shellCommandToken{Text: raw, Class: class})
	}
	return tokens
}

func classifyShellToken(token string, quoted bool, expectCommand bool, afterRedirect bool) shellTokenClass {
	if quoted {
		return shellTokenQuoted
	}
	if afterRedirect {
		if isShellPathToken(token) {
			return shellTokenPath
		}
		return shellTokenArg
	}
	if isShellEnvAssignment(token) && expectCommand {
		return shellTokenEnv
	}
	if expectCommand {
		if isShellKeyword(token) {
			return shellTokenKeyword
		}
		return shellTokenCommand
	}
	if isShellKeyword(token) {
		return shellTokenKeyword
	}
	if isShellVariableToken(token) {
		return shellTokenVariable
	}
	if strings.HasPrefix(token, "-") {
		return shellTokenFlag
	}
	if isShellPathToken(token) {
		return shellTokenPath
	}
	return shellTokenArg
}

func isShellWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n'
}

func shellOperatorAt(text string) (string, int) {
	for _, op := range []string{
		"2>&1", "1>&2", "&>>", "&>", "2>>", "1>>", ">>", "<<<", "<<", "2>", "1>", ">|",
		"&&", "||", "|&", ";;", "|", ";", ">", "<", "(", ")", "{", "}",
	} {
		if strings.HasPrefix(text, op) {
			return op, len(op)
		}
	}
	return "", 0
}

func shellOperatorIsRedirect(op string) bool {
	return strings.ContainsAny(op, "><")
}

func shellOperatorStartsCommand(op string) bool {
	switch op {
	case "&&", "||", "|", "|&", ";", ";;", "(":
		return true
	default:
		return false
	}
}

func isShellEnvAssignment(token string) bool {
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		return false
	}
	for i := 0; i < idx; i++ {
		ch := token[i]
		if i == 0 {
			if !isShellEnvNameStart(ch) {
				return false
			}
			continue
		}
		if !isShellEnvNameChar(ch) {
			return false
		}
	}
	return true
}

func isShellKeyword(token string) bool {
	switch token {
	case "if", "then", "else", "elif", "fi",
		"for", "select", "while", "until", "do", "done",
		"case", "in", "esac", "function", "time", "coproc":
		return true
	default:
		return false
	}
}

func shellKeywordExpectsCommand(token string) bool {
	switch token {
	case "then", "else", "elif", "do", "time", "coproc", "function":
		return true
	default:
		return false
	}
}

func isShellVariableToken(token string) bool {
	if strings.HasPrefix(token, "$") {
		return true
	}
	return strings.HasPrefix(token, "${")
}

func isShellPathToken(token string) bool {
	token = strings.Trim(token, `"'`)
	switch {
	case token == "":
		return false
	case strings.HasPrefix(token, "/"):
		return true
	case strings.HasPrefix(token, "./"), strings.HasPrefix(token, "../"), strings.HasPrefix(token, "~/"):
		return true
	case strings.Contains(token, "/") && !strings.Contains(token, "://"):
		return true
	default:
		return false
	}
}

func isShellEnvNameStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isShellEnvNameChar(ch byte) bool {
	return isShellEnvNameStart(ch) || ch >= '0' && ch <= '9'
}
