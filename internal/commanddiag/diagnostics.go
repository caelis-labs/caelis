package commanddiag

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

const (
	CodeWindowsMSYSSSHSignalPipe   = "windows_msys_ssh_signal_pipe"
	CodeWindowsSChannelCredentials = "windows_schannel_no_credentials"
	CodeGitIndexLockSandboxDenied  = "git_index_lock_sandbox_denied"
)

const (
	hintWindowsMSYSSSHSignalPipe = "Git for Windows MSYS ssh appears incompatible with the Windows restricted-token sandbox. Retry with GIT_SSH_COMMAND=C:/Windows/System32/OpenSSH/ssh.exe if that binary exists, or run dependency download outside the sandbox."
	hintWindowsSChannel          = "Windows SChannel TLS can fail under the restricted-token sandbox. Prefer Python/Node/OpenSSL-backed HTTPS, use native alternatives, or rerun the specific network operation outside the sandbox."
	hintGitIndexLockSandbox      = "Git index write is blocked by sandbox permissions; retry the original Git command with escalation."
)

type Input struct {
	ToolName string
	Command  string
	Stdout   string
	Stderr   string
	Error    string
	ExitCode int
	Route    sandbox.Route
	Backend  sandbox.Backend
	GOOS     string
}

type Diagnostic struct {
	Code     string
	Hint     string
	Severity string
}

func Best(input Input) (Diagnostic, bool) {
	if !isFailedCommand(input) {
		return Diagnostic{}, false
	}
	text := diagnosticText(input)
	lower := strings.ToLower(text)
	if isGitIndexLockSandboxDenied(input, lower) {
		return Diagnostic{Code: CodeGitIndexLockSandboxDenied, Hint: hintGitIndexLockSandbox, Severity: "warning"}, true
	}
	if !isWindowsSandbox(input) {
		return Diagnostic{}, false
	}
	switch {
	case isMSYSSSHSignalPipeFailure(lower):
		return Diagnostic{Code: CodeWindowsMSYSSSHSignalPipe, Hint: hintWindowsMSYSSSHSignalPipe, Severity: "warning"}, true
	case isSChannelNoCredentialsFailure(lower):
		return Diagnostic{Code: CodeWindowsSChannelCredentials, Hint: hintWindowsSChannel, Severity: "warning"}, true
	default:
		return Diagnostic{}, false
	}
}

func Detect(input Input) []Diagnostic {
	best, ok := Best(input)
	if !ok {
		return nil
	}
	return []Diagnostic{best}
}

func isWindowsSandbox(input Input) bool {
	if goos := strings.TrimSpace(input.GOOS); goos != "" && !strings.EqualFold(goos, "windows") {
		return false
	}
	if !isSandbox(input) {
		return false
	}
	switch normalizeWindowsBackend(input.Backend) {
	case sandbox.BackendWindows:
		return true
	default:
		return false
	}
}

func isSandbox(input Input) bool {
	if input.Route != sandbox.RouteSandbox {
		return false
	}
	switch input.Backend {
	case "", sandbox.BackendHost:
		return false
	default:
		return true
	}
}

func normalizeWindowsBackend(backend sandbox.Backend) sandbox.Backend {
	switch strings.ToLower(strings.TrimSpace(string(backend))) {
	case "windows", "windows-restricted-token", "windows_restricted_token", "windows-elevated", "windows_elevated", "elevated":
		return sandbox.BackendWindows
	default:
		return backend
	}
}

func isFailedCommand(input Input) bool {
	return input.ExitCode != 0 || strings.TrimSpace(input.Error) != ""
}

func diagnosticText(input Input) string {
	var parts []string
	for _, value := range []string{input.Command, input.Stdout, input.Stderr, input.Error} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "\n")
}

func isMSYSSSHSignalPipeFailure(lower string) bool {
	if !strings.Contains(lower, "fatal error") ||
		!strings.Contains(lower, "couldn't create signal pipe") ||
		!strings.Contains(lower, "win32 error 5") {
		return false
	}
	return strings.Contains(lower, `usr\bin\ssh.exe`) ||
		strings.Contains(lower, "usr/bin/ssh.exe") ||
		strings.Contains(lower, `git\usr\bin\ssh.exe`) ||
		strings.Contains(lower, "git/usr/bin/ssh.exe")
}

func isSChannelNoCredentialsFailure(lower string) bool {
	if strings.Contains(lower, "schannel: acquirecredentialshandle failed") &&
		(strings.Contains(lower, "sec_e_no_credentials") || strings.Contains(lower, "0x8009030e")) {
		return true
	}
	if !strings.Contains(lower, "system.componentmodel.win32exception") {
		return false
	}
	return strings.Contains(lower, "安全包中没有可用的凭证") ||
		strings.Contains(lower, "no credentials are available in the security package") ||
		strings.Contains(lower, "sec_e_no_credentials") ||
		strings.Contains(lower, "0x8009030e")
}

func isGitIndexLockSandboxDenied(input Input, lower string) bool {
	if !isSandbox(input) {
		return false
	}
	if !hasGitIndexLockEvidence(lower) {
		return false
	}
	return sandbox.IsSandboxPermissionDeniedText(lower)
}

func hasGitIndexLockEvidence(lower string) bool {
	if strings.Contains(lower, ".git/index.lock") || strings.Contains(lower, `.git\index.lock`) {
		return true
	}
	if !strings.Contains(lower, "index.lock") {
		return false
	}
	return strings.Contains(lower, "could not lock index") ||
		strings.Contains(lower, "unable to create") ||
		strings.Contains(lower, "unable to create file") ||
		strings.Contains(lower, "fatal:")
}
