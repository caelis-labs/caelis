//go:build darwin

package seatbelt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
)

// TestExplicitReadCeilingNative executes only synthetic files under a temporary
// directory. Opt in because a containing sandbox can prohibit sandbox_apply.
func TestExplicitReadCeilingNative(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native Seatbelt evidence")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "allowed")
	if err = os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "denied.txt")
	if err = os.WriteFile(outside, []byte("synthetic-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "input.txt"), []byte("synthetic-input"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := sandbox.Config{CWD: work, ResourceLimits: &sandbox.ResourceLimits{ReadPaths: []string{work, "/bin", "/usr/bin", "/usr/lib", "/System/Library", "/System/Cryptexes", "/System/Volumes/Preboot/Cryptexes", "/dev/null"}, WritePaths: []string{work, "/dev/null"}, Network: sandbox.NetworkDisabled}}
	profile, err := buildSeatbeltProfile(policy.Default(cfg, sandbox.Constraints{Permission: sandbox.PermissionFullAccess, Network: sandbox.NetworkEnabled}), work)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	script := `set -eu
/bin/cat input.txt > output.txt
if /bin/cat "$DENIED" > leak.txt 2>/dev/null; then exit 42; fi
if /bin/ls /Library/Preferences > directory.txt 2>/dev/null; then exit 43; fi
if /bin/echo escape > "$DENIED" 2>/dev/null; then exit 44; fi
printf 'ceiling-ok'
`
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/bin/sh", "-c", script)
	cmd.Dir = work
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + work, "TMPDIR=" + work, "DENIED=" + outside}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("strict native execution failed: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "ceiling-ok") {
		t.Fatalf("shell never proved completion: %s", output)
	}
	got, err := os.ReadFile(filepath.Join(work, "output.txt"))
	if err != nil || string(got) != "synthetic-input" {
		t.Fatalf("allowed bytes=%q err=%v", got, err)
	}
	got, err = os.ReadFile(outside)
	if err != nil || string(got) != "synthetic-secret" {
		t.Fatalf("outside modified=%q err=%v", got, err)
	}
}

// TestExplicitProcessIsolationNative reads only a test-owned process whose
// entire environment is a synthetic sentinel. Both probes must recover it
// outside Seatbelt before their sandboxed denial can establish isolation.
func TestExplicitProcessIsolationNative(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native Seatbelt evidence")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	const helper = "-test.run=^TestExplicitProcessIsolationHelper$"
	sentinel := "CAELIS_SYNTHETIC_CREDENTIAL=" + rand.Text()
	digest := sha256.Sum256([]byte(sentinel))
	target := exec.CommandContext(ctx, executable, helper, "--", "target")
	target.Dir = work
	target.Env = []string{sentinel}
	stdin, err := target.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := target.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	if err = target.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = target.Process.Kill()
		_ = target.Wait()
	})
	if ready, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || ready != "target-ready\n" {
		t.Fatalf("synthetic target readiness failed: %v", err)
	}
	pid := strconv.Itoa(target.Process.Pid)
	cfg := sandbox.Config{CWD: work, ResourceLimits: &sandbox.ResourceLimits{
		ReadPaths:  []string{work, executable, "/bin", "/usr/bin", "/usr/lib", "/System/Library", "/System/Cryptexes", "/System/Volumes/Preboot/Cryptexes", "/dev/null"},
		WritePaths: []string{work, "/dev/null"},
		Network:    sandbox.NetworkDisabled,
	}}
	profile, err := buildSeatbeltProfile(policy.Default(cfg, sandbox.Constraints{Permission: sandbox.PermissionFullAccess, Network: sandbox.NetworkEnabled}), work)
	if err != nil {
		t.Fatal(err)
	}
	runProbe := func(t *testing.T, mode string, confined bool) processIsolationProbe {
		t.Helper()
		args := []string{executable, helper, "--", mode, pid, hex.EncodeToString(digest[:])}
		if confined {
			args = append([]string{"/usr/bin/sandbox-exec", "-p", profile}, args...)
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = work
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + work, "TMPDIR=" + work}
		output, err := cmd.Output()
		if err != nil {
			// The helper never prints target bytes; stderr can explain bootstrap
			// failures such as an outer sandbox denying sandbox_apply.
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				t.Fatalf("%s probe bootstrap failed (confined=%v): %v: %s%s", mode, confined, err, output, exit.Stderr)
			}
			t.Fatalf("%s probe start failed (confined=%v): %v", mode, confined, err)
		}
		var result processIsolationProbe
		if err := json.Unmarshal(output, &result); err != nil || !result.Reached {
			t.Fatalf("%s probe did not return its result (confined=%v): %v", mode, confined, err)
		}
		return result
	}
	for _, mode := range []string{"ps", "sysctl"} {
		t.Run(mode, func(t *testing.T) {
			baseline := runProbe(t, mode, false)
			if !baseline.Found || baseline.Errno != 0 || baseline.ExitCode != 0 {
				t.Fatalf("unconfined positive control did not recover synthetic sentinel: %+v", baseline)
			}
			confined := runProbe(t, mode, true)
			if confined.Found {
				t.Fatal("explicit read ceiling exposed another process's synthetic credential")
			}
			if mode == "sysctl" && confined.Errno != syscall.EPERM && confined.Errno != syscall.EACCES {
				t.Fatalf("direct KERN_PROCARGS2 was not denied by policy: %+v", confined)
			}
			t.Logf("synthetic sentinel recovered outside Seatbelt; confined found=%v errno=%d exit=%d", confined.Found, confined.Errno, confined.ExitCode)
		})
	}
}

