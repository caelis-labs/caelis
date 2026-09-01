package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/memorybinding"
	memorycredentialstore "github.com/caelis-labs/caelis/control/memorybinding/credentialstore"
	"github.com/caelis-labs/caelis/control/memorytool"
	"github.com/caelis-labs/caelis/surfaces/headless"
	"github.com/caelis-labs/memory/sdk/go/memory/sidecar"
)

const (
	memoryReleaseServiceVersion = "0.5.0-rc.1"
	memoryReleaseBuildRevision  = "6d34b9def418aaeb78c3504b2539ebe1cc7c77e3"
	memoryReleaseModuleVersion  = "v0.5.0-rc.1.0.20260901114055-6d34b9def418"
	memoryReleaseDarwinArm64SHA = "e761a776bb1695f2df14206c43b8dbec4446935c596ceff9a926fb039ee806bc"
	memoryGoldenPrivate         = "commit does not authorize push"
	memoryGoldenShared          = "the project uses Go"
)

func TestMemoryGoldenPathE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := memoryGoldenTempDir(t)
	artifact := buildMemoryGoldenArtifacts(t, ctx, root)
	dataDir := filepath.Join(root, "appliance")
	storeA := filepath.Join(root, "caelis-a")
	storeB := filepath.Join(root, "caelis-b")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	config := memoryGoldenConfiguration(artifact.manifest)
	for _, storeDir := range []string{storeA, storeB} {
		if err := newAppConfigStore(storeDir).Save(AppConfig{Memory: config}); err != nil {
			t.Fatalf("save Memory config: %v", err)
		}
	}
	provider := newMemoryGoldenProvider(t)

	stackA := newMemoryGoldenStack(t, artifact, provider, dataDir, storeA, workspace, "actor-a-private", false)
	credentials := bootstrapMemoryGoldenTopology(t, ctx, artifact.memoryctl, dataDir, root)
	putMemoryGoldenCredential(t, storeA, "principal:bot-a", credentials["principal:bot-a"])
	putMemoryGoldenCredential(t, storeB, "principal:bot-b", credentials["principal:bot-b"])
	provider.Begin(memoryGoldenScenario{
		name: "bot-a-private", actions: []memoryGoldenAction{
			{name: memorytool.RememberToolName, arguments: `{"text":"` + memoryGoldenPrivate + `"}`},
			{name: memorytool.RecallToolName, arguments: `{"query":"commit push"}`},
		},
	})
	privateA := runMemoryGoldenSession(t, ctx, stackA, "session-bot-a-private")
	privateBefore := memoryGoldenToolResults(t, stackA, privateA.SessionRef)
	assertMemoryGoldenResult(t, privateBefore, memorytool.RememberToolName, `{"accepted":true}`)
	assertMemoryGoldenResult(t, privateBefore, memorytool.RecallToolName, memoryGoldenPrivate)
	privateCursor := memoryGoldenConsistencyToken(t, stackA, privateA.SessionRef)
	provider.AssertComplete(t)
	if err := stackA.Close(); err != nil {
		t.Fatal(err)
	}

	stackA = newMemoryGoldenStack(t, artifact, provider, dataDir, storeA, workspace, "actor-a-private", false)
	if restartedCursor := memoryGoldenConsistencyToken(t, stackA, privateA.SessionRef); restartedCursor != privateCursor {
		t.Fatalf("restarted Caelis consistency cursor = %q, want %q", restartedCursor, privateCursor)
	}
	provider.Begin(memoryGoldenScenario{
		name:    "bot-a-private-after-restart",
		actions: []memoryGoldenAction{{name: memorytool.RecallToolName, arguments: `{"query":"commit push"}`}},
	})
	privateRestarted := runMemoryGoldenSession(t, ctx, stackA, privateA.SessionID)
	privateBefore = memoryGoldenToolResults(t, stackA, privateRestarted.SessionRef)
	assertMemoryGoldenLastResult(t, privateBefore, memorytool.RecallToolName, memoryGoldenPrivate)
	provider.AssertComplete(t)
	if err := stackA.Close(); err != nil {
		t.Fatal(err)
	}

	stackB := newMemoryGoldenStack(t, artifact, provider, dataDir, storeB, workspace, "actor-b-private", false)
	provider.Begin(memoryGoldenScenario{
		name:    "bot-b-private-isolation",
		actions: []memoryGoldenAction{{name: memorytool.RecallToolName, arguments: `{"query":"commit push"}`}},
	})
	privateB := runMemoryGoldenSession(t, ctx, stackB, "session-bot-b-private")
	privateBResults := memoryGoldenToolResults(t, stackB, privateB.SessionRef)
	assertMemoryGoldenResult(t, privateBResults, memorytool.RecallToolName, `{"fragments":[]}`)
	provider.AssertComplete(t)
	if err := stackB.Close(); err != nil {
		t.Fatal(err)
	}

	stackA = newMemoryGoldenStack(t, artifact, provider, dataDir, storeA, workspace, "actor-a-shared", false)
	provider.Begin(memoryGoldenScenario{
		name: "bot-a-shared", actions: []memoryGoldenAction{
			{name: memorytool.RememberToolName, arguments: `{"text":"` + memoryGoldenShared + `"}`},
			{name: memorytool.RecallToolName, arguments: `{"query":"project language"}`},
		},
	})
	sharedA := runMemoryGoldenSession(t, ctx, stackA, "session-bot-a-shared")
	sharedAResults := memoryGoldenToolResults(t, stackA, sharedA.SessionRef)
	assertMemoryGoldenResult(t, sharedAResults, memorytool.RecallToolName, memoryGoldenShared)
	provider.AssertComplete(t)

	stackB = newMemoryGoldenStack(t, artifact, provider, dataDir, storeB, workspace, "actor-b-shared", false)
	provider.Begin(memoryGoldenScenario{
		name:    "bot-b-shared",
		actions: []memoryGoldenAction{{name: memorytool.RecallToolName, arguments: `{"query":"project language"}`}},
	})
	sharedB := runMemoryGoldenSession(t, ctx, stackB, "session-bot-b-shared")
	sharedBResults := memoryGoldenToolResults(t, stackB, sharedB.SessionRef)
	assertMemoryGoldenResult(t, sharedBResults, memorytool.RecallToolName, memoryGoldenShared)
	provider.AssertComplete(t)
	if err := stackB.memorySidecar.Close(); err != nil {
		t.Fatal(err)
	}
	provider.Begin(memoryGoldenScenario{
		name:    "owner-after-attached-close",
		actions: []memoryGoldenAction{{name: memorytool.RecallToolName, arguments: `{"query":"project language"}`}},
	})
	ownerAfterAttachedClose := runMemoryGoldenSession(t, ctx, stackA, "session-owner-after-attached-close")
	ownerResults := memoryGoldenToolResults(t, stackA, ownerAfterAttachedClose.SessionRef)
	assertMemoryGoldenResult(t, ownerResults, memorytool.RecallToolName, memoryGoldenShared)
	provider.AssertComplete(t)
	if err := stackA.Close(); err != nil {
		t.Fatal(err)
	}
	provider.Begin(memoryGoldenScenario{
		name:    "memory-service-loss",
		actions: []memoryGoldenAction{{name: memorytool.RecallToolName, arguments: `{"query":"project language"}`}},
	})
	offlineRecall := runMemoryGoldenSession(t, ctx, stackB, "session-memory-service-loss")
	offlineResults := memoryGoldenToolResults(t, stackB, offlineRecall.SessionRef)
	assertMemoryGoldenResult(t, offlineResults, memorytool.RecallToolName, "unavailable")
	provider.AssertComplete(t)
	if err := stackB.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, "memory.db")); err != nil {
		t.Fatalf("durable appliance data after managed restarts: %v", err)
	}
	offline := newMemoryGoldenStack(t, artifact, provider, dataDir, storeA, workspace, "actor-a-private", true)
	privateAfter := memoryGoldenToolResults(t, offline, privateA.SessionRef)
	if !reflect.DeepEqual(privateAfter, privateBefore) {
		t.Fatalf("offline Session Replay changed Memory ToolResults:\nbefore=%q\nafter=%q", privateBefore, privateAfter)
	}
	if offline.memorySidecar != nil {
		t.Fatal("kill-switched replay started memoryd")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "memory.db")); err != nil {
		t.Fatalf("kill-switched replay changed appliance data: %v", err)
	}
	if err := offline.Close(); err != nil {
		t.Fatal(err)
	}
	assertMemoryGoldenDiagnosticsContainNoSecrets(t, []string{storeA, storeB}, credentials)
}

