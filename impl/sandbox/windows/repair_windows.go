//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/caelis-labs/caelis/impl/sandbox/windows/internal/pathutil"
	"github.com/caelis-labs/caelis/ports/sandbox"
	"golang.org/x/sys/windows"
)

const elevatedRepairRequestVersion = 1

type elevatedRepairRequest struct {
	Version int            `json:"version"`
	Config  sandbox.Config `json:"config"`
}

type elevatedRepairResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type elevatedRepairProcessLauncher func(context.Context, string, []string, string) (uint32, error)

var launchElevatedRepairProcess elevatedRepairProcessLauncher = launchElevatedRepairProcessDefault

func (r *runtime) runElevatedRepair(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.verifyRepairRootsWritable(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(r.sandboxStateDir(), 0o700); err != nil {
		return fmt.Errorf("impl/sandbox/windows: prepare repair state directory: %w", err)
	}
	repairID, err := newID("repair")
	if err != nil {
		return err
	}
	configFile := filepath.Join(r.sandboxStateDir(), repairID+".request.json")
	resultFile := filepath.Join(r.sandboxStateDir(), repairID+".result.json")
	defer func() {
		_ = os.Remove(configFile)
		_ = os.Remove(resultFile)
	}()

	cfg := sandbox.NormalizeConfig(r.cfg)
	cfg.RequestedBackend = sandbox.BackendWindows
	request := elevatedRepairRequest{
		Version: elevatedRepairRequestVersion,
		Config:  cfg,
	}
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		return fmt.Errorf("impl/sandbox/windows: write elevated repair request: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: resolve current executable: %w", err)
	}
	exitCode, launchErr := launchElevatedRepairProcess(ctx, exe, []string{
		internalRepairHelperCommand,
		"-config-file", configFile,
		"-result-file", resultFile,
	}, cfg.CWD)
	result, resultErr := readElevatedRepairResult(resultFile)
	if launchErr != nil {
		if resultErr == nil && strings.TrimSpace(result.Error) != "" {
			return fmt.Errorf("impl/sandbox/windows: elevated sandbox repair failed: %s", result.Error)
		}
		return fmt.Errorf("impl/sandbox/windows: launch elevated sandbox repair: %w", launchErr)
	}
	if resultErr != nil {
		return fmt.Errorf("impl/sandbox/windows: read elevated sandbox repair result: %w", resultErr)
	}
	if strings.TrimSpace(result.Error) != "" {
		return fmt.Errorf("impl/sandbox/windows: elevated sandbox repair failed: %s", result.Error)
	}
	if exitCode != 0 || !result.OK {
		return fmt.Errorf("impl/sandbox/windows: elevated sandbox repair exited with code %d", exitCode)
	}
	return nil
}

