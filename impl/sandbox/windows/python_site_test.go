package windows

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSandboxPythonSiteCustomizePython37Syntax(t *testing.T) {
	python := requirePythonForSiteCustomizeTest(t)
	cmd := python.command("-c", `
import ast
import sys

source = sys.stdin.read()
try:
    ast.parse(source, filename="sitecustomize.py", feature_version=(3, 7))
except TypeError:
    ast.parse(source, filename="sitecustomize.py")
`)
	cmd.Stdin = strings.NewReader(sandboxPythonSiteCustomize)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sitecustomize Python 3.7 syntax check failed: %v\n%s", err, out)
	}
}

func TestSandboxPythonSiteCustomizeSkipsPatchBeforePython313(t *testing.T) {
	python := requirePythonForSiteCustomizeTest(t)
	cmd := python.command("-c", `
import os
import sys
import tempfile

source = sys.stdin.read()
root = sys.argv[1]
original_mkdtemp = tempfile.mkdtemp
old_name = os.name
old_version_info = sys.version_info
had_audit = hasattr(sys, "audit")
old_audit = getattr(sys, "audit", None)
try:
    os.environ["CAELIS_SANDBOX_TEMP"] = root
    os.name = "nt"
    sys.version_info = (3, 7, 17)
    if had_audit:
        del sys.audit
    exec(compile(source, "sitecustomize.py", "exec"), {})
    if tempfile.mkdtemp is not original_mkdtemp:
        raise SystemExit("mkdtemp was patched before Python 3.13")
finally:
    tempfile.mkdtemp = original_mkdtemp
    os.name = old_name
    sys.version_info = old_version_info
    if had_audit:
        sys.audit = old_audit
`, t.TempDir())
	cmd.Stdin = strings.NewReader(sandboxPythonSiteCustomize)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sitecustomize Python 3.7 compatibility check failed: %v\n%s", err, out)
	}
}

func TestSandboxPythonSiteCustomizeMkdtempToleratesMissingAudit(t *testing.T) {
	python := requirePythonForSiteCustomizeTest(t)
	cmd := python.command("-c", `
import os
import sys
import tempfile

source = sys.stdin.read()
root = sys.argv[1]
original_mkdtemp = tempfile.mkdtemp
old_name = os.name
old_version_info = sys.version_info
had_audit = hasattr(sys, "audit")
old_audit = getattr(sys, "audit", None)
try:
    os.environ["CAELIS_SANDBOX_TEMP"] = root
    os.name = "nt"
    sys.version_info = (3, 13, 0)
    if had_audit:
        del sys.audit
    exec(compile(source, "sitecustomize.py", "exec"), {})
    if tempfile.mkdtemp is original_mkdtemp:
        raise SystemExit("mkdtemp was not patched for Python 3.13")
    tmp = tempfile.mkdtemp(prefix="pip-unpack-", dir=root)
    path = os.path.join(tmp, "ok.txt")
    with open(path, "w", encoding="utf-8") as f:
        f.write("ok")
    with open(path, "r", encoding="utf-8") as f:
        value = f.read()
    if value != "ok":
        raise SystemExit("tempfile write/read failed")
finally:
    tempfile.mkdtemp = original_mkdtemp
    os.name = old_name
    sys.version_info = old_version_info
    if had_audit:
        sys.audit = old_audit
`, t.TempDir())
	cmd.Stdin = strings.NewReader(sandboxPythonSiteCustomize)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sitecustomize missing-audit mkdtemp check failed: %v\n%s", err, out)
	}
}

type pythonTestCommand struct {
	name string
	args []string
}

func (c pythonTestCommand) command(args ...string) *exec.Cmd {
	all := append([]string(nil), c.args...)
	all = append(all, args...)
	return exec.Command(c.name, all...)
}

func (c pythonTestCommand) shellPrefix() string {
	parts := append([]string{c.name}, c.args...)
	return strings.Join(parts, " ")
}

func requirePythonForSiteCustomizeTest(t *testing.T) pythonTestCommand {
	t.Helper()
	if candidate, ok := availablePythonForSiteCustomize(); ok {
		return candidate
	}
	t.Skip("python is not available")
	return pythonTestCommand{}
}

func availablePythonForSiteCustomize() (pythonTestCommand, bool) {
	for _, candidate := range []pythonTestCommand{
		{name: "python3"},
		{name: "python"},
		{name: "py", args: []string{"-3"}},
		{name: "py"},
	} {
		if usablePythonForSiteCustomize(candidate) {
			return candidate, true
		}
	}
	return pythonTestCommand{}, false
}

func usablePythonForSiteCustomize(candidate pythonTestCommand) bool {
	if strings.TrimSpace(candidate.name) == "" {
		return false
	}
	if _, err := exec.LookPath(candidate.name); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := append([]string(nil), candidate.args...)
	args = append(args, "-c", "import sys; raise SystemExit(0 if sys.version_info[0] == 3 else 1)")
	cmd := exec.CommandContext(ctx, candidate.name, args...)
	return cmd.Run() == nil
}
