package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestResolveACPConnectionLauncherUsesInstalledNativeACPCommand(t *testing.T) {
	tests := []struct {
		adapter string
		command string
		args    []string
		env     map[string]string
	}{
		{adapter: "grok", command: "grok", args: []string{"agent", "stdio"}},
		{adapter: "kimi", command: "kimi", args: []string{"acp"}},
		{adapter: "opencode", command: "opencode", args: []string{"acp"}},
		{adapter: "copilot", command: "copilot", args: []string{"--acp"}},
		{adapter: "qoder", command: "qoder", args: []string{"--acp"}},
		{adapter: "gemini", command: "gemini", args: []string{"--acp"}},
		{adapter: "qwen-code", command: "qwen", args: []string{"--acp"}},
		{
			adapter: "auggie", command: "auggie", args: []string{"--acp"},
			env: map[string]string{"AUGMENT_DISABLE_AUTO_UPDATE": "1"},
		},
		{adapter: "cline", command: "cline", args: []string{"--acp"}},
		{
			adapter: "factory-droid", command: "droid", args: []string{"exec", "--output-format", "acp-daemon"},
			env: map[string]string{
				"DROID_DISABLE_AUTO_UPDATE":         "true",
				"FACTORY_DROID_AUTO_UPDATE_ENABLED": "false",
			},
		},
		{adapter: "goose", command: "goose", args: []string{"acp"}},
		{adapter: "kilo", command: "kilo", args: []string{"acp"}},
	}
	for _, test := range tests {
		t.Run(test.adapter, func(t *testing.T) {
			binDir := t.TempDir()
			writeExternalAgentExecutable(t, binDir, test.command)
			t.Setenv("PATH", binDir)

			connection, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
				AdapterID: test.adapter, Launcher: controlagents.LauncherChoiceInstalled,
			})
			if err != nil {
				t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
			}
			if connection.ID != test.adapter || connection.Launcher.Command != test.command || connection.Launcher.Kind != controlagents.LaunchKindExecutable {
				t.Fatalf("connection = %#v, want PATH command %q", connection, test.command)
			}
			if !reflect.DeepEqual(connection.Launcher.Args, test.args) {
				t.Fatalf("launcher args = %#v, want %#v", connection.Launcher.Args, test.args)
			}
			if !reflect.DeepEqual(connection.Launcher.Env, test.env) {
				t.Fatalf("launcher env = %#v, want %#v", connection.Launcher.Env, test.env)
			}
		})
	}
}

func TestResolveACPConnectionLauncherUsesQoderCommandAlias(t *testing.T) {
	binDir := t.TempDir()
	writeExternalAgentExecutable(t, binDir, "qodercli")
	t.Setenv("PATH", binDir)

	connection, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "qoder", Launcher: controlagents.LauncherChoiceInstalled,
	})
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	if connection.Launcher.Command != "qodercli" || !reflect.DeepEqual(connection.Launcher.Args, []string{"--acp"}) {
		t.Fatalf("connection launcher = %#v, want qodercli alias", connection.Launcher)
	}
}

func TestResolveACPConnectionLauncherRequiresNativeCommandOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	connection, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "grok", Launcher: controlagents.LauncherChoiceInstalled,
	})
	if err == nil || !strings.Contains(err.Error(), `"grok" is available on PATH`) || connection.Launcher.Command != "" {
		t.Fatalf("resolveACPConnectionLauncher() = %#v, %v", connection, err)
	}
}

func TestResolveACPConnectionLauncherRejectsRemovedRegistryLaunchers(t *testing.T) {
	for _, req := range []controlagents.ConnectRequest{
		{AdapterID: "codex", Launcher: controlagents.LauncherChoiceManaged},
		{AdapterID: "claude", Launcher: controlagents.LauncherChoiceGlobal},
		{AdapterID: "factory-droid", Launcher: controlagents.LauncherChoiceNPX},
		{AdapterID: "grok", Launcher: controlagents.LauncherChoiceNPX},
	} {
		if _, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), req); err == nil {
			t.Fatalf("resolveACPConnectionLauncher(%#v) succeeded, want removed launcher rejected", req)
		}
	}
}

func TestSplitACPCommandLinePreservesQuotedExecutableAndArguments(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "agent bins")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wantCommand := writeExternalAgentExecutable(t, binDir, "custom acp")
	command, args, err := splitACPCommandLine(`"` + wantCommand + `" --mode "deep review"`)
	if err != nil {
		t.Fatal(err)
	}
	if command != wantCommand || len(args) != 2 || args[0] != "--mode" || args[1] != "deep review" {
		t.Fatalf("split command = %q %#v", command, args)
	}
}

func TestSplitACPCommandLineKeepsPATHCommandLogical(t *testing.T) {
	binDir := t.TempDir()
	writeExternalAgentExecutable(t, binDir, "codex-acp")
	t.Setenv("PATH", binDir)

	command, args, err := splitACPCommandLine("codex-acp --profile latest")
	if err != nil {
		t.Fatal(err)
	}
	if command != "codex-acp" || !reflect.DeepEqual(args, []string{"--profile", "latest"}) {
		t.Fatalf("split command = %q %#v, want logical PATH entry", command, args)
	}
}

func TestSplitACPCommandLineCanonicalizesRelativeExecutable(t *testing.T) {
	binDir := t.TempDir()
	wantCommand := writeExternalAgentExecutable(t, binDir, "relative-acp")
	t.Chdir(binDir)

	commandLine := "." + string(filepath.Separator) + filepath.Base(wantCommand) + " --profile local"
	command, args, err := splitACPCommandLine(commandLine)
	if err != nil {
		t.Fatal(err)
	}
	if command != wantCommand || !reflect.DeepEqual(args, []string{"--profile", "local"}) {
		t.Fatalf("split command = %q %#v, want absolute %q", command, args, wantCommand)
	}
}

func TestRosterNameValidationRejectsRuntimeAndRunAddresses(t *testing.T) {
	for _, name := range []string{"self", "local", "main", "kernel", "sandbox", "guardian", "reviewer", "status", "worker(lina)", "bad name"} {
		if !forbiddenExternalAgentID(name) {
			t.Fatalf("forbiddenExternalAgentID(%q) = false", name)
		}
	}
	for _, name := range []string{"opus", "worker-2", "deepseek-v4-pro", "mimo-v2-5-pro"} {
		if forbiddenExternalAgentID(name) {
			t.Fatalf("forbiddenExternalAgentID(%q) = true", name)
		}
	}
}

func storedACPAgentInfo(values []ACPAgentInfo, name string) (ACPAgentInfo, bool) {
	for _, value := range values {
		if strings.EqualFold(value.Name, name) {
			return value, true
		}
	}
	return ACPAgentInfo{}, false
}

func writeExternalAgentExecutable(t *testing.T, dir string, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test executable fixture\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Abs(%s) error = %v", path, err)
	}
	return abs
}