func (r *runtime) verifyRepairRootsWritable(ctx context.Context) error {
	policy, err := r.policyForRequest(sandbox.CommandRequest{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil {
		return err
	}
	for _, root := range policy.WriteRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verifyDirectoryFileWrite(root); err != nil {
			return fmt.Errorf("impl/sandbox/windows: elevated repair refuses root without current-user file write access %s: %w", root, err)
		}
	}
	return nil
}

func verifyDirectoryFileWrite(root string) error {
	root = pathutil.Normalize(root)
	if root == "" {
		return fmt.Errorf("path is required")
	}
	file, err := os.CreateTemp(root, ".caelis-repair-probe-*")
	if err != nil {
		return err
	}
	name := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	return errors.Join(closeErr, removeErr)
}

func runInternalRepairHelper(args []string) error {
	fs := flag.NewFlagSet(internalRepairHelperCommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var configFile string
	var resultFile string
	fs.StringVar(&configFile, "config-file", "", "repair request file")
	fs.StringVar(&resultFile, "result-file", "", "repair result file")
	parseErr := fs.Parse(args)
	opErr := parseErr
	if opErr == nil {
		opErr = runInternalRepairFromConfig(configFile)
	}
	resultErr := writeElevatedRepairResult(resultFile, opErr)
	if opErr != nil {
		return errors.Join(opErr, resultErr)
	}
	return resultErr
}

func runInternalRepairFromConfig(configFile string) error {
	configFile = strings.TrimSpace(configFile)
	if configFile == "" {
		return fmt.Errorf("missing --config-file")
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("read repair request: %w", err)
	}
	var request elevatedRepairRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return fmt.Errorf("decode repair request: %w", err)
	}
	if request.Version != elevatedRepairRequestVersion {
		return fmt.Errorf("unsupported repair request version %d", request.Version)
	}
	cfg := sandbox.NormalizeConfig(request.Config)
	if err := validateElevatedRepairConfig(cfg); err != nil {
		return err
	}
	rt, err := newRuntime(cfg)
	if err != nil {
		return err
	}
	defer rt.Close()
	windowsRuntime, ok := rt.(*runtime)
	if !ok {
		return fmt.Errorf("impl/sandbox/windows: unexpected repair runtime type %T", rt)
	}
	return windowsRuntime.repairCurrentWorkspaceACLs(context.Background())
}

func (r *runtime) repairCurrentWorkspaceACLs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	policy, err := r.policyForRequest(sandbox.CommandRequest{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()
	if err := os.MkdirAll(r.sandboxStateDir(), 0o700); err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	manifest, manifestErr := r.readManifest()
	if manifestErr == nil {
		r.cleanupStaleManifestACLs(manifest, policy)
	}
	if err := r.applyPolicyACLs(policy); err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	if err := r.writeManifest(policy); err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	r.clearWorkspaceSetupError()
	return nil
}

func readElevatedRepairResult(path string) (elevatedRepairResult, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return elevatedRepairResult{}, fmt.Errorf("result file path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return elevatedRepairResult{}, err
	}
	var result elevatedRepairResult
	if err := json.Unmarshal(data, &result); err != nil {
		return elevatedRepairResult{}, err
	}
	return result, nil
}

func writeElevatedRepairResult(path string, opErr error) error {
	path = strings.TrimSpace(path)
	if path == "" {
		if opErr != nil {
			return nil
		}
		return fmt.Errorf("missing --result-file")
	}
	result := elevatedRepairResult{OK: opErr == nil}
	if opErr != nil {
		result.Error = opErr.Error()
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func validateElevatedRepairConfig(cfg sandbox.Config) error {
	if sandbox.CanonicalBackend(cfg.RequestedBackend) != sandbox.BackendWindows {
		return fmt.Errorf("impl/sandbox/windows: elevated repair only supports the Windows sandbox backend")
	}
	workspaceRoot, err := pathutil.NormalizeWithBase("", cfg.CWD)
	if err != nil {
		return err
	}
	if workspaceRoot == "" {
		return fmt.Errorf("impl/sandbox/windows: workspace cwd is required for elevated repair")
	}
	if err := validateRepairDirectory("workspace", workspaceRoot); err != nil {
		return err
	}
	stateRoot, err := resolveStateRoot(cfg.StateDir)
	if err != nil {
		return err
	}
	if err := validateRepairDirectory("state", stateRoot); err != nil {
		return err
	}
	for _, root := range cfg.WritableRoots {
		normalized, err := pathutil.NormalizeWithBase(workspaceRoot, root)
		if err != nil {
			return err
		}
		if normalized == "" {
			continue
		}
		existing, err := existingWritableRoots([]string{normalized})
		if err != nil {
			return err
		}
		for _, path := range existing {
			if err := validateRepairDirectory("writable root", path); err != nil {
				return err
			}
		}
	}
	for _, subpath := range cfg.ReadOnlySubpaths {
		if !safeRelativeSubpath(subpath) {
			return fmt.Errorf("impl/sandbox/windows: elevated repair refuses unsafe read-only subpath: %s", subpath)
		}
	}
	return nil
}

func validateRepairDirectory(label string, path string) error {
	path = pathutil.Normalize(path)
	if path == "" {
		return fmt.Errorf("impl/sandbox/windows: %s path is required", label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: inspect %s path %s: %w", label, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("impl/sandbox/windows: %s path %s is not a directory", label, path)
	}
	if isUNCPath(path) || isVolumeRoot(path) || isKnownSystemPath(path) {
		return fmt.Errorf("impl/sandbox/windows: elevated repair refuses unsafe %s path: %s", label, path)
	}
	reparse, err := isReparsePoint(path)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: inspect %s reparse point %s: %w", label, path, err)
	}
	if reparse {
		return fmt.Errorf("impl/sandbox/windows: elevated repair refuses reparse-point %s path: %s", label, path)
	}
	return nil
}

func safeRelativeSubpath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return true
	}
	if filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func isUNCPath(path string) bool {
	return strings.HasPrefix(filepath.Clean(path), `\\`)
}

func isVolumeRoot(path string) bool {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return clean == string(filepath.Separator)
	}
	root := filepath.Clean(volume + string(filepath.Separator))
	return strings.EqualFold(clean, root)
}

func isKnownSystemPath(path string) bool {
	for _, root := range knownSystemRoots() {
		if root != "" && pathutil.IsUnder(path, root) {
			return true
		}
	}
	return false
}

func knownSystemRoots() []string {
	var roots []string
	for _, key := range []string{"WINDIR", "SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			roots = append(roots, pathutil.Normalize(value))
		}
	}
	return pathutil.Dedupe(roots)
}

func isReparsePoint(path string) (bool, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return false, err
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoConsole      = 0x00008000
	swHide                = 0
	waitTimeout           = 258
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

func launchElevatedRepairProcessDefault(ctx context.Context, exe string, args []string, cwd string) (uint32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(exe) == "" {
		return 0, fmt.Errorf("executable path is required")
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(composeWindowsParameters(args))
	if err != nil {
		return 0, err
	}
	var dir *uint16
	if strings.TrimSpace(cwd) != "" {
		dir, err = windows.UTF16PtrFromString(cwd)
		if err != nil {
			return 0, err
		}
	}
	info := shellExecuteInfo{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		fMask:        seeMaskNoCloseProcess | seeMaskNoConsole,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		lpDirectory:  dir,
		nShow:        swHide,
	}
	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if !errors.Is(callErr, syscall.Errno(0)) {
			if errors.Is(callErr, windows.ERROR_CANCELLED) {
				return 0, fmt.Errorf("UAC prompt was cancelled")
			}
			return 0, callErr
		}
		return 0, syscall.EINVAL
	}
	if info.hProcess == 0 {
		return 0, nil
	}
	defer func() {
		_ = windows.CloseHandle(info.hProcess)
	}()
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		event, err := windows.WaitForSingleObject(info.hProcess, uint32((200*time.Millisecond)/time.Millisecond))
		if err != nil {
			return 0, err
		}
		switch event {
		case windows.WAIT_OBJECT_0:
			var exitCode uint32
			if err := windows.GetExitCodeProcess(info.hProcess, &exitCode); err != nil {
				return 0, err
			}
			return exitCode, nil
		case waitTimeout:
			continue
		default:
			return 0, fmt.Errorf("wait for elevated repair process returned %d", event)
		}
	}
}

func composeWindowsParameters(args []string) string {
	if len(args) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(args))
	for _, arg := range args {
		escaped = append(escaped, windows.EscapeArg(arg))
	}
	return strings.Join(escaped, " ")
}
