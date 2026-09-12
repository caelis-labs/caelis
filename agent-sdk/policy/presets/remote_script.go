package presets

import (
	"slices"
	"strings"
)

// remoteScriptExecution recognizes literal downloader-to-interpreter flows in
// POSIX and PowerShell pipelines, groups, and shell command payloads. It does not
// resolve variables or user-defined aliases, or track downloads saved for later
// commands. This is a risk classifier, not a shell evaluator or execution boundary.
func remoteScriptExecution(command string) bool {
	scan := remoteScriptScanner{tokens: remoteScriptTokens(command)}
	return scan.list(false).execution
}

type remoteScriptFlow struct {
	download  bool
	execution bool
}

type remoteScriptScanner struct {
	tokens []remoteScriptToken
	pos    int
}

// list retains pipeline input across continuations and into groups, but not
// across independent commands. A group's output includes all of its commands.
func (s *remoteScriptScanner) list(input bool) remoteScriptFlow {
	var result remoteScriptFlow
	var fields []string
	groupOutput, pipeInput, afterPipe := false, input, false
	finish := func() remoteScriptFlow {
		flow := remoteScriptCommand(fields, pipeInput, groupOutput)
		fields, groupOutput = nil, false
		return flow
	}
	for s.pos < len(s.tokens) {
		token := s.tokens[s.pos]
		s.pos++
		if !token.operator {
			fields = append(fields, token.text)
			afterPipe = false
			continue
		}
		switch token.text {
		case "(", "{":
			group := s.list(pipeInput)
			if group.execution {
				return group
			}
			groupOutput = groupOutput || group.download
			afterPipe = false
			continue
		case "&":
			// PowerShell's call operator can prefix either pipeline command.
			if len(fields) == 0 && !groupOutput {
				continue
			}
		case "\n":
			if afterPipe {
				continue
			}
		}
		flow := finish()
		if flow.execution {
			return flow
		}
		if token.text == "|" || token.text == "|&" {
			pipeInput, afterPipe = flow.download, true
			continue
		}
		result.download = result.download || flow.download
		if token.text == ")" || token.text == "}" {
			return result
		}
		pipeInput, afterPipe = input, false
	}
	flow := finish()
	result.download = result.download || flow.download
	result.execution = flow.execution
	return result
}

func remoteScriptCommand(fields []string, input, groupOutput bool) remoteScriptFlow {
	flow := remoteScriptFlow{download: input || groupOutput}
	// These words introduce commands, rather than naming the executable.
	for len(fields) > 0 && slices.Contains([]string{"if", "then", "elif", "else", "while", "until", "do", "!", "time"}, fields[0]) {
		fields = fields[1:]
	}
	index := commandStartIndexInSegment(fields, 0)
	if index < 0 {
		return flow
	}
	name := executableBase(fields[index])
	if start, ok := shellPayloadStart(fields, index); ok && start < len(fields) && fields[start] != "-" {
		payload := fields[start]
		if name == "powershell" || name == "powershell.exe" || name == "pwsh" || name == "pwsh.exe" || name == "cmd" || name == "cmd.exe" {
			payload = strings.Join(fields[start:], " ")
		}
		scan := remoteScriptScanner{tokens: remoteScriptTokens(payload)}
		inner := scan.list(input)
		flow.download = flow.download || inner.download
		flow.execution = inner.execution
		return flow
	}
	switch name {
	case "curl", "curl.exe", "wget", "wget.exe", "irm", "invoke-restmethod", "iwr", "invoke-webrequest":
		flow.download = true
	case "sh", "sh.exe", "bash", "bash.exe", "zsh", "zsh.exe", "dash", "dash.exe", "ksh", "ksh.exe", "ash", "ash.exe", "fish", "fish.exe",
		"python", "python3", "python.exe", "python3.exe", "node", "node.exe", "perl", "perl.exe", "ruby", "ruby.exe",
		"powershell", "powershell.exe", "pwsh", "pwsh.exe", "iex", "invoke-expression":
		flow.execution = input || groupOutput
	}
	return flow
}
