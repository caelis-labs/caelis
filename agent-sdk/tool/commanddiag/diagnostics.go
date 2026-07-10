package commanddiag

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

const (
	CodeWindowsMSYSSSHSignalPipe   = "windows_msys_ssh_signal_pipe"
	CodeWindowsSChannelCredentials = "windows_schannel_no_credentials"
	CodeGitIndexLockSandboxDenied  = "git_index_lock_sandbox_denied"
	CodeWindowsGitCredential       = "windows_git_credential_sandbox_incompatible"
	CodeGoPrivateDependency        = "go_private_dependency_sandbox_incompatible"
	CodeSandboxCacheDenied         = "sandbox_cache_write_denied"
	CodeWindowsSandboxACLDenied    = "windows_sandbox_acl_denied"
	CodeHostExecutionApproval      = "host_execution_requires_approval"
)

const (
	// hintEscalationOnce is the only allowed escalation teaching string: same
	// command, once, no habit transfer to later similar commands.
	hintEscalationOnce = "If still required, retry THIS SAME command once with sandbox_permissions=require_escalated and a justification that cites this failure. Do not escalate later similar commands by habit."

	hintWindowsMSYSSSHSignalPipe = "Git for Windows MSYS ssh appears incompatible with the Windows restricted-token sandbox. Prefer GIT_SSH_COMMAND=C:/Windows/System32/OpenSSH/ssh.exe when available. " + hintEscalationOnce
	hintWindowsSChannel          = "Windows SChannel TLS can fail under the restricted-token sandbox. Prefer Python/Node/OpenSSL-backed HTTPS or native alternatives. " + hintEscalationOnce
	hintGitIndexLockSandbox      = "Git index write is blocked by sandbox permissions (write path). " + hintEscalationOnce
	hintWindowsGitCredential     = "Git credential helpers can fail under the Windows restricted-token sandbox. " + hintEscalationOnce
	hintGoPrivateDependency      = "Go private dependency resolution can hit Windows sandbox TLS or credential-helper limits. " + hintEscalationOnce
	hintSandboxCacheDenied       = "A sandboxed tool could not write its cache path. Clean/reset the sandbox cache if corrupt. " + hintEscalationOnce
	hintWindowsSandboxACLDenied  = "Windows sandbox ACL preparation failed for a required foreground path. Run `/doctor` or `caelis sandbox fix` when convenient. " + hintEscalationOnce
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
	Code                        string
	Hint                        string
	Severity                    string
	RetryableWithHost           bool
	SuggestedSandboxPermissions string
	SuggestedPrefixRule         []string
}

