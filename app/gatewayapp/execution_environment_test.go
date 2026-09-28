package gatewayapp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestRuntimeCommandEnvironmentExcludesOnlyInternalConnection(t *testing.T) {
	private := []string{"CAELIS_CONTROL_TOKEN", "CAELIS_CONTROL_TOKEN_FILE", "CAELIS_CONTROL_URL", "CAELIS_COLLABORATION_TOKEN", "CAELIS_COLLABORATION_URL", "CAELIS_COLLABORATION_TOOLS"}
	for _, name := range private {
		t.Setenv(name, "synthetic-private-value")
	}
	t.Setenv("CAELIS_TEST_USER_TOOL_CONFIG", "preserved")
	env := runtimeCommandEnvironment()
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for _, name := range private {
		if _, ok := values[name]; ok {
			t.Fatalf("inherited internal connection field %s", name)
		}
		if os.Getenv(name) != "synthetic-private-value" {
			t.Fatalf("assembly mutated Host field %s", name)
		}
	}
	if values["CAELIS_TEST_USER_TOOL_CONFIG"] != "preserved" || values["PATH"] != os.Getenv("PATH") {
		t.Fatal("user environment changed at the product boundary")
	}
}

func TestApplicationCommandPreservesEnvironmentOverrides(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rt := &applicationExecutionRuntime{cwd: cwd}
	env := map[string]string{"HOME": "caller-home", "PATH": "caller-tools", "ZDOTDIR": "caller-dotfiles", "EMPTY": ""}
	request := sandbox.CommandRequest{Env: env, UnsetEnv: []string{"REMOVE"}}
	got, err := rt.command(request)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != cwd || !reflect.DeepEqual(got.Env, env) || !reflect.DeepEqual(got.UnsetEnv, request.UnsetEnv) {
		t.Fatal("application command normalization replaced process configuration")
	}
	if request.Dir != "" || !reflect.DeepEqual(request.Env, env) {
		t.Fatal("application command normalization mutated the caller")
	}
}
