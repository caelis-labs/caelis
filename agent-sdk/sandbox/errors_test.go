package sandbox

import (
	"fmt"
	"testing"
)

func TestSandboxPermissionDetailDetectsStdoutRedirectedDiagnostics(t *testing.T) {
	t.Parallel()

	const deniedPath = "/home/test/go/pkg/mod/cache/download/code.example/internal/system/@v/v0.0.0.tmp"
	detail, ok := SandboxPermissionDetail(CommandResult{
		Stdout:  "go: writing stat cache: open " + deniedPath + ": read-only file system\n",
		Route:   RouteSandbox,
		Backend: BackendBwrap,
	}, fmt.Errorf("exit status 1"))
	if !ok {
		t.Fatal("SandboxPermissionDetail() ok = false, want true")
	}
	if detail != SandboxPermissionDeniedMessage {
		t.Fatalf("detail = %q, want concise sandbox prefix without raw output from %q", detail, deniedPath)
	}
}

func TestSandboxPermissionDetailIgnoresSuccessfulPermissionLikeOutput(t *testing.T) {
	t.Parallel()

	quoted := "quoted DecodePermissionRequest and go/pkg/mod cache: permission denied; EPERM EACCES\n"
	detail, ok := SandboxPermissionDetail(CommandResult{
		Stdout:  quoted,
		Route:   RouteSandbox,
		Backend: BackendSeatbelt,
	}, nil)
	if ok {
		t.Fatalf("SandboxPermissionDetail() = %q, true; successful textual output is not a sandbox denial", detail)
	}
}

func TestSandboxPermissionDetailIgnoresNonPermissionResultError(t *testing.T) {
	t.Parallel()

	detail, ok := SandboxPermissionDetail(CommandResult{
		Error:    "command timed out",
		ExitCode: 1,
		Route:    RouteSandbox,
		Backend:  BackendBwrap,
	}, fmt.Errorf("command timed out"))
	if ok {
		t.Fatalf("SandboxPermissionDetail() = %q, true; want no permission classification", detail)
	}
}

func TestIsSandboxPermissionDeniedTextDetectsDotNetDeniedPath(t *testing.T) {
	t.Parallel()

	text := `Access to the path 'C:\Users\me\.nuget\packages\private\1.0.0' is denied.`
	if !IsSandboxPermissionDeniedText(text) {
		t.Fatalf("IsSandboxPermissionDeniedText(%q) = false, want true", text)
	}
}

func TestIsSandboxPermissionDeniedTextDoesNotTreatGenericDeniedAsFilesystem(t *testing.T) {
	t.Parallel()

	text := "remote cache policy is denied."
	if IsSandboxPermissionDeniedText(text) {
		t.Fatalf("IsSandboxPermissionDeniedText(%q) = true, want false", text)
	}
}

func TestIsSandboxPermissionDeniedTextMatchesErrnoTokensNotSubstrings(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"EPERM",
		"open /tmp/x: EPERM",
		"Error: EACCES: mkdir failed",
		"errno=EACCES",
		"(EPERM)",
	} {
		if !IsSandboxPermissionDeniedText(text) {
			t.Fatalf("IsSandboxPermissionDeniedText(%q) = false, want true", text)
		}
	}

	for _, text := range []string{
		"DecodePermissionRequest",
		"control/acppermission.DecodePermissionRequest converts the wire request",
		"theaccess token was refreshed",
	} {
		if IsSandboxPermissionDeniedText(text) {
			t.Fatalf("IsSandboxPermissionDeniedText(%q) = true, want false", text)
		}
	}
}

func TestSharedClassifiersDoNotTreatQuotedSuccessTextAsCacheDenial(t *testing.T) {
	t.Parallel()

	quoted := "quoted DecodePermissionRequest uses gocache and go/pkg/mod"
	if IsSandboxPermissionDeniedText(quoted) {
		t.Fatalf("IsSandboxPermissionDeniedText(%q) = true, want false so cache-denial hints stay off", quoted)
	}
	if !IsSandboxCachePathEvidenceText(quoted) {
		t.Fatalf("IsSandboxCachePathEvidenceText(%q) = false, want cache path evidence without a denial", quoted)
	}
}

func TestIsSandboxCachePathEvidenceText(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"go: writing stat cache: open /home/test/go/pkg/mod/cache/download/private/@v/v0.tmp",
		`Access to the path 'C:\Users\me\.nuget\packages\private\1.0.0' is denied.`,
		`open C:\Users\me\AppData\Local\pnpm-store\v3: access is denied`,
		`Error: EACCES: permission denied, mkdir 'C:\state\.sandbox\env\current\cache\npm\_cacache'`,
		`open C:\state\.sandbox\env\current\cache\pip\http-v2: access is denied`,
		`Access to the path 'C:\state\.sandbox\env\current\cache\nuget\packages\private\1.0.0' is denied.`,
		`open /tmp/caelis/cache/yarn/v6: read-only file system`,
	} {
		if !IsSandboxCachePathEvidenceText(text) {
			t.Fatalf("IsSandboxCachePathEvidenceText(%q) = false, want true", text)
		}
	}

	text := "remote cache policy is denied."
	if IsSandboxCachePathEvidenceText(text) {
		t.Fatalf("IsSandboxCachePathEvidenceText(%q) = true, want false", text)
	}
}
