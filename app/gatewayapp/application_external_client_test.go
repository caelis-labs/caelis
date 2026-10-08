//go:build darwin || linux || windows

package gatewayapp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

// TestExternalExecutionClientHTTP uses an independently compiled client with
// public imports only against the real HTTP Host and deterministic model. It
// requires native sandbox execution, which an outer sandbox may disallow.
func TestExternalExecutionClientHTTP(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native application acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "execution-client")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./app/gatewayapp/testdata/execution_client")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile external public client: %v\n%s", err, output)
	}
	root := t.TempDir()
	store, workspace := filepath.Join(root, "store"), filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	provider := &nativeModelScript{}
	host := startApplicationHTTPHost(t, store, workspace, provider)
	defer func() {
		if host != nil {
			host.close(t)
		}
	}()
	// Model and application enrollment use existing public Host fixtures. The
	// external binary then creates and reads its own application Session.
	_, _ = nativeHostSession(t, ctx, host, workspace)
	credential := filepath.Join(root, "native.credential")
	run := func(action, id string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, binary, action, host.server.URL, credential, id)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("external client %s failed: %v\n%s", action, err, output)
		}
		return output
	}
	var created appserver.CommandResult
	if err := json.Unmarshal(run("create", workspace), &created); err != nil || created.Outcome != appserver.OutcomeCommitted || created.SessionID == "" {
		t.Fatalf("external create = %+v (%v)", created, err)
	}
	inspect := func() {
		t.Helper()
		var got struct {
			Binding application.Binding    `json:"binding"`
			State   appserver.SessionState `json:"state"`
		}
		if err := json.Unmarshal(run("inspect", created.SessionID), &got); err != nil {
			t.Fatal(err)
		}
		wantEnv := map[string]string{"CAELIS_EXTERNAL_FIXTURE": "external-value"}
		if runtime.GOOS == "windows" {
			wantEnv["SystemRoot"] = os.Getenv("SystemRoot")
			wantEnv["TEMP"], wantEnv["TMP"] = workspace, workspace
		}
		for _, cfg := range []*sandbox.ExecutionConfig{got.Binding.Profile.ExecutionConfig, got.State.ExecutionConfig} {
			if cfg == nil || cfg.Environment.Inherit == nil || *cfg.Environment.Inherit || !reflect.DeepEqual(cfg.Environment.Set, wantEnv) {
				t.Fatalf("external profile/Session readback diverged: %+v", got)
			}
		}
		if got.State.CWD != workspace || got.State.SessionID != created.SessionID {
			t.Fatalf("external canonical Session = %+v", got.State)
		}
	}
	inspect()
	for _, action := range []string{"prompt", "prompt-restarted"} {
		if action == "prompt-restarted" {
			host.close(t)
			host = startApplicationHTTPHost(t, store, workspace, provider)
			inspect()
		}
		filename := action + ".txt"
		command := `printf '%s' "$CAELIS_EXTERNAL_FIXTURE" > ` + filename
		if runtime.GOOS == "windows" {
			command = "[IO.File]::WriteAllText('" + filename + "', $env:CAELIS_EXTERNAL_FIXTURE)"
		}
		input, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		provider.set(nativeModelTool{"RunCommand", string(input)})
		var prompted appserver.CommandResult
		if err := json.Unmarshal(run(action, created.SessionID), &prompted); err != nil || prompted.Outcome != appserver.OutcomeCommitted && prompted.Outcome != appserver.OutcomeAccepted {
			t.Fatalf("external %s = %+v (%v)", action, prompted, err)
		}
		actual, err := os.ReadFile(filepath.Join(workspace, filename))
		if err != nil || strings.TrimSpace(string(actual)) != "external-value" {
			t.Fatalf("native external client %s environment = %q (%v)", action, actual, err)
		}
	}
}
