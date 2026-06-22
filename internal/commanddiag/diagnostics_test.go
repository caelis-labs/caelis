package commanddiag

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestBestDetectsWindowsMSYSSSHSignalPipe(t *testing.T) {
	input := windowsSandboxInput()
	input.Stderr = `      0 [main] ssh (17912) D:\xue\Git\usr\bin\ssh.exe: *** fatal error - couldn't create signal pipe, Win32 error 5
fatal: Could not read from remote repository.`

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want MSYS ssh diagnostic")
	}
	if got.Code != CodeWindowsMSYSSSHSignalPipe {
		t.Fatalf("Code = %q, want %q", got.Code, CodeWindowsMSYSSSHSignalPipe)
	}
	if got.Hint == "" || got.Severity != "warning" {
		t.Fatalf("Diagnostic = %+v, want user-visible warning hint", got)
	}
	if !got.RetryableWithHost || got.SuggestedSandboxPermissions != "require_escalated" {
		t.Fatalf("Diagnostic = %+v, want host retry metadata", got)
	}
	if !strings.Contains(got.Hint, "sandbox_permissions=require_escalated") {
		t.Fatalf("Hint = %q, want concrete escalation parameter", got.Hint)
	}
}

func TestBestDoesNotLabelNativeOpenSSHPublicKeyFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Stderr = "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no diagnostic for normal remote auth failure", got)
	}
}

func TestBestRequiresWindowsSandboxBackend(t *testing.T) {
	input := windowsSandboxInput()
	input.Backend = sandbox.BackendHost
	input.Stderr = `C:\Program Files\Git\usr\bin\ssh.exe: *** fatal error - couldn't create signal pipe, Win32 error 5`

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no diagnostic outside Windows sandbox backend", got)
	}
}

func TestBestDetectsCurlSChannelNoCredentials(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "curl.exe https://private.example.com"
	input.Stderr = "curl: (35) schannel: AcquireCredentialsHandle failed: SEC_E_NO_CREDENTIALS (0x8009030E) - 安全包中没有可用的凭证"

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want SChannel diagnostic")
	}
	if got.Code != CodeWindowsSChannelCredentials {
		t.Fatalf("Code = %q, want %q", got.Code, CodeWindowsSChannelCredentials)
	}
	if !strings.Contains(got.Hint, "sandbox_permissions=require_escalated") {
		t.Fatalf("Hint = %q, want concrete escalation parameter", got.Hint)
	}
	if !got.RetryableWithHost || got.SuggestedPrefixRule != nil {
		t.Fatalf("Diagnostic = %+v, want host retry metadata without broad prefix rule", got)
	}
}

func TestBestDetectsDotNetSChannelNoCredentials(t *testing.T) {
	input := windowsSandboxInput()
	input.Stdout = "TLS_ERR inner_type=System.ComponentModel.Win32Exception\nTLS_ERR inner_message=安全包中没有可用的凭证"

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want .NET SChannel diagnostic")
	}
	if got.Code != CodeWindowsSChannelCredentials {
		t.Fatalf("Code = %q, want %q", got.Code, CodeWindowsSChannelCredentials)
	}
}

func TestBestDetectsEnglishDotNetSChannelNoCredentials(t *testing.T) {
	input := windowsSandboxInput()
	input.Stdout = "TLS_ERR inner_type=System.ComponentModel.Win32Exception\nTLS_ERR inner_message=No credentials are available in the security package"

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want English .NET SChannel diagnostic")
	}
	if got.Code != CodeWindowsSChannelCredentials {
		t.Fatalf("Code = %q, want %q", got.Code, CodeWindowsSChannelCredentials)
	}
}

func TestBestDoesNotLabelOrdinaryTLSCertificateFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Stderr = "curl: (60) schannel: CertGetCertificateChain trust error CERT_TRUST_IS_UNTRUSTED_ROOT"

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no diagnostic for ordinary certificate validation failure", got)
	}
}

func TestBestDetectsGitIndexLockSandboxDenied(t *testing.T) {
	input := sandboxInput()
	input.Command = "git add ."
	input.Stderr = "fatal: Unable to create '/workspace/.git/index.lock': Read-only file system"

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want Git index lock diagnostic")
	}
	if got.Code != CodeGitIndexLockSandboxDenied {
		t.Fatalf("Code = %q, want %q", got.Code, CodeGitIndexLockSandboxDenied)
	}
	if got.Hint != hintGitIndexLockSandbox {
		t.Fatalf("Hint = %q, want short precise hint %q", got.Hint, hintGitIndexLockSandbox)
	}
	if !got.RetryableWithHost || got.SuggestedSandboxPermissions != "require_escalated" {
		t.Fatalf("Diagnostic = %+v, want host retry metadata", got)
	}
	if !sameStrings(got.SuggestedPrefixRule, []string{"git", "add"}) {
		t.Fatalf("SuggestedPrefixRule = %#v, want git add", got.SuggestedPrefixRule)
	}
}

func TestBestDoesNotSuggestGitGlobalOptionPrefixRule(t *testing.T) {
	input := sandboxInput()
	input.Command = `git -C C:\repo add .`
	input.Stderr = "fatal: Unable to create 'C:/repo/.git/index.lock': Access is denied."

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want Git index lock diagnostic")
	}
	if got.SuggestedPrefixRule != nil {
		t.Fatalf("SuggestedPrefixRule = %#v, want nil for git global option command", got.SuggestedPrefixRule)
	}
}

