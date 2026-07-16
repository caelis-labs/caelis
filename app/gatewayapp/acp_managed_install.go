package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/procutil"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

const (
	managedACPInstallOutputLimit = 64 * 1024
	managedACPProgressInterval   = 2 * time.Second
	managedACPStagingMaxAge      = 24 * time.Hour
)

// ACPAgentInstallError preserves the bounded npm diagnostic tail while keeping
// cancellation and process failures available through errors.Is/errors.As.
type ACPAgentInstallError struct {
	Agent   string
	Command []string
	Output  string
	Err     error
}

type builtinACPAgentNPMInstallRequest struct {
	Root        string
	CacheRoot   string
	AdapterID   string
	InstallSpec string
	Package     builtinACPAdapterPackage
}

type builtinACPAgentNPMInstallResult struct {
	Command []string
	Output  string
}

var runBuiltinACPAgentNPMInstall = defaultRunBuiltinACPAgentNPMInstall

// Managed installs are serialized per adapter version. Different adapters do
// not share writable installation roots and can make progress independently.
var managedACPInstallLocks sync.Map

func (e *ACPAgentInstallError) Error() string {
	if e == nil {
		return ""
	}
	agent := strings.TrimSpace(e.Agent)
	if agent == "" {
		agent = "unknown"
	}
	errText := "failed"
	if e.Err != nil {
		errText = e.Err.Error()
	}
	msg := fmt.Sprintf("gatewayapp: install ACP agent %q: %s", agent, errText)
	if out := strings.TrimSpace(e.Output); out != "" {
		msg += "\n" + out
	}
	return msg
}

func (e *ACPAgentInstallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ACPAgentInstallError) CommandString() string {
	if e == nil {
		return ""
	}
	return strings.Join(e.Command, " ")
}

func (s *Stack) installBuiltinACPAgent(ctx context.Context, name string, base assembly.AgentConfig) (assembly.AgentConfig, error) {
	pkg, ok := builtinACPAdapterPackageFor(name)
	if !ok {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: ACP agent %q does not support local npm install", strings.TrimSpace(name))
	}
	root, err := installManagedACPAdapter(ctx, s.managedACPAgentRoot(), strings.TrimSpace(name), pkg)
	if err != nil {
		return assembly.AgentConfig{}, err
	}
	base.Command = managedACPAgentBinPath(root, pkg.Bin)
	base.Args = nil
	return base, nil
}