func Best(input Input) (Diagnostic, bool) {
	if !isFailedCommand(input) {
		return Diagnostic{}, false
	}
	text := diagnosticText(input)
	lower := strings.ToLower(text)
	if isHostExecutionApprovalRequired(lower) {
		return hostRetryDiagnostic(input, CodeHostExecutionApproval, sandbox.HostExecutionRequiresApprovalMessage), true
	}
	if isGitIndexLockSandboxDenied(input, lower) {
		return hostRetryDiagnostic(input, CodeGitIndexLockSandboxDenied, hintGitIndexLockSandbox), true
	}
	if isSandboxCacheDenied(input, lower) {
		return hostRetryDiagnostic(input, CodeSandboxCacheDenied, hintSandboxCacheDenied), true
	}
	if !isWindowsSandbox(input) {
		return Diagnostic{}, false
	}
	switch {
	case isWindowsSandboxACLSetupDenied(lower):
		return hostRetryDiagnostic(input, CodeWindowsSandboxACLDenied, hintWindowsSandboxACLDenied), true
	case isMSYSSSHSignalPipeFailure(lower):
		return hostRetryDiagnostic(input, CodeWindowsMSYSSSHSignalPipe, hintWindowsMSYSSSHSignalPipe), true
	case isSChannelNoCredentialsFailure(lower):
		return hostRetryDiagnostic(input, CodeWindowsSChannelCredentials, hintWindowsSChannel), true
	case isWindowsGitCredentialFailure(lower):
		return hostRetryDiagnostic(input, CodeWindowsGitCredential, hintWindowsGitCredential), true
	case isGoPrivateDependencyFailure(input, lower):
		return hostRetryDiagnostic(input, CodeGoPrivateDependency, hintGoPrivateDependency), true
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
	switch sandbox.CanonicalBackend(input.Backend) {
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

func isFailedCommand(input Input) bool {
	return input.ExitCode != 0 || strings.TrimSpace(input.Error) != ""
}

func isHostExecutionApprovalRequired(lower string) bool {
	return strings.Contains(lower, strings.ToLower(sandbox.HostExecutionRequiresApprovalMessage)) ||
		strings.Contains(lower, "host execution requires approval")
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

func hostRetryDiagnostic(input Input, code string, hint string) Diagnostic {
	return Diagnostic{
		Code:                        code,
		Hint:                        hint,
		Severity:                    "warning",
		RetryableWithHost:           true,
		SuggestedSandboxPermissions: "require_escalated",
		SuggestedPrefixRule:         suggestedPrefixRule(input.Command),
	}
}

func suggestedPrefixRule(command string) []string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return nil
	}
	first := strings.ToLower(commandBase(fields[0]))
	switch first {
	case "go", "go.exe":
		if len(fields) >= 2 {
			switch strings.ToLower(strings.Trim(fields[1], `"'`)) {
			case "test", "mod", "list", "get", "work", "run", "build":
				return []string{fields[0], fields[1]}
			}
		}
	case "git", "git.exe":
		if len(fields) >= 2 && isGitSubcommand(fields[1]) {
			return []string{fields[0], fields[1]}
		}
	}
	return nil
}

func isGitSubcommand(value string) bool {
	switch strings.ToLower(strings.Trim(value, `"'`)) {
	case "add", "clone", "fetch", "pull", "push", "submodule":
		return true
	default:
		return false
	}
}

func isWindowsSandboxACLSetupDenied(lower string) bool {
	if !strings.Contains(lower, "impl/sandbox/windows") {
		return false
	}
	if !strings.Contains(lower, "acl") && !strings.Contains(lower, "dacl") && !strings.Contains(lower, "write_dac") {
		return false
	}
	return strings.Contains(lower, "access is denied") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "拒绝访问")
}

func isWindowsGitCredentialFailure(lower string) bool {
	if !strings.Contains(lower, "credential") {
		return false
	}
	if !strings.Contains(lower, "git") &&
		!strings.Contains(lower, "credential-manager") &&
		!strings.Contains(lower, "git-credential-manager") &&
		!strings.Contains(lower, "manager-core") {
		return false
	}
	return strings.Contains(lower, "access is denied") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "no credentials") ||
		strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "failed to")
}

func isGoPrivateDependencyFailure(input Input, lower string) bool {
	fields := strings.Fields(strings.TrimSpace(input.Command))
	if len(fields) == 0 || !strings.EqualFold(strings.TrimSuffix(commandBase(fields[0]), ".exe"), "go") {
		return false
	}
	if !strings.Contains(lower, "go:") && !strings.Contains(lower, "module") && !strings.Contains(lower, "git ls-remote") {
		return false
	}
	credentialEvidence := strings.Contains(lower, "could not read username") ||
		strings.Contains(lower, "terminal prompts disabled") ||
		strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "credential")
	tlsEvidence := strings.Contains(lower, "schannel") ||
		strings.Contains(lower, "sec_e_no_credentials") ||
		strings.Contains(lower, "no credentials")
	sandboxEvidence := sandbox.IsSandboxPermissionDeniedText(lower) ||
		strings.Contains(lower, "access is denied") ||
		strings.Contains(lower, "permission denied")
	return strings.Contains(lower, "could not read username") ||
		strings.Contains(lower, "terminal prompts disabled") ||
		(strings.Contains(lower, "git ls-remote") && (credentialEvidence || tlsEvidence || sandboxEvidence)) ||
		(strings.Contains(lower, "module") && (credentialEvidence || tlsEvidence))
}

func commandBase(raw string) string {
	raw = strings.Trim(strings.TrimSpace(raw), `"'`)
	if raw == "" {
		return ""
	}
	lastSlash := strings.LastIndexAny(raw, `\/`)
	if lastSlash >= 0 && lastSlash+1 < len(raw) {
		raw = raw[lastSlash+1:]
	}
	return raw
}

func isSandboxCacheDenied(input Input, lower string) bool {
	if !isSandbox(input) {
		return false
	}
	if !sandbox.IsSandboxCachePathEvidenceText(lower) {
		return false
	}
	return sandbox.IsSandboxPermissionDeniedText(lower)
}
