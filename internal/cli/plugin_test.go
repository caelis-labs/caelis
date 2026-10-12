package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func writeCLIPluginFile(t *testing.T, root, path, content string) {
	t.Helper()
	file := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resetCLIPluginEnv(t *testing.T) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	clearSelfAgentEnv(t)
	for _, key := range []string{"CAELIS_STORE_DIR", "CAELIS_CONTROL_URL", "CAELIS_CONTROL_EMBEDDED", "CAELIS_CONTROL_TOKEN", "CAELIS_CONTROL_TOKEN_FILE", "CAELIS_CONTROL_LISTEN"} {
		t.Setenv(key, "")
	}
}

func runCLIPluginJSON(t *testing.T, ctx context.Context, opener productClientOpener, args ...string) (pluginCommandOutput, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	err := runWithProductClientOpener(ctx, append([]string{"plugin"}, args...), strings.NewReader("must not become a prompt"), &out, &stderr, opener)
	var result pluginCommandOutput
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("JSON result = %q; stderr=%q; error=%v; decode=%v", out.String(), stderr.String(), err, decodeErr)
	}
	if result.SchemaVersion != "1" {
		t.Fatalf("result schema = %+v", result)
	}
	return result, err
}

func TestCLIPluginIsolatedInstallStatusRepeatRemove(t *testing.T) {
	resetCLIPluginEnv(t)
	t.Chdir(t.TempDir())
	store := cliTestStoreDir(t)
	root := filepath.Join(t.TempDir(), "插件 with spaces")
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"1.0.0","author":{"name":"Caelis Labs"}}`
	writeCLIPluginFile(t, root, "plugin.json", manifest)
	writeCLIPluginFile(t, root, "skills/desktop-world/SKILL.md", "---\nname: desktop-world\ndescription: desktop fixture\n---\n")
	other := t.TempDir()
	writeCLIPluginFile(t, other, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"unrelated","version":"1.0.0"}`)
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	wantOther, err := filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	opener := roundTripProductClientOpener(t)
	call := func(action, target string) pluginCommandOutput {
		t.Helper()
		result, err := runCLIPluginJSON(t, t.Context(), opener, action, target, "--json", "--embedded", "--store-dir", store)
		if err != nil {
			t.Fatalf("%s = %+v, %v", action, result, err)
		}
		return result
	}
	call("install", other)
	for range 2 {
		result := call("install", root)
		if !result.Installed || result.Plugin == nil || result.ID != "desktop-world" || result.Plugin.Root != wantRoot || result.Plugin.Version != "1.0.0" || !result.Plugin.Enabled || len(result.Plugin.Skills) != 1 {
			t.Fatalf("install = %+v", result)
		}
	}
	writeCLIPluginFile(t, root, "plugin.json", strings.Replace(manifest, `"version":"1.0.0"`, `"version":"2.0.0"`, 1))
	if got := call("install", root); got.Plugin.Version != "2.0.0" {
		t.Fatalf("upgrade = %+v", got)
	}
	if got := call("status", "DESKTOP-WORLD"); !got.Installed || got.Plugin.Version != "2.0.0" {
		t.Fatalf("status = %+v", got)
	}
	collision := t.TempDir()
	writeCLIPluginFile(t, collision, "plugin.json", strings.Replace(manifest, "Caelis Labs", "Other publisher", 1))
	got, err := runCLIPluginJSON(t, t.Context(), opener, "install", collision, "--store-dir", store, "--embedded", "--json")
	if err == nil || got.Receipt == nil || !strings.Contains(got.Error, "another source") {
		t.Fatalf("collision = %+v, %v", got, err)
	}
	if got := call("status", "desktop-world"); got.Plugin.Root != wantRoot {
		t.Fatalf("collision replaced existing root: %+v", got)
	}
	for range 2 {
		if got := call("remove", "desktop-world"); got.Installed || got.Plugin != nil {
			t.Fatalf("remove = %+v", got)
		}
	}
	if got := call("status", "desktop-world"); got.Installed {
		t.Fatalf("removed status = %+v", got)
	}
	if got := call("status", "unrelated"); !got.Installed || got.Plugin.Root != wantOther {
		t.Fatalf("unrelated plugin lost: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "plugin.json")); err != nil {
		t.Fatalf("remove deleted external package: %v", err)
	}
}

func TestCLIPluginValidationAndHostFailures(t *testing.T) {
	resetCLIPluginEnv(t)
	t.Chdir(t.TempDir())
	root := t.TempDir()
	writeCLIPluginFile(t, root, "plugin.json", "invalid JSON")
	for _, args := range [][]string{
		{"install", "relative"}, {"install", filepath.Join(root, "missing")},
		{"install", filepath.Join(root, "plugin.json")}, {"unknown", "demo"},
		{"remove", ""}, {"status"}, {"status", "demo", "extra"}, {"status", "demo", "--bad-flag"},
	} {
		_, err := runCLIPluginJSON(t, t.Context(), func(context.Context, gatewayapp.Config, productClientOptions) (*productClients, error) {
			t.Fatal("invalid arguments opened a Host")
			return nil, nil
		}, append(args, "--json")...)
		if err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
	result, err := runCLIPluginJSON(t, t.Context(), roundTripProductClientOpener(t), "install", root, "--json", "--embedded", "--store-dir", cliTestStoreDir(t))
	if err == nil || result.Installed || !strings.Contains(result.Error, "parse plugin failed") {
		t.Fatalf("malformed source = %+v, %v", result, err)
	}
	sentinel := errors.New("Store unavailable")
	result, err = runCLIPluginJSON(t, t.Context(), func(context.Context, gatewayapp.Config, productClientOptions) (*productClients, error) {
		return nil, sentinel
	}, "status", "demo", "--json")
	if !errors.Is(err, sentinel) || !strings.Contains(result.Error, "Store unavailable") {
		t.Fatalf("Host failure = %+v, %v", result, err)
	}
	// Real Host ownership failure cannot silently open a second Store owner.
	store := cliTestStoreDir(t)
	owner, err := acquireProductHostOwnership(store)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	result, err = runCLIPluginJSON(t, t.Context(), roundTripProductClientOpener(t), "status", "demo", "--json", "--embedded", "--store-dir", store)
	if !errors.Is(err, ErrProductHostOwnershipConflict) || result.Error == "" {
		t.Fatalf("ownership failure = %+v, %v", result, err)
	}
}

func TestCLIPluginHelpFlagsAndLocalMode(t *testing.T) {
	resetCLIPluginEnv(t)
	var out, stderr bytes.Buffer
	if err := runWithProductClientOpener(t.Context(), []string{"plugin", "--help"}, nil, &out, &stderr, openProductClients); err != nil || !strings.Contains(stderr.String(), "caelis plugin install") {
		t.Fatalf("help = %q, %v", stderr.String(), err)
	}
	t.Setenv("CAELIS_CONTROL_URL", "http://remote.example")
	got, err := runCLIPluginJSON(t, t.Context(), openProductClients, "--json", "status", "demo")
	if err == nil || !strings.Contains(got.Error, "local Host") {
		t.Fatalf("remote mode = %+v, %v", got, err)
	}
}
