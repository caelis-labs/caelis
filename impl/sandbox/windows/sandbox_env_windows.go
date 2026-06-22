//go:build windows

package windows

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
)

type sandboxCacheEnvMode int

const (
	sandboxCacheEnvForce sandboxCacheEnvMode = iota
	sandboxCacheEnvDefault
)

type sandboxCacheEnvSpec struct {
	Key        string
	PathParts  []string
	Mode       sandboxCacheEnvMode
	AliasGroup string
}

var sandboxCacheEnvSpecs = []sandboxCacheEnvSpec{
	{Key: "GOCACHE", PathParts: []string{"go-build"}, Mode: sandboxCacheEnvForce},
	{Key: "GOMODCACHE", PathParts: []string{"go-mod"}, Mode: sandboxCacheEnvForce},
	{Key: "PIP_CACHE_DIR", PathParts: []string{"pip"}, Mode: sandboxCacheEnvForce},
	{Key: "npm_config_cache", PathParts: []string{"npm"}, Mode: sandboxCacheEnvForce},
	{Key: "NUGET_PACKAGES", PathParts: []string{"nuget", "packages"}, Mode: sandboxCacheEnvDefault},
	{Key: "pnpm_config_store_dir", PathParts: []string{"pnpm-store"}, Mode: sandboxCacheEnvDefault, AliasGroup: "pnpm-store"},
	{Key: "npm_config_store_dir", PathParts: []string{"pnpm-store"}, Mode: sandboxCacheEnvDefault, AliasGroup: "pnpm-store"},
	{Key: "YARN_CACHE_FOLDER", PathParts: []string{"yarn"}, Mode: sandboxCacheEnvDefault},
}

func sandboxEnvDirs(envRoot string) []string {
	envRoot = pathutil.Normalize(envRoot)
	if envRoot == "" {
		return nil
	}
	cacheRoot := sandboxCacheRoot(envRoot)
	paths := []string{
		envRoot,
		sandboxTempRoot(envRoot),
		cacheRoot,
		filepath.Join(envRoot, "powershell"),
		sandboxPowerShellCacheDir(envRoot),
		sandboxPythonSiteDir(envRoot),
	}
	for _, spec := range sandboxCacheEnvSpecs {
		if path := sandboxCacheEnvPath(cacheRoot, spec); path != "" {
			paths = append(paths, path)
		}
	}
	return pathutil.Dedupe(paths)
}

func sandboxTempRoot(envRoot string) string {
	return sandboxSubdir(envRoot, "tmp")
}

func sandboxCacheRoot(envRoot string) string {
	return sandboxSubdir(envRoot, "cache")
}

func sandboxPowerShellCacheDir(envRoot string) string {
	return sandboxSubdir(envRoot, "powershell", "CommandAnalysis")
}

func sandboxPythonSiteDir(envRoot string) string {
	return sandboxSubdir(envRoot, "python-site")
}

func sandboxSubdir(envRoot string, parts ...string) string {
	envRoot = pathutil.Normalize(envRoot)
	if envRoot == "" {
		return ""
	}
	return filepath.Join(append([]string{envRoot}, parts...)...)
}

func sandboxEnvironment(policy workspacePolicy, extra map[string]string) ([]string, error) {
	envRoot := strings.TrimSpace(policy.SandboxEnvRoot)
	if envRoot == "" {
		return nil, fmt.Errorf("impl/sandbox/windows: sandbox environment root is required")
	}
	envRoot = pathutil.Normalize(envRoot)
	tempRoot := sandboxTempRoot(envRoot)
	cacheRoot := sandboxCacheRoot(envRoot)
	psCacheDir := sandboxPowerShellCacheDir(envRoot)
	pythonSiteDir := sandboxPythonSiteDir(envRoot)
	for _, dir := range sandboxEnvDirs(envRoot) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("impl/sandbox/windows: prepare sandbox environment directory %s: %w", dir, err)
		}
	}
	touchSandboxEnvRoot(envRoot)
	if err := writeSandboxPythonSiteCustomize(pythonSiteDir); err != nil {
		return nil, err
	}
	forced := map[string]string{
		"TEMP":                        tempRoot,
		"TMP":                         tempRoot,
		"GOTMPDIR":                    tempRoot,
		"CAELIS_SANDBOX_TEMP":         tempRoot,
		"GOTELEMETRY":                 "off",
		"PYTHONPATH":                  prependEnvPath(pythonSiteDir, commandEnvValue(extra, "PYTHONPATH")),
		"PSModuleAnalysisCachePath":   filepath.Join(psCacheDir, "PowerShell_AnalysisCache"),
		"POWERSHELL_TELEMETRY_OPTOUT": "1",
	}
	addSandboxCacheEnv(forced, extra, cacheRoot)
	if gitSSHCommand, ok := defaultGitOpenSSHCommand(extra); ok {
		forced["GIT_SSH_COMMAND"] = gitSSHCommand
	}
	if skillsDir := hostUserSkillsDir(); skillsDir != "" {
		forced["CAELIS_SKILLS_DIR"] = skillsDir
	}
	forced["SystemRoot"] = resolveSystemRoot()
	if strings.TrimSpace(os.Getenv("WINDIR")) == "" {
		forced["WINDIR"] = forced["SystemRoot"]
	}
	if strings.TrimSpace(os.Getenv("ComSpec")) == "" {
		forced["ComSpec"] = filepath.Join(forced["SystemRoot"], "System32", "cmd.exe")
	}
	if strings.TrimSpace(os.Getenv("PATHEXT")) == "" {
		forced["PATHEXT"] = `.COM;.EXE;.BAT;.CMD;.VBS;.VBE;.JS;.JSE;.WSF;.WSH;.MSC`
	}
	return mergeEnv(extra, forced), nil
}

