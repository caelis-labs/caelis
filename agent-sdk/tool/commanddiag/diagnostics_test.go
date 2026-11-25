package commanddiag

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestHintDetectsWindowsMSYSSSHSignalPipe(t *testing.T) {
	input := windowsSandboxInput()
	input.Stderr = `      0 [main] ssh (17912) D:\xue\Git\usr\bin\ssh.exe: *** fatal error - couldn't create signal pipe, Win32 error 5
fatal: Could not read from remote repository.`

	got := Hint(input)
	if !strings.Contains(got, "GIT_SSH_COMMAND=C:/Windows/System32/OpenSSH/ssh.exe") || !strings.Contains(got, "sandbox_permissions=require_escalated") || !strings.Contains(got, "THIS SAME command once") {
		t.Fatalf("Hint = %q, want native OpenSSH and once-only escalation guidance", got)
	}
}

func TestHintDetectsHostExecutionRequiresApproval(t *testing.T) {
	input := Input{
		Command:  "git log --oneline -3",
		Error:    "ports/sandbox: " + sandbox.HostExecutionRequiresApprovalMessage,
		ExitCode: 1,
		Route:    sandbox.RouteHost,
		Backend:  sandbox.BackendHost,
	}

	got := Hint(input)
	if got != sandbox.HostExecutionRequiresApprovalMessage {
		t.Fatalf("Hint() = %q, want %q", got, sandbox.HostExecutionRequiresApprovalMessage)
	}
}

func TestHintDoesNotLabelNativeOpenSSHPublicKeyFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Stderr = "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no diagnostic for normal remote auth failure", got)
	}
}

func TestHintRequiresWindowsSandboxBackend(t *testing.T) {
	input := windowsSandboxInput()
	input.Backend = sandbox.BackendHost
	input.Stderr = `C:\Program Files\Git\usr\bin\ssh.exe: *** fatal error - couldn't create signal pipe, Win32 error 5`

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no diagnostic outside Windows sandbox backend", got)
	}
}

func TestHintDetectsCurlSChannelNoCredentials(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "curl.exe https://private.example.com"
	input.Stderr = "curl: (35) schannel: AcquireCredentialsHandle failed: SEC_E_NO_CREDENTIALS (0x8009030E) - 安全包中没有可用的凭证"

	got := Hint(input)
	if !strings.Contains(got, "SChannel TLS can fail") || !strings.Contains(got, "sandbox_permissions=require_escalated") || !strings.Contains(got, "THIS SAME command once") {
		t.Fatalf("Hint = %q, want SChannel and once-only escalation guidance", got)
	}
}

func TestHintDetectsDotNetSChannelNoCredentials(t *testing.T) {
	input := windowsSandboxInput()
	input.Stdout = "TLS_ERR inner_type=System.ComponentModel.Win32Exception\nTLS_ERR inner_message=安全包中没有可用的凭证"

	got := Hint(input)
	if got != hintWindowsSChannel {
		t.Fatalf("Hint() = %q, want %q", got, hintWindowsSChannel)
	}
}

func TestHintDetectsEnglishDotNetSChannelNoCredentials(t *testing.T) {
	input := windowsSandboxInput()
	input.Stdout = "TLS_ERR inner_type=System.ComponentModel.Win32Exception\nTLS_ERR inner_message=No credentials are available in the security package"

	got := Hint(input)
	if got != hintWindowsSChannel {
		t.Fatalf("Hint() = %q, want %q", got, hintWindowsSChannel)
	}
}

func TestHintDoesNotLabelOrdinaryTLSCertificateFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Stderr = "curl: (60) schannel: CertGetCertificateChain trust error CERT_TRUST_IS_UNTRUSTED_ROOT"

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no diagnostic for ordinary certificate validation failure", got)
	}
}

func TestHintDetectsGitIndexLockSandboxDenied(t *testing.T) {
	input := sandboxInput()
	input.Command = "git add ."
	input.Stderr = "fatal: Unable to create '/workspace/.git/index.lock': Read-only file system"

	got := Hint(input)
	if !strings.Contains(got, "Git index write is blocked") || !strings.Contains(got, "THIS SAME command once") {
		t.Fatalf("Hint = %q, want once-only Git index sandbox guidance", got)
	}
}

func TestHintDetectsGitIndexLockAfterGlobalOptions(t *testing.T) {
	input := sandboxInput()
	input.Command = `git -C C:\repo add .`
	input.Stderr = "fatal: Unable to create 'C:/repo/.git/index.lock': Access is denied."

	got := Hint(input)
	if got != hintGitIndexLockSandbox {
		t.Fatalf("Hint() = %q, want %q", got, hintGitIndexLockSandbox)
	}
}

