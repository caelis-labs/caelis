package gatewayapp_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/httpclient"
	"github.com/caelis-labs/caelis/internal/testenv"
)

// TestPluginMutationHTTPOutcomeParity covers Host plugin mutation header,
// outcome, and error parity through the real HTTP AppServer path without
// binding a host kernel socket.
func TestPluginMutationHTTPOutcomeParity(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspaceDir := filepath.Join(root, "ws")
	for _, dir := range []string{storeDir, workspaceDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace", WorkspaceCWD: workspaceDir,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })

	appServer, err := local.NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(token, controlclient.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{
		Services: appServer.Services, TaskStreams: appServer.TaskStreams,
	}, controlserver.Config{
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := testenv.NewHTTPServer(t, handler)
	client, err := httpclient.New(httpclient.Config{
		BaseURL: httpServer.URL, BearerToken: token,
		HTTPClient:    httpServer.Client(),
		Compatibility: controlclient.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	clients, _, err := httpclient.BindAppServer(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	revision, err := host.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".caelis-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".caelis-plugin", "plugin.json"), []byte(`{"name":"http-demo","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	committed, err := clients.Plugins.AddPluginPath(ctx, controlclient.AddPluginPathRequest{
		WriteBase: controlclient.WriteBase{OperationID: "http-plugin-add", ExpectedRevision: &revision},
		Path:      pluginDir,
	})
	if err != nil || committed.Outcome != controlclient.OutcomeCommitted || committed.Revision <= revision {
		t.Fatalf("AddPluginPath(committed) = %#v, %v", committed, err)
	}
	if committed.Resource == nil || committed.Resource.Kind != controlclient.CommandResourcePlugin {
		t.Fatalf("committed Resource = %#v, want plugin kind", committed.Resource)
	}

	// Stale revision must surface as conflicted with HTTP 409 parity (client
	// wraps OutcomeError; status is validated inside httpclient.doCommand).
	stale := revision
	conflicted, conflictErr := clients.Plugins.EnablePlugin(ctx, controlclient.EnablePluginRequest{
		WriteBase: controlclient.WriteBase{OperationID: "http-plugin-stale", ExpectedRevision: &stale},
		ID:        "http-demo",
	})
	if conflictErr == nil || conflicted.Outcome != controlclient.OutcomeConflicted {
		t.Fatalf("EnablePlugin(stale) = %#v, %v want conflicted", conflicted, conflictErr)
	}
	var outcomeErr *controlclient.OutcomeError
	if !errors.As(conflictErr, &outcomeErr) || outcomeErr.Outcome != controlclient.OutcomeConflicted {
		t.Fatalf("stale error = %v, want OutcomeError(conflicted)", conflictErr)
	}

	current, err := host.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rejected, rejectErr := clients.Plugins.InstallPlugin(ctx, controlclient.InstallPluginRequest{
		WriteBase: controlclient.WriteBase{OperationID: "http-plugin-empty", ExpectedRevision: &current},
		Source:    "",
	})
	if rejectErr == nil || rejected.Outcome != controlclient.OutcomeRejected {
		t.Fatalf("InstallPlugin(empty) = %#v, %v want rejected", rejected, rejectErr)
	}
	if !errors.As(rejectErr, &outcomeErr) || outcomeErr.Outcome != controlclient.OutcomeRejected {
		t.Fatalf("empty source error = %v, want OutcomeError(rejected)", rejectErr)
	}

	t.Run("post-effect CAS is unknown and replay does not reclone", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test git wrapper uses a POSIX shell")
		}
		remoteSource := "https://example.com/plugins/http-race-market.git"
		marketRepo := filepath.Join(root, "http-race-market")
		writeHTTPParityGitMarketplace(t, marketRepo)
		started, release, count := installBlockingGitWrapper(t, marketRepo, remoteSource)

		expected, err := host.ConfigurationRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		req := controlclient.AddMarketplaceRequest{
			WriteBase: controlclient.WriteBase{OperationID: "http-market-post-effect-cas", ExpectedRevision: &expected},
			Source:    remoteSource,
		}
		type commandCall struct {
			result controlclient.CommandResult
			err    error
		}
		callDone := make(chan commandCall, 1)
		go func() {
			result, callErr := clients.Plugins.AddMarketplace(ctx, req)
			callDone <- commandCall{result: result, err: callErr}
		}()
		waitForHTTPParityPath(t, started)

		bumpDir := filepath.Join(root, "revision-bump")
		if err := os.MkdirAll(filepath.Join(bumpDir, ".caelis-plugin"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bumpDir, ".caelis-plugin", "plugin.json"), []byte(`{"name":"revision-bump","version":"1.0.0"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		bumped, bumpErr := clients.Plugins.AddPluginPath(ctx, controlclient.AddPluginPathRequest{
			WriteBase: controlclient.WriteBase{OperationID: "http-plugin-race-bump", ExpectedRevision: &expected},
			Path:      bumpDir,
		})
		if bumpErr != nil || bumped.Outcome != controlclient.OutcomeCommitted {
			t.Fatalf("AddPluginPath(revision bump) = %#v, %v", bumped, bumpErr)
		}
		if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		call := <-callDone
		if call.err == nil || call.result.Outcome != controlclient.OutcomeUnknown {
			t.Fatalf("AddMarketplace(post-effect CAS) = %#v, %v; want unknown", call.result, call.err)
		}
		if !errors.As(call.err, &outcomeErr) || outcomeErr.Outcome != controlclient.OutcomeUnknown {
			t.Fatalf("post-effect error = %v, want OutcomeError(unknown)", call.err)
		}
		if got := blockingGitCloneCount(t, count); got != 1 {
			t.Fatalf("clone count after first command = %d, want 1", got)
		}

		replayed, replayErr := clients.Plugins.AddMarketplace(ctx, req)
		if replayErr == nil || replayed.Outcome != controlclient.OutcomeUnknown {
			t.Fatalf("AddMarketplace(replay) = %#v, %v; want stored unknown", replayed, replayErr)
		}
		if got := blockingGitCloneCount(t, count); got != 1 {
			t.Fatalf("clone count after replay = %d, want 1", got)
		}
	})
}

func writeHTTPParityGitMarketplace(t *testing.T, root string) {
	t.Helper()
	manifestDir := filepath.Join(root, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"http-race-market","owner":{"name":"tester"},"plugins":[]}`
	if err := os.WriteFile(filepath.Join(manifestDir, "marketplace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "."},
		{"commit", "-m", "marketplace"},
	} {
		cmd := exec.Command(realGit, append([]string{"-C", root}, args...)...)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("git %v: %v\n%s", args, runErr, output)
		}
	}
}

func installBlockingGitWrapper(t *testing.T, localRepo, remoteSource string) (started, release, count string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	dir := t.TempDir()
	started = filepath.Join(dir, "clone-started")
	release = filepath.Join(dir, "clone-release")
	count = filepath.Join(dir, "clone-count")
	wrapper := filepath.Join(dir, "git")
	script := `#!/bin/sh
if [ "$1" = "clone" ]; then
  printf 'clone\n' >> "$CAELIS_TEST_GIT_COUNT"
  : > "$CAELIS_TEST_GIT_STARTED"
  while [ ! -e "$CAELIS_TEST_GIT_RELEASE" ]; do
    sleep 0.01
  done
fi
exec "$CAELIS_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAELIS_TEST_REAL_GIT", realGit)
	t.Setenv("CAELIS_TEST_GIT_STARTED", started)
	t.Setenv("CAELIS_TEST_GIT_RELEASE", release)
	t.Setenv("CAELIS_TEST_GIT_COUNT", count)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+(&url.URL{Scheme: "file", Path: localRepo}).String()+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", remoteSource)
	t.Setenv("GIT_CONFIG_KEY_1", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_1", "always")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("cleanup\n"), 0o600) })
	return started, release, count
}

func waitForHTTPParityPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func blockingGitCloneCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "clone\n")
}