type memoryGoldenArtifact struct {
	manifestPath string
	memoryctl    string
	manifest     sidecar.Manifest
}

func memoryGoldenTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	root, err := os.MkdirTemp("/tmp", "caelis-memory-release-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func buildMemoryGoldenArtifacts(t *testing.T, ctx context.Context, root string) memoryGoldenArtifact {
	t.Helper()
	assertMemoryGoldenModulePin(t, ctx)
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	memoryctl := filepath.Join(binDir, memoryGoldenExecutable("memoryctl"))
	runMemoryGoldenCommand(t, ctx, repoRootForGatewayAppTest(t), "go", "build", "-trimpath", "-o", memoryctl, "github.com/caelis-labs/memory/cmd/memoryctl")
	if manifestPath := strings.TrimSpace(os.Getenv("CAELIS_MEMORY_GOLDEN_SIDECAR_MANIFEST")); manifestPath != "" {
		manifestPath, err := filepath.Abs(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := sidecar.Load(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		expectedDigest, ok := memoryReleaseArtifactDigest(manifest.GOOS, manifest.GOARCH)
		if !ok {
			t.Fatalf("no reviewed Memory artifact digest for %s/%s", manifest.GOOS, manifest.GOARCH)
		}
		if manifest.ServiceVersion != memoryReleaseServiceVersion ||
			manifest.BuildRevision != memoryReleaseBuildRevision ||
			manifest.SHA256 != expectedDigest {
			t.Fatalf("public Memory artifact identity = version %q revision %q digest %q", manifest.ServiceVersion, manifest.BuildRevision, manifest.SHA256)
		}
		if _, err := manifest.VerifySupportedNative(filepath.Dir(manifestPath)); err != nil {
			t.Fatal(err)
		}
		return memoryGoldenArtifact{manifestPath: manifestPath, memoryctl: memoryctl, manifest: manifest}
	}
	memoryd := filepath.Join(binDir, memoryGoldenExecutable("memoryd"))
	ldflags := fmt.Sprintf(
		"-s -w -X github.com/caelis-labs/memory/internal/buildinfo.ServiceVersion=%s -X github.com/caelis-labs/memory/internal/buildinfo.BuildRevision=%s",
		memoryReleaseServiceVersion,
		memoryReleaseBuildRevision,
	)
	runMemoryGoldenCommand(t, ctx, repoRootForGatewayAppTest(t), "go", "build", "-trimpath", "-ldflags", ldflags, "-o", memoryd, "github.com/caelis-labs/memory/cmd/memoryd")
	manifest, err := sidecar.CreateManifest(memoryd, memoryReleaseServiceVersion, memoryReleaseBuildRevision, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(binDir, "memoryd.manifest.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("using a source-built integration artifact; formal artifact digest acceptance requires CAELIS_MEMORY_GOLDEN_SIDECAR_MANIFEST")
	return memoryGoldenArtifact{manifestPath: manifestPath, memoryctl: memoryctl, manifest: manifest}
}

func memoryReleaseArtifactDigest(goos, goarch string) (string, bool) {
	if goos == "darwin" && goarch == "arm64" {
		return memoryReleaseDarwinArm64SHA, true
	}
	return "", false
}

func memoryGoldenExecutable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func assertMemoryGoldenModulePin(t *testing.T, ctx context.Context) {
	t.Helper()
	var module struct {
		Version string
		Main    bool
		Dir     string
	}
	output := memoryGoldenCommandOutput(t, ctx, repoRootForGatewayAppTest(t), "go", "list", "-m", "-json", "github.com/caelis-labs/memory")
	if err := json.Unmarshal(output, &module); err != nil {
		t.Fatal(err)
	}
	if !module.Main {
		if module.Version != memoryReleaseModuleVersion {
			t.Fatalf("Memory module version = %q, want %q", module.Version, memoryReleaseModuleVersion)
		}
		return
	}
	head := strings.TrimSpace(string(memoryGoldenCommandOutput(t, ctx, module.Dir, "git", "rev-parse", "HEAD")))
	if head != memoryReleaseBuildRevision {
		t.Fatalf("workspace Memory revision = %q, want %q", head, memoryReleaseBuildRevision)
	}
	if dirty := strings.TrimSpace(string(memoryGoldenCommandOutput(t, ctx, module.Dir, "git", "status", "--porcelain"))); dirty != "" {
		t.Fatalf("workspace Memory source is dirty:\n%s", dirty)
	}
}

func memoryGoldenConfiguration(manifest sidecar.Manifest) memorybinding.Configuration {
	endpoint := memorybinding.EndpointConfig{
		ID:         "memory-default",
		Deployment: memorybinding.DeploymentModeManagedLocal,
		Compatibility: memorybinding.APICompatibility{
			Protocol: manifest.Protocol, APIVersion: manifest.APIVersion, CoreProfile: manifest.CoreProfile,
			ServiceVersion: manifest.ServiceVersion, BuildRevision: manifest.BuildRevision, ArtifactSHA256: manifest.SHA256,
		},
	}
	return memorybinding.Configuration{
		Enabled:           true,
		Endpoint:          endpoint,
		DefaultBindingRef: "actor-a-private",
		Bindings: []memorybinding.AccessBinding{
			{
				BindingRef: "actor-a-private", RuntimeActorRef: "actor-bot-a",
				PrincipalRef: "principal:bot-a", IssuerCredentialRef: memorycredentialstore.BuildReference("principal:bot-a"),
				ViewRef: "view-bot-a-private", GrantRef: "grant-bot-a-private", Audience: memorybinding.OutputAudiencePrivate,
				BindingVersion: 1,
			},
			{
				BindingRef: "actor-a-shared", RuntimeActorRef: "actor-bot-a",
				PrincipalRef: "principal:bot-a", IssuerCredentialRef: memorycredentialstore.BuildReference("principal:bot-a"),
				ViewRef: "view-bot-a-shared", GrantRef: "grant-bot-a-shared", Audience: memorybinding.OutputAudienceShared,
				BindingVersion: 1,
			},
			{
				BindingRef: "actor-b-private", RuntimeActorRef: "actor-bot-b",
				PrincipalRef: "principal:bot-b", IssuerCredentialRef: memorycredentialstore.BuildReference("principal:bot-b"),
				ViewRef: "view-bot-b-private", GrantRef: "grant-bot-b-private", Audience: memorybinding.OutputAudiencePrivate,
				BindingVersion: 1,
			},
			{
				BindingRef: "actor-b-shared", RuntimeActorRef: "actor-bot-b",
				PrincipalRef: "principal:bot-b", IssuerCredentialRef: memorycredentialstore.BuildReference("principal:bot-b"),
				ViewRef: "view-bot-b-shared", GrantRef: "grant-bot-b-shared", Audience: memorybinding.OutputAudienceShared,
				BindingVersion: 1,
			},
		},
	}
}

func newMemoryGoldenStack(
	t *testing.T,
	artifact memoryGoldenArtifact,
	provider *memoryGoldenProvider,
	dataDir, storeDir, workspace string,
	bindingRef memorybinding.BindingRef,
	disabled bool,
) *Stack {
	t.Helper()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis-memory-release", UserID: "memory-golden", StoreDir: storeDir,
		WorkspaceKey: "memory-golden", WorkspaceCWD: workspace, SkillDirs: []string{},
		Sandbox: SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "openai-compatible", API: providers.APIOpenAICompatible,
			Model: "memory-golden", BaseURL: provider.URL, HTTPClient: provider.Client(),
			Token: "memory-model-test-token", AuthType: providers.AuthBearerToken,
			ContextWindowTokens: 128000, MaxOutputTok: 1024, Timeout: 5 * time.Second,
		},
		MemoryBindingRef: bindingRef, DisableMemory: disabled,
		MemorySidecarManifest: artifact.manifestPath, MemoryDataDir: dataDir,
	})
	if err != nil {
		t.Fatalf("start Memory Golden Path stack: %v", err)
	}
	return stack
}

func bootstrapMemoryGoldenTopology(t *testing.T, ctx context.Context, memoryctl, dataDir, root string) map[string]string {
	t.Helper()
	request := map[string]any{
		"realms": []any{map[string]any{"id": "realm-default"}},
		"identities": []any{
			map[string]any{"id": "identity-bot-a", "realm_id": "realm-default"},
			map[string]any{"id": "identity-bot-b", "realm_id": "realm-default"},
		},
		"spaces": []any{
			map[string]any{"id": "space-shared", "realm_id": "realm-default", "class": "shared"},
			map[string]any{"id": "space-bot-a", "realm_id": "realm-default", "identity_id": "identity-bot-a", "class": "private"},
			map[string]any{"id": "space-bot-b", "realm_id": "realm-default", "identity_id": "identity-bot-b", "class": "private"},
		},
		"views": []any{
			memoryGoldenView("view-bot-a-private", []string{"space-shared", "space-bot-a"}, "space-bot-a", "private"),
			memoryGoldenView("view-bot-b-private", []string{"space-shared", "space-bot-b"}, "space-bot-b", "private"),
			memoryGoldenView("view-bot-a-shared", []string{"space-shared"}, "space-shared", "shared"),
			memoryGoldenView("view-bot-b-shared", []string{"space-shared"}, "space-shared", "shared"),
		},
		"grants": []any{
			memoryGoldenGrant("grant-bot-a-private", "principal:bot-a", "actor-bot-a", "view-bot-a-private", "private"),
			memoryGoldenGrant("grant-bot-b-private", "principal:bot-b", "actor-bot-b", "view-bot-b-private", "private"),
			memoryGoldenGrant("grant-bot-a-shared", "principal:bot-a", "actor-bot-a", "view-bot-a-shared", "shared"),
			memoryGoldenGrant("grant-bot-b-shared", "principal:bot-b", "actor-bot-b", "view-bot-b-shared", "shared"),
		},
		"issuer_principals": []string{"principal:bot-a", "principal:bot-b"},
	}
	requestPath := filepath.Join(root, "bootstrap.json")
	writeMemoryGoldenJSON(t, requestPath, request)
	issuerPath := filepath.Join(root, "issuers.json")
	runMemoryGoldenCommand(
		t, ctx, root, memoryctl,
		"-data-dir", dataDir,
		"-management-credential", filepath.Join(dataDir, "management.token"),
		"bootstrap", "-file", requestPath, "-issuer-output", issuerPath,
	)
	var output struct {
		IssuerCredentials map[string]string `json:"issuer_credentials"`
	}
	data, err := os.ReadFile(issuerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if output.IssuerCredentials["principal:bot-a"] == "" || output.IssuerCredentials["principal:bot-b"] == "" {
		t.Fatalf("bootstrap returned incomplete issuer credentials")
	}
	return output.IssuerCredentials
}

func memoryGoldenView(id string, reads []string, write, disclosure string) map[string]any {
	return map[string]any{
		"id": id, "realm_id": "realm-default", "read_space_ids": reads,
		"write_space_id": write, "max_disclosure_class": disclosure, "version": 1,
	}
}

func memoryGoldenGrant(id, principal, actor, view, audience string) map[string]any {
	return map[string]any{
		"id": id, "principal_ref": principal, "actor_ref": actor, "view_ref": view,
		"allowed_operations": []string{"remember", "recall"}, "allowed_audiences": []string{audience},
		"expires_at": "2099-01-01T00:00:00Z", "version": 1,
	}
}

func putMemoryGoldenCredential(t *testing.T, storeDir, principal, credential string) {
	t.Helper()
	store, err := memorycredentialstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), memorycredentialstore.BuildReference(principal), credential); err != nil {
		t.Fatal(err)
	}
}

