package runnerclient

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerEnvironmentProvidesPathExt(t *testing.T) {
	t.Setenv("PATHEXT", "")

	client := New(Config{StateRoot: t.TempDir()})
	env, err := client.runnerEnvironment(Credentials{})
	if err != nil {
		t.Fatalf("runnerEnvironment() error = %v", err)
	}
	got := strings.ToUpper(testEnvValue(env, "PATHEXT"))
	if got == "" {
		t.Fatal("PATHEXT missing from runner environment")
	}
	if !strings.Contains(got, ".EXE") || !strings.Contains(got, ".CMD") {
		t.Fatalf("PATHEXT = %q, want Windows executable extensions", got)
	}
}

func TestRunnerEnvironmentUsesSandboxPrivateDirsWithoutCacheRedirects(t *testing.T) {
	stateRoot := t.TempDir()
	hostProfile := filepath.Join(t.TempDir(), "host-profile")
	hostCache := filepath.Join(t.TempDir(), "host-go-cache")
	t.Setenv("USERPROFILE", hostProfile)
	t.Setenv("GOCACHE", hostCache)
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "host-go-mod-cache"))
	t.Setenv("npm_config_cache", filepath.Join(t.TempDir(), "host-npm-cache"))

	client := New(Config{StateRoot: stateRoot})
	env, err := client.runnerEnvironment(Credentials{Username: "CaelisSbxOffTest"})
	if err != nil {
		t.Fatalf("runnerEnvironment() error = %v", err)
	}
	home := testEnvValue(env, "CAELIS_SANDBOX_HOME")
	userProfile := testEnvValue(env, "USERPROFILE")
	localAppData := testEnvValue(env, "LOCALAPPDATA")
	if home == "" || !pathIsUnder(home, stateRoot) {
		t.Fatalf("CAELIS_SANDBOX_HOME = %q, want under state root %q", home, stateRoot)
	}
	if !strings.EqualFold(userProfile, hostProfile) {
		t.Fatalf("USERPROFILE = %q, want inherited host profile %q", userProfile, hostProfile)
	}
	if localAppData == "" || !pathIsUnder(localAppData, home) {
		t.Fatalf("LOCALAPPDATA = %q, want under sandbox home %q", localAppData, home)
	}
	for _, key := range []string{
		"GOCACHE",
		"GOPATH",
		"GOMODCACHE",
		"npm_config_cache",
		"YARN_CACHE_FOLDER",
		"PIP_CACHE_DIR",
		"UV_CACHE_DIR",
		"CARGO_HOME",
		"GRADLE_USER_HOME",
		"NUGET_PACKAGES",
		"npm_config_store_dir",
		"PNPM_HOME",
		"BUN_INSTALL",
		"BUN_INSTALL_CACHE_DIR",
	} {
		if got := testEnvValue(env, key); got != "" {
			t.Fatalf("%s = %q, did not expect sandbox-local cache redirect", key, got)
		}
	}
}

func TestCommandExitError(t *testing.T) {
	if err := commandExitError(0, ""); err != nil {
		t.Fatalf("commandExitError(0) = %v, want nil", err)
	}
	if err := commandExitError(17, "process exited with code 17"); err == nil || !strings.Contains(err.Error(), "17") {
		t.Fatalf("commandExitError(17) = %v, want exit failure", err)
	}
	if err := commandExitError(3, ""); err == nil || !strings.Contains(err.Error(), "3") {
		t.Fatalf("commandExitError(3, empty reason) = %v, want synthesized exit failure", err)
	}
}

func testEnvValue(env []string, key string) string {
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}

func pathIsUnder(path string, root string) bool {
	path = strings.ToLower(filepath.Clean(path))
	root = strings.ToLower(filepath.Clean(root))
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