func installManagedACPAdapter(ctx context.Context, baseRoot string, adapterID string, pkg builtinACPAdapterPackage) (root string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	baseRoot = strings.TrimSpace(baseRoot)
	adapterID = strings.ToLower(strings.TrimSpace(adapterID))
	if baseRoot == "" || adapterID == "" {
		return "", fmt.Errorf("gatewayapp: managed ACP install requires a root and adapter id")
	}

	target := managedACPAdapterRoot(baseRoot, pkg)
	controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
		AdapterID: adapterID,
		Phase:     controlagents.SetupPhaseChecking,
		Detail:    "Checking the isolated managed installation",
	})
	if validateManagedACPAdapterRoot(target, pkg) == nil {
		controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
			AdapterID: adapterID,
			Phase:     controlagents.SetupPhaseReady,
			Detail:    "Using the verified managed installation",
		})
		return target, nil
	}

	lockKey := target
	newGate := make(chan struct{}, 1)
	newGate <- struct{}{}
	lockValue, _ := managedACPInstallLocks.LoadOrStore(lockKey, newGate)
	gate := lockValue.(chan struct{})
	select {
	case <-gate:
	case <-ctx.Done():
		return "", ctx.Err()
	default:
		controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
			AdapterID: adapterID,
			Phase:     controlagents.SetupPhaseWaiting,
			Detail:    "Waiting for another setup of this adapter to finish",
		})
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	defer func() { gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	lockRoot := filepath.Join(baseRoot, ".locks")
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		return "", err
	}
	fileLock, err := acquireManagedACPFileLock(ctx, filepath.Join(lockRoot, managedACPInstallLockName(pkg)), func() {
		controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
			AdapterID: adapterID,
			Phase:     controlagents.SetupPhaseWaiting,
			Detail:    "Waiting for another Caelis process to finish this adapter",
		})
	})
	if err != nil {
		return "", fmt.Errorf("gatewayapp: lock managed ACP install: %w", err)
	}
	defer func() { _ = fileLock.Close() }()
	if validateManagedACPAdapterRoot(target, pkg) == nil {
		controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
			AdapterID: adapterID,
			Phase:     controlagents.SetupPhaseReady,
			Detail:    "Using the installation completed by another setup",
		})
		return target, nil
	}

	stageParent := managedACPAdapterStagingRoot(baseRoot, pkg)
	if err := os.MkdirAll(stageParent, 0o700); err != nil {
		return "", err
	}
	cleanupStaleManagedACPStages(stageParent, time.Now(), managedACPStagingMaxAge)
	stage, err := os.MkdirTemp(stageParent, "install-")
	if err != nil {
		return "", err
	}
	defer func() {
		if stage == "" {
			return
		}
		if cleanupErr := removeManagedACPInstallTree(stage); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("gatewayapp: clean incomplete ACP install %s: %w", stage, cleanupErr))
		}
	}()

	controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
		AdapterID: adapterID,
		Phase:     controlagents.SetupPhaseInstalling,
		Detail:    "Downloading a large runtime; the first setup may take several minutes",
	})
	result, installErr := runBuiltinACPAgentNPMInstall(ctx, builtinACPAgentNPMInstallRequest{
		Root:        stage,
		CacheRoot:   managedACPAdapterCacheRoot(baseRoot, pkg),
		AdapterID:   adapterID,
		InstallSpec: builtinACPAdapterInstallSpec(pkg),
		Package:     pkg,
	})
	if installErr != nil {
		return "", &ACPAgentInstallError{
			Agent: adapterID, Command: result.Command, Output: result.Output, Err: installErr,
		}
	}

	controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
		AdapterID: adapterID,
		Phase:     controlagents.SetupPhaseVerifying,
		Detail:    "Verifying the adapter and its platform runtime",
	})
	if validateErr := validateManagedACPAdapterRoot(stage, pkg); validateErr != nil {
		return "", &ACPAgentInstallError{
			Agent: adapterID, Command: result.Command, Output: result.Output,
			Err: fmt.Errorf("installed package is incomplete: %w", validateErr),
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}

	// A failed prior version of this exact target is never edited in place. It
	// is moved into the isolated staging area before the verified tree is
	// atomically published.
	if _, statErr := os.Stat(target); statErr == nil {
		if validateManagedACPAdapterRoot(target, pkg) == nil {
			return target, nil
		}
		quarantine := filepath.Join(stageParent, "replaced-"+filepath.Base(stage))
		if err := os.Rename(target, quarantine); err != nil {
			if validateManagedACPAdapterRoot(target, pkg) == nil {
				return target, nil
			}
			return "", fmt.Errorf("gatewayapp: quarantine incomplete ACP install: %w", err)
		}
		defer func() { _ = removeManagedACPInstallTree(quarantine) }()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if err := os.Rename(stage, target); err != nil {
		// Another Caelis process may have won the same immutable publish race.
		// Accept only a fully verified winner and discard our private stage.
		if validateManagedACPAdapterRoot(target, pkg) == nil {
			return target, nil
		}
		return "", fmt.Errorf("gatewayapp: publish managed ACP install: %w", err)
	}
	stage = ""
	controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
		AdapterID: adapterID,
		Phase:     controlagents.SetupPhaseReady,
		Detail:    "Managed installation is verified and ready",
	})
	return target, nil
}

