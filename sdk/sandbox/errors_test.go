package sandbox

import (
	"fmt"
	"strings"
	"testing"
)

func TestSandboxPermissionDetailIncludesStdoutRedirectedDiagnostics(t *testing.T) {
	t.Parallel()

	const deniedPath = "/home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp"
	detail, ok := SandboxPermissionDetail(CommandResult{
		Stdout:  "go: writing stat cache: open " + deniedPath + ": read-only file system\n",
		Route:   RouteSandbox,
		Backend: BackendBwrap,
	}, fmt.Errorf("exit status 1"))
	if !ok {
		t.Fatal("SandboxPermissionDetail() ok = false, want true")
	}
	if !strings.Contains(detail, SandboxPermissionDeniedMessage) ||
		!strings.Contains(detail, deniedPath) {
		t.Fatalf("detail = %q, want sandbox prefix plus stdout denied path", detail)
	}
}
