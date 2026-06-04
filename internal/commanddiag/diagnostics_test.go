package commanddiag

import (
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

func windowsSandboxInput() Input {
	return Input{
		Command:  "go build ./...",
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
		GOOS:     "windows",
	}
}
