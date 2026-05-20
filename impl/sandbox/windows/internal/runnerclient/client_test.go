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

func TestRunnerEnvironmentUsesSandboxLocalCaches(t *testing.T) {
	stateRoot := t.TempDir()
	hostCache := filepath.Join(t.TempDir(), "host-go-cache")
	t.Setenv("GOCACHE", hostCache)
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "host-go-mod-cache"))
	t.Setenv("npm_config_cache", filepath.Join(t.TempDir(), "host-npm-cache"))

	client := New(Config{StateRoot: stateRoot})
	env, err := client.runnerEnvironment(Credentials{Username: "CaelisSbxOffTest"})
	if err != nil {
		t.Fatalf("runnerEnvironment() error = %v", err)
	}
	home := testEnvValue(env, "CAELIS_SANDBOX_HOME")
	localAppData := testEnvValue(env, "LOCALAPPDATA")
	if home == "" || !pathIsUnder(home, stateRoot) {
		t.Fatalf("CAELIS_SANDBOX_HOME = %q, want under state root %q", home, stateRoot)
	}
	for _, tc := range []struct {
		key  string
		root string
	}{
		{"GOCACHE", localAppData},
		{"GOMODCACHE", home},
		{"npm_config_cache", localAppData},
		{"YARN_CACHE_FOLDER", localAppData},
		{"PIP_CACHE_DIR", localAppData},
		{"UV_CACHE_DIR", localAppData},
		{"CARGO_HOME", home},
		{"GRADLE_USER_HOME", home},
		{"NUGET_PACKAGES", home},
		{"npm_config_store_dir", localAppData},
		{"PNPM_HOME", localAppData},
		{"BUN_INSTALL", home},
		{"BUN_INSTALL_CACHE_DIR", home},
	} {
		got := testEnvValue(env, tc.key)
		if got == "" || !pathIsUnder(got, tc.root) {
			t.Fatalf("%s = %q, want under %q", tc.key, got, tc.root)
		}
		if strings.EqualFold(got, hostCache) {
			t.Fatalf("%s = %q, did not expect host cache", tc.key, got)
		}
	}
}

func TestPlainCommandExitReason(t *testing.T) {
	for _, reason := range []string{
		"exit status 1",
		"signal: killed",
		"process exited with code 1",
	} {
		if !plainCommandExitReason(reason) {
			t.Fatalf("plainCommandExitReason(%q) = false, want true", reason)
		}
	}
	if plainCommandExitReason("runner protocol error") {
		t.Fatal("plainCommandExitReason(runner protocol error) = true, want false")
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