func defaultRunBuiltinACPAgentNPMInstall(ctx context.Context, req builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
	root := strings.TrimSpace(req.Root)
	installSpec := strings.TrimSpace(req.InstallSpec)
	args := []string{"install", "--prefix", root, "--no-audit", "--no-fund", "--progress=false", installSpec}
	result := builtinACPAgentNPMInstallResult{Command: append([]string{"npm"}, args...)}
	npm, err := exec.LookPath("npm")
	if err != nil || strings.TrimSpace(npm) == "" {
		return result, fmt.Errorf("npm is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return result, err
	}
	cacheRoot := strings.TrimSpace(req.CacheRoot)
	if cacheRoot == "" {
		cacheRoot = filepath.Join(filepath.Dir(root), "npm-cache")
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return result, err
	}
	args[len(args)-1] = npmInstallSpecForExec(npm, installSpec)
	cmd := exec.Command(npm, args...)
	cmd.Dir = root
	cmd.Env = withCommandEnv(os.Environ(), map[string]string{
		"CI":                         "1",
		"NO_COLOR":                   "1",
		"npm_config_cache":           cacheRoot,
		"npm_config_update_notifier": "false",
	})
	procutil.ApplyNonInteractiveCommandDefaults(cmd)
	output := &cappedACPInstallOutput{limit: managedACPInstallOutputLimit}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := cmd.Start(); err != nil {
		return result, err
	}

	started := time.Now()
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(managedACPProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
					AdapterID: req.AdapterID,
					Phase:     controlagents.SetupPhaseDownloading,
					Detail:    "npm is downloading and unpacking runtime dependencies",
					Bytes:     managedACPInstallTreeSize(root),
					Elapsed:   time.Since(started),
				})
			}
		}
	}()
	err = procutil.WaitWithIdleTimeout(ctx, cmd, 0, nil)
	close(stopProgress)
	<-progressDone
	result.Output = strings.TrimSpace(output.String())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return result, err
	}
	return result, nil
}

func (s *Stack) managedACPAgentRoot() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.storeDir, "acp-agents", "npm")
}

func managedACPAdapterRoot(baseRoot string, pkg builtinACPAdapterPackage) string {
	return filepath.Join(
		strings.TrimSpace(baseRoot), "installations",
		managedACPPathComponent(pkg.Bin), managedACPPathComponent(firstNonEmpty(pkg.Version, "latest")),
	)
}

func managedACPAdapterStagingRoot(baseRoot string, pkg builtinACPAdapterPackage) string {
	return filepath.Join(
		strings.TrimSpace(baseRoot), ".staging",
		managedACPPathComponent(pkg.Bin), managedACPPathComponent(firstNonEmpty(pkg.Version, "latest")),
	)
}

func managedACPAdapterCacheRoot(baseRoot string, pkg builtinACPAdapterPackage) string {
	return filepath.Join(strings.TrimSpace(baseRoot), "cache", managedACPPathComponent(pkg.Bin))
}

func managedACPInstallLockName(pkg builtinACPAdapterPackage) string {
	return managedACPPathComponent(pkg.Bin) + "-" + managedACPPathComponent(firstNonEmpty(pkg.Version, "latest")) + ".lock"
}

func managedACPPathComponent(value string) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	component := strings.Trim(out.String(), ".")
	if component == "" {
		return "unknown"
	}
	return component
}

func managedACPAgentBinPath(root string, bin string) string {
	bin = strings.TrimSpace(bin)
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(bin), ".cmd") {
		bin += ".cmd"
	}
	return filepath.Join(strings.TrimSpace(root), "node_modules", ".bin", bin)
}

func validateManagedACPAdapterRoot(root string, pkg builtinACPAdapterPackage) error {
	root = strings.TrimSpace(root)
	if !managedACPAdapterPackageMatches(root, pkg) {
		return fmt.Errorf("missing curated package %s", builtinACPAdapterInstallSpec(pkg))
	}
	bin := managedACPAgentBinPath(root, pkg.Bin)
	info, err := os.Stat(bin)
	if err != nil {
		return fmt.Errorf("missing adapter executable %s: %w", bin, err)
	}
	if info.IsDir() {
		return fmt.Errorf("adapter executable %s is a directory", bin)
	}
	return validateManagedACPPlatformRuntime(root, pkg)
}

func validateManagedACPPlatformRuntime(root string, pkg builtinACPAdapterPackage) error {
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	switch strings.TrimSpace(pkg.Package) {
	case "@agentclientprotocol/codex-acp":
		triple := ""
		switch platform + "/" + arch {
		case "darwin/x64":
			triple = "x86_64-apple-darwin"
		case "darwin/arm64":
			triple = "aarch64-apple-darwin"
		case "linux/x64":
			triple = "x86_64-unknown-linux-musl"
		case "linux/arm64":
			triple = "aarch64-unknown-linux-musl"
		case "win32/x64":
			triple = "x86_64-pc-windows-msvc"
		case "win32/arm64":
			triple = "aarch64-pc-windows-msvc"
		default:
			return fmt.Errorf("unsupported Codex platform %s/%s", platform, arch)
		}
		binary := "codex"
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		return requireManagedACPFile(filepath.Join(
			root, "node_modules", "@openai", "codex-"+platform+"-"+arch,
			"vendor", triple, "bin", binary,
		))
	case "@agentclientprotocol/claude-agent-acp":
		candidates := []string{"@anthropic-ai/claude-agent-sdk-" + platform + "-" + arch}
		if platform == "linux" {
			candidates = append(candidates, candidates[0]+"-musl")
		}
		binary := "claude"
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		var candidateErrors []error
		for _, candidate := range candidates {
			path := filepath.Join(root, "node_modules", filepath.FromSlash(candidate), binary)
			if err := requireManagedACPFile(path); err == nil {
				return nil
			} else {
				candidateErrors = append(candidateErrors, err)
			}
		}
		return fmt.Errorf("missing Claude platform runtime: %w", errors.Join(candidateErrors...))
	default:
		return nil
	}
}

func requireManagedACPFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("missing platform runtime %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return fmt.Errorf("platform runtime %s is not a non-empty file", path)
	}
	return nil
}

func cleanupStaleManagedACPStages(root string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || maxAge > 0 && now.Sub(info.ModTime()) < maxAge {
			continue
		}
		_ = removeManagedACPInstallTree(filepath.Join(root, entry.Name()))
	}
}

func removeManagedACPInstallTree(path string) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = os.RemoveAll(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

func managedACPInstallTreeSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// cleanupLegacyManagedACPInstallIfUnused removes the former shared mutable npm
// tree only after no persisted Connection can launch from it. New immutable
// installations, caches, and staging directories live beside these legacy
// entries and are not touched.
func (s *Stack) cleanupLegacyManagedACPInstallIfUnused() {
	if s == nil || s.store == nil {
		return
	}
	doc, err := s.store.Load()
	if err != nil {
		return
	}
	_ = cleanupLegacyManagedACPInstall(s.managedACPAgentRoot(), doc.AgentRoster)
}

func cleanupLegacyManagedACPInstall(baseRoot string, roster controlagents.Configuration) error {
	baseRoot = strings.TrimSpace(baseRoot)
	if baseRoot == "" {
		return fmt.Errorf("gatewayapp: legacy managed ACP cleanup root is empty")
	}
	legacyModules := filepath.Join(baseRoot, "node_modules")
	for _, connection := range controlagents.NormalizeConfiguration(roster).Connections {
		if pathWithinRoot(connection.Launcher.Command, legacyModules) {
			return nil
		}
	}
	if info, err := os.Stat(legacyModules); err == nil && time.Since(info.ModTime()) < managedACPStagingMaxAge {
		// A second process running an older Caelis build may still be writing the
		// former shared tree. Leave recent legacy content alone; a later setup can
		// reclaim it after it is unreferenced and stale.
		return nil
	}
	var cleanupErr error
	for _, name := range []string{"node_modules", "npm-cache", "package.json", "package-lock.json"} {
		path := filepath.Join(baseRoot, name)
		if err := removeManagedACPInstallTree(path); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove legacy managed ACP path %s: %w", path, err))
		}
	}
	return cleanupErr
}

func pathWithinRoot(path string, root string) bool {
	path = strings.TrimSpace(path)
	root = strings.TrimSpace(root)
	if path == "" || root == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absRoot, absPath)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func withCommandEnv(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	keys := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		canonical := strings.ToLower(key)
		if _, exists := values[canonical]; !exists {
			keys = append(keys, key)
		}
		values[canonical] = item
	}
	for key, value := range overrides {
		canonical := strings.ToLower(strings.TrimSpace(key))
		if canonical == "" {
			continue
		}
		if _, exists := values[canonical]; !exists {
			keys = append(keys, key)
		}
		values[canonical] = key + "=" + value
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, key := range keys {
		canonical := strings.ToLower(key)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, values[canonical])
	}
	return out
}

type cappedACPInstallOutput struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (w *cappedACPInstallOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.limit <= 0 {
		return len(p), nil
	}
	w.data = append(w.data, p...)
	if len(w.data) > w.limit {
		w.data = append([]byte(nil), w.data[len(w.data)-w.limit:]...)
	}
	return len(p), nil
}

func (w *cappedACPInstallOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(append([]byte(nil), w.data...))
}

var _ io.Writer = (*cappedACPInstallOutput)(nil)