func TestBestDetectsGoPrivateDependencySandboxFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "go test ./..."
	input.Stderr = "go: module private.example.com/repo: git ls-remote -q origin in C:\\cache: terminal prompts disabled"

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want Go private dependency diagnostic")
	}
	if got.Code != CodeGoPrivateDependency {
		t.Fatalf("Code = %q, want %q", got.Code, CodeGoPrivateDependency)
	}
	if got.SuggestedSandboxPermissions != "require_escalated" || !sameStrings(got.SuggestedPrefixRule, []string{"go", "test"}) {
		t.Fatalf("Diagnostic = %+v, want go test host retry metadata", got)
	}
}

func TestBestDoesNotTreatGoRepositoryNotFoundAsSandboxFailure(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "go get example.com/missing/module"
	input.Stderr = "go: module example.com/missing/module: git ls-remote -q origin: repository not found"

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no sandbox diagnostic for ordinary missing repository", got)
	}
}

func TestBestDetectsWindowsSandboxACLDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Error = `impl/sandbox/windows: apply writable root ACL C:\repo: current token cannot update the directory DACL: Access is denied.`

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want Windows ACL diagnostic")
	}
	if got.Code != CodeWindowsSandboxACLDenied {
		t.Fatalf("Code = %q, want %q", got.Code, CodeWindowsSandboxACLDenied)
	}
	if !got.RetryableWithHost {
		t.Fatalf("Diagnostic = %+v, want retryable host path", got)
	}
}

func TestBestDetectsSandboxCacheDenied(t *testing.T) {
	input := sandboxInput()
	input.Command = "go test ./..."
	input.Stderr = "go: writing stat cache: open /home/test/go/pkg/mod/cache/download/private/@v/v0.tmp: read-only file system"

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want cache denied diagnostic")
	}
	if got.Code != CodeSandboxCacheDenied {
		t.Fatalf("Code = %q, want %q", got.Code, CodeSandboxCacheDenied)
	}
	if got.SuggestedSandboxPermissions != "require_escalated" {
		t.Fatalf("Diagnostic = %+v, want escalation metadata", got)
	}
}

func TestBestDetectsNuGetPackageCacheDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "dotnet restore"
	input.Stderr = `Access to the path 'C:\Users\me\.nuget\packages\private\1.0.0' is denied.`

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want NuGet package cache diagnostic")
	}
	if got.Code != CodeSandboxCacheDenied {
		t.Fatalf("Code = %q, want %q", got.Code, CodeSandboxCacheDenied)
	}
	if got.SuggestedPrefixRule != nil {
		t.Fatalf("SuggestedPrefixRule = %#v, want nil for dotnet command", got.SuggestedPrefixRule)
	}
}

func TestBestDetectsRedirectedNPMCacheDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "npm install"
	input.Stderr = `Error: EACCES: permission denied, mkdir 'C:\state\.sandbox\env\current\cache\npm\_cacache'`

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want redirected npm cache diagnostic")
	}
	if got.Code != CodeSandboxCacheDenied {
		t.Fatalf("Code = %q, want %q", got.Code, CodeSandboxCacheDenied)
	}
	if got.SuggestedPrefixRule != nil {
		t.Fatalf("SuggestedPrefixRule = %#v, want nil for npm command", got.SuggestedPrefixRule)
	}
}

func TestBestDoesNotLabelPackageCachePathWithoutPermissionEvidence(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "dotnet restore"
	input.Stderr = `Package C:\Users\me\.nuget\packages\missing\1.0.0 was not found.`

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no cache diagnostic without permission evidence", got)
	}
}

func TestBestDoesNotLabelGenericCacheDenied(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "tool sync"
	input.Stderr = "remote cache policy is denied."

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no cache diagnostic without tool cache evidence", got)
	}
}

func TestBestDoesNotSuggestBroadPrefixRule(t *testing.T) {
	input := windowsSandboxInput()
	input.Command = "python -m pip install private-package"
	input.Stderr = "pip cache write failed: access is denied"

	got, ok := Best(input)
	if !ok {
		t.Fatal("Best() ok = false, want cache diagnostic")
	}
	if got.SuggestedPrefixRule != nil {
		t.Fatalf("SuggestedPrefixRule = %#v, want nil for broad command", got.SuggestedPrefixRule)
	}
}

func TestBestDoesNotHintGitIndexLockWithoutPermissionEvidence(t *testing.T) {
	input := sandboxInput()
	input.Command = "git add ."
	input.Stderr = "fatal: Unable to create '/workspace/.git/index.lock': File exists."

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no diagnostic for lock contention without permission evidence", got)
	}
}

func TestBestDoesNotHintPermissionDeniedWithoutGitIndexLock(t *testing.T) {
	input := sandboxInput()
	input.Command = "touch notes.txt"
	input.Stderr = "touch: notes.txt: Permission denied"

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no diagnostic without Git index lock evidence", got)
	}
}

func TestBestDoesNotHintGitIndexLockOutsideSandbox(t *testing.T) {
	input := sandboxInput()
	input.Route = sandbox.RouteHost
	input.Backend = sandbox.BackendHost
	input.Command = "git add ."
	input.Stderr = "fatal: Unable to create '/workspace/.git/index.lock': Permission denied"

	if got, ok := Best(input); ok {
		t.Fatalf("Best() = %+v, want no diagnostic outside sandbox execution", got)
	}
}

func windowsSandboxInput() Input {
	return Input{
		Command:  "go build ./...",
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
		GOOS:     "windows",
	}
}

func sandboxInput() Input {
	return Input{
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendLandlock,
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