type processIsolationProbe struct {
	Reached  bool
	Found    bool
	Errno    syscall.Errno
	ExitCode int
}

// TestExplicitProcessIsolationHelper is a reexec helper, never a process scan.
// Only the PID supplied by the owning test is inspected; results contain no
// target bytes and the expected sentinel is supplied only as a SHA-256 digest.
func TestExplicitProcessIsolationHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		t.Skip("reexec helper")
	}
	mode := os.Args[3]
	if mode == "target" && len(os.Args) == 4 {
		if env := os.Environ(); len(env) != 1 || !strings.HasPrefix(env[0], "CAELIS_SYNTHETIC_CREDENTIAL=") {
			t.Fatal("target environment is not exclusively synthetic")
		}
		fmt.Fprintln(os.Stdout, "target-ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if len(os.Args) != 6 || (mode != "ps" && mode != "sysctl") {
		t.Fatal("invalid helper arguments")
	}
	pid, err := strconv.ParseInt(os.Args[4], 10, 32)
	if err != nil || pid <= 0 {
		t.Fatal("invalid synthetic target PID")
	}
	digest, err := hex.DecodeString(os.Args[5])
	if err != nil || len(digest) != sha256.Size {
		t.Fatal("invalid synthetic sentinel digest")
	}
	result := processIsolationProbe{Reached: true}
	var fields [][]byte
	if mode == "ps" {
		cmd := exec.Command("/bin/ps", "eww", "-p", os.Args[4], "-o", "command=")
		cmd.Env = []string{}
		output, err := cmd.Output()
		if err != nil {
			var exit *exec.ExitError
			switch {
			case errors.Is(err, syscall.EPERM):
				// Darwin's setuid ps may be denied at exec itself.
				result.Errno = syscall.EPERM
			case errors.Is(err, syscall.EACCES):
				result.Errno = syscall.EACCES
			case errors.As(err, &exit):
				result.ExitCode = exit.ExitCode()
			default:
				t.Fatalf("could not launch ps: %v", err)
			}
		}
		fields = bytes.Fields(output)
	} else {
		// sys/sysctl.h: CTL_KERN=1, KERN_PROCARGS2=49. Use the numeric
		// syscall so denial of sysctl name lookup cannot mask access to the
		// target's arguments/environment. 64 KiB bounds our tiny fixture
		// below Darwin ARG_MAX without requiring another sysctl grant.
		mib := [3]int32{1, 49, int32(pid)}
		buf := make([]byte, 64<<10)
		n := uintptr(len(buf))
		_, _, result.Errno = syscall.Syscall6(syscall.SYS___SYSCTL,
			uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
		if result.Errno == 0 {
			if n > uintptr(len(buf)) {
				t.Fatal("KERN_PROCARGS2 exceeded probe buffer")
			}
			fields = bytes.Split(buf[:n], []byte{0})
		}
	}
	for _, field := range fields {
		got := sha256.Sum256(field)
		if bytes.Equal(got[:], digest) {
			result.Found = true
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