func TestHintDetectsGoPrivateDependencySandboxFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "go test ./..."
	input.Stderr = "go: module private.example.com/repo: git ls-remote -q origin in C:\\cache: terminal prompts disabled"

	got := Hint(input)
	if got != hintGoPrivateDependency {
		t.Fatalf("Hint() = %q, want %q", got, hintGoPrivateDependency)
	}
}

func TestHintDoesNotTreatGoRepositoryNotFoundAsSandboxFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "go get example.com/missing/module"
	input.Stderr = "go: module example.com/missing/module: git ls-remote -q origin: repository not found"

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no sandbox diagnostic for ordinary missing repository", got)
	}
}

func TestHintDetectsWindowsSandboxACLDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Error = `impl/sandbox/windows: apply writable root ACL C:\repo: current token cannot update the directory DACL: Access is denied.`

	got := Hint(input)
	if got != hintWindowsSandboxACLDenied {
		t.Fatalf("Hint() = %q, want %q", got, hintWindowsSandboxACLDenied)
	}
}

func TestHintDetectsSandboxCacheDenied(t *testing.T) {
	input := sandboxInput()
	input.Command = "go test ./..."
	input.Stderr = "go: writing stat cache: open /home/test/go/pkg/mod/cache/download/private/@v/v0.tmp: read-only file system"

	got := Hint(input)
	if got != hintSandboxCacheDenied {
		t.Fatalf("Hint() = %q, want %q", got, hintSandboxCacheDenied)
	}
}

func TestHintDetectsNuGetPackageCacheDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "dotnet restore"
	input.Stderr = `Access to the path 'C:\Users\me\.nuget\packages\private\1.0.0' is denied.`

	got := Hint(input)
	if got != hintSandboxCacheDenied {
		t.Fatalf("Hint() = %q, want %q", got, hintSandboxCacheDenied)
	}
}

func TestHintDetectsRedirectedNPMCacheDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "npm install"
	input.Stderr = `Error: EACCES: permission denied, mkdir 'C:\state\.sandbox\env\current\cache\npm\_cacache'`

	got := Hint(input)
	if got != hintSandboxCacheDenied {
		t.Fatalf("Hint() = %q, want %q", got, hintSandboxCacheDenied)
	}
}

func TestHintDoesNotLabelPackageCachePathWithoutPermissionEvidence(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "dotnet restore"
	input.Stderr = `Package C:\Users\me\.nuget\packages\missing\1.0.0 was not found.`

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no cache diagnostic without permission evidence", got)
	}
}

func TestHintDoesNotLabelGenericCacheDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "tool sync"
	input.Stderr = "remote cache policy is denied."

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no cache diagnostic without tool cache evidence", got)
	}
}

func TestHintDetectsPipCacheDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "python -m pip install private-package"
	input.Stderr = "pip cache write failed: access is denied"

	got := Hint(input)
	if got != hintSandboxCacheDenied {
		t.Fatalf("Hint() = %q, want %q", got, hintSandboxCacheDenied)
	}
}

func TestHintDoesNotHintGitIndexLockWithoutPermissionEvidence(t *testing.T) {
	input := sandboxInput()
	input.Command = "git add ."
	input.Stderr = "fatal: Unable to create '/workspace/.git/index.lock': File exists."

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no diagnostic for lock contention without permission evidence", got)
	}
}

func TestHintDoesNotHintPermissionDeniedWithoutGitIndexLock(t *testing.T) {
	input := sandboxInput()
	input.Command = "touch notes.txt"
	input.Stderr = "touch: notes.txt: Permission denied"

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no diagnostic without Git index lock evidence", got)
	}
}

func TestHintDoesNotHintGitIndexLockOutsideSandbox(t *testing.T) {
	input := sandboxInput()
	input.Route = sandbox.RouteHost
	input.Backend = sandbox.BackendHost
	input.Command = "git add ."
	input.Stderr = "fatal: Unable to create '/workspace/.git/index.lock': Permission denied"

	if got := Hint(input); got != "" {
		t.Fatalf("Hint() = %q, want no diagnostic outside sandbox execution", got)
	}
}

func windowsSandboxInput() Input {
	return Input{
		Command:  "go build ./...",
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
	}
}

func sandboxInput() Input {
	return Input{
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendBwrap,
	}
}