func runMemoryGoldenSession(t *testing.T, ctx context.Context, stack *Stack, sessionID string) session.Session {
	t.Helper()
	active, err := startGatewayAppTestSession(ctx, stack, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, sessionID, "execute Memory Golden Path step", headless.Options{})
	if err != nil {
		t.Fatalf("run Memory Golden Path Session: %v", err)
	}
	if strings.TrimSpace(result.Output) != "memory-golden-ok" {
		t.Fatalf("Memory Golden Path output = %q", result.Output)
	}
	return active
}

type memoryGoldenResult struct {
	name string
	data string
}

func memoryGoldenToolResults(t *testing.T, stack *Stack, ref session.SessionRef) []memoryGoldenResult {
	t.Helper()
	events, err := stack.composition.sessions.Events(context.Background(), session.EventsRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	var results []memoryGoldenResult
	for _, event := range events {
		if event == nil || event.Type != session.EventTypeToolResult {
			continue
		}
		message, ok := session.ModelMessageOf(event)
		if !ok || message.ToolResponse() == nil {
			continue
		}
		response := message.ToolResponse()
		if response.Name != memorytool.RememberToolName && response.Name != memorytool.RecallToolName {
			continue
		}
		data, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, memoryGoldenResult{name: response.Name, data: string(data)})
	}
	return results
}