func addSandboxCacheEnv(forced map[string]string, extra map[string]string, cacheRoot string) {
	presentGroups := map[string]bool{}
	for _, spec := range sandboxCacheEnvSpecs {
		if spec.Mode != sandboxCacheEnvDefault || spec.AliasGroup == "" {
			continue
		}
		if _, ok := lookupCommandEnv(extra, spec.Key); ok {
			presentGroups[spec.AliasGroup] = true
		}
	}
	for _, spec := range sandboxCacheEnvSpecs {
		if value, ok := resolveCacheEnv(spec, extra, cacheRoot, presentGroups); ok {
			forced[spec.Key] = value
		}
	}
}

func resolveCacheEnv(spec sandboxCacheEnvSpec, extra map[string]string, cacheRoot string, presentGroups map[string]bool) (string, bool) {
	value := sandboxCacheEnvPath(cacheRoot, spec)
	if strings.TrimSpace(value) == "" {
		return "", false
	}
	switch spec.Mode {
	case sandboxCacheEnvForce:
		if _, ok := lookupExtraCommandEnv(extra, spec.Key); ok {
			return "", false
		}
		return value, true
	case sandboxCacheEnvDefault:
		if spec.AliasGroup != "" && presentGroups[spec.AliasGroup] {
			return "", false
		}
		if spec.AliasGroup == "" {
			if _, ok := lookupCommandEnv(extra, spec.Key); ok {
				return "", false
			}
		}
		return value, true
	default:
		return "", false
	}
}

func sandboxCacheEnvPath(cacheRoot string, spec sandboxCacheEnvSpec) string {
	return sandboxSubdir(cacheRoot, spec.PathParts...)
}

func defaultGitOpenSSHCommand(extra map[string]string) (string, bool) {
	if _, ok := lookupCommandEnv(extra, "GIT_SSH_COMMAND"); ok {
		return "", false
	}
	if _, ok := lookupCommandEnv(extra, "GIT_SSH"); ok {
		return "", false
	}
	path := defaultWindowsOpenSSHPath()
	if path == "" {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return filepath.ToSlash(path), true
}

func defaultWindowsOpenSSHPath() string {
	return filepath.Join(resolveSystemRoot(), "System32", "OpenSSH", "ssh.exe")
}

func resolveSystemRoot() string {
	if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
		return systemRoot
	}
	if windir := strings.TrimSpace(os.Getenv("WINDIR")); windir != "" {
		return windir
	}
	return `C:\Windows`
}

func touchSandboxEnvRoot(envRoot string) {
	envRoot = strings.TrimSpace(envRoot)
	if envRoot == "" {
		return
	}
	now := time.Now()
	_ = os.Chtimes(envRoot, now, now)
}

func hostUserSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".caelis", "skills")
}

func mergeEnv(extra map[string]string, forced map[string]string) []string {
	values := map[string]string{}
	names := map[string]string{}
	set := func(key string, value string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		canonical := strings.ToUpper(key)
		if existing := names[canonical]; existing != "" && existing != key {
			delete(values, existing)
		}
		names[canonical] = key
		values[key] = value
	}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		set(key, value)
	}
	for key, value := range extra {
		set(key, value)
	}
	for key, value := range forced {
		set(key, value)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func commandEnvValue(extra map[string]string, key string) string {
	value, _ := lookupCommandEnv(extra, key)
	return value
}

func lookupExtraCommandEnv(extra map[string]string, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	for name, value := range extra {
		if strings.EqualFold(strings.TrimSpace(name), key) {
			return value, true
		}
	}
	return "", false
}

func lookupCommandEnv(extra map[string]string, key string) (string, bool) {
	if value, ok := lookupExtraCommandEnv(extra, key); ok {
		return value, true
	}
	key = strings.TrimSpace(key)
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), key) {
			return value, true
		}
	}
	return "", false
}

func prependEnvPath(first string, rest string) string {
	first = strings.TrimSpace(first)
	if first == "" {
		return rest
	}
	if strings.TrimSpace(rest) == "" {
		return first
	}
	return first + string(os.PathListSeparator) + rest
}

func writeSandboxPythonSiteCustomize(siteDir string) error {
	siteDir = strings.TrimSpace(siteDir)
	if siteDir == "" {
		return nil
	}
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		return fmt.Errorf("impl/sandbox/windows: prepare Python sandbox site directory %s: %w", siteDir, err)
	}
	path := filepath.Join(siteDir, "sitecustomize.py")
	if err := os.WriteFile(path, []byte(sandboxPythonSiteCustomize), 0o600); err != nil {
		return fmt.Errorf("impl/sandbox/windows: write Python sandbox sitecustomize %s: %w", path, err)
	}
	return nil
}
