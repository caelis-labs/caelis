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