func memoryGoldenConsistencyToken(t *testing.T, stack *Stack, ref session.SessionRef) string {
	t.Helper()
	state, err := stack.composition.sessions.SnapshotState(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := state[memorybinding.SessionStateKey].(map[string]any)
	token, _ := binding["consistency_token"].(string)
	if strings.TrimSpace(token) == "" {
		t.Fatalf("Session %q has no hidden Memory consistency cursor: %#v", ref.SessionID, binding)
	}
	return token
}

func assertMemoryGoldenResult(t *testing.T, results []memoryGoldenResult, name, contains string) {
	t.Helper()
	for _, result := range results {
		if result.name == name && strings.Contains(result.data, contains) {
			return
		}
	}
	t.Fatalf("Memory %s ToolResult missing %q: %#v", name, contains, results)
}

func assertMemoryGoldenLastResult(t *testing.T, results []memoryGoldenResult, name, contains string) {
	t.Helper()
	if len(results) == 0 {
		t.Fatalf("Memory %s ToolResult missing %q: %#v", name, contains, results)
	}
	last := results[len(results)-1]
	if last.name != name || !strings.Contains(last.data, contains) {
		t.Fatalf("last Memory ToolResult = %#v, want %s containing %q", last, name, contains)
	}
}

func assertMemoryGoldenDiagnosticsContainNoSecrets(t *testing.T, storeDirs []string, credentials map[string]string) {
	t.Helper()
	for _, storeDir := range storeDirs {
		for _, name := range []string{"runtime.jsonl", "runtime.jsonl.1"} {
			data, err := os.ReadFile(filepath.Join(storeDir, "logs", name))
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				t.Fatal(err)
			}
			text := string(data)
			if strings.Contains(text, memoryGoldenPrivate) || strings.Contains(text, memoryGoldenShared) {
				t.Fatalf("runtime diagnostics leaked raw Memory text")
			}
			for _, credential := range credentials {
				if credential != "" && strings.Contains(text, credential) {
					t.Fatalf("runtime diagnostics leaked an issuer credential")
				}
			}
		}
	}
}

func writeMemoryGoldenJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runMemoryGoldenCommand(t *testing.T, ctx context.Context, dir, name string, arguments ...string) {
	t.Helper()
	_ = memoryGoldenCommandOutput(t, ctx, dir, name, arguments...)
}

func memoryGoldenCommandOutput(t *testing.T, ctx context.Context, dir, name string, arguments ...string) []byte {
	t.Helper()
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
	return output
}

type memoryGoldenAction struct {
	name      string
	arguments string
}

type memoryGoldenScenario struct {
	name    string
	actions []memoryGoldenAction
}

type memoryGoldenProvider struct {
	*gatewayTestHTTPServer
	mu       sync.Mutex
	scenario memoryGoldenScenario
	calls    int
	errors   []string
}

func newMemoryGoldenProvider(t *testing.T) *memoryGoldenProvider {
	t.Helper()
	provider := &memoryGoldenProvider{}
	provider.gatewayTestHTTPServer = newGatewayTestHTTPServer(http.HandlerFunc(provider.handle))
	t.Cleanup(provider.Close)
	return provider
}

func (p *memoryGoldenProvider) Begin(scenario memoryGoldenScenario) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scenario = scenario
	p.calls = 0
	p.errors = nil
}

func (p *memoryGoldenProvider) handle(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/chat/completions" {
		http.NotFound(w, request)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	call := p.calls
	p.calls++
	scenario := p.scenario
	p.checkTools(payload)
	p.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	if call < len(scenario.actions) {
		action := scenario.actions[call]
		writePluginSystemE2ESSE(w, map[string]any{
			"id": "memory-golden-" + scenario.name, "object": "chat.completion.chunk", "model": "memory-golden",
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"index": 0, "id": fmt.Sprintf("memory_%s_%d", scenario.name, call), "type": "function",
						"function": map[string]any{"name": action.name, "arguments": action.arguments},
					}},
				},
				"finish_reason": "tool_calls",
			}},
		})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	writePluginSystemE2ESSE(w, map[string]any{
		"id": "memory-golden-final-" + scenario.name, "object": "chat.completion.chunk", "model": "memory-golden",
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": "memory-golden-ok"}, "finish_reason": "stop",
		}},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *memoryGoldenProvider) checkTools(payload map[string]any) {
	counts := map[string]int{}
	for _, raw := range anySliceFromFinalToolValue(payload["tools"]) {
		entry, _ := raw.(map[string]any)
		function, _ := entry["function"].(map[string]any)
		name, _ := function["name"].(string)
		if name == memorytool.RememberToolName || name == memorytool.RecallToolName {
			counts[name]++
			parameters, _ := function["parameters"].(map[string]any)
			properties, _ := parameters["properties"].(map[string]any)
			want := "text"
			if name == memorytool.RecallToolName {
				want = "query"
			}
			if len(properties) != 1 || properties[want] == nil {
				p.errors = append(p.errors, fmt.Sprintf("%s properties=%v", name, properties))
			}
		}
	}
	if counts[memorytool.RememberToolName] != 1 || counts[memorytool.RecallToolName] != 1 {
		p.errors = append(p.errors, fmt.Sprintf("Memory tool counts=%v", counts))
	}
}

func (p *memoryGoldenProvider) AssertComplete(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != len(p.scenario.actions)+1 || len(p.errors) != 0 {
		t.Fatalf("Memory provider scenario %q calls=%d errors=%v", p.scenario.name, p.calls, p.errors)
	}
}
