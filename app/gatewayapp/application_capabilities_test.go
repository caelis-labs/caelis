package gatewayapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/promptassembly"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestApplicationSkillRootLoadsOnlySelectedMetadataThenBody(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "selected")
	ambient := filepath.Join(root, "ambient")
	for _, path := range []string{selected, ambient} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(selected, "SKILL.md"), []byte("---\nname: selected\ndescription: Selected synthetic skill.\n---\nSELECTED_BODY_MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambient, "SKILL.md"), []byte("---\nname: ambient\ndescription: Ambient synthetic skill.\n---\nAMBIENT_BODY_MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := applicationSkillCatalog(sessionprofile.Profile{SkillRoots: []string{selected}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := promptassembly.SkillMetadata(catalog)
	if !strings.Contains(metadata, "selected") || strings.Contains(metadata, "SELECTED_BODY_MARKER") || strings.Contains(metadata, "ambient") {
		t.Fatalf("root metadata includes unselected Skill or body: %s", metadata)
	}
	meta, status := catalog.Resolve("selected")
	if status != skill.ResolveMatched {
		t.Fatal("selected root was not discovered")
	}
	loaded, err := (skillfs.Loader{}).Load(context.Background(), skill.RefFromMeta(meta))
	if err != nil || !strings.Contains(loaded.Content, "SELECTED_BODY_MARKER") {
		t.Fatalf("on-demand body = %+v, %v", loaded, err)
	}
}

func TestApplicationCommittedMCPRemovalFailsClosedWhenReloadFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	remote := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "revocation-test", Version: "1"}, nil)
	mcpsdk.AddTool(remote, &mcpsdk.Tool{Name: "probe"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{}, nil, nil
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return remote }, nil))
	defer endpoint.Close()
	provider := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), UserID: "owner",
		Model: ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "compact-test", BaseURL: provider.URL, HTTPClient: provider.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: "owner"}
	result, err := stack.ControlClient().CreateSession(ctx, principal, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "create"},
		Profile: &sessionprofile.Profile{Version: "test/1", Model: "ollama/compact-test", ToolsVersion: "tools/1", Execution: "tools-only",
			MCPServers: []sessionprofile.MCPServer{{Name: "revocation_test", Transport: "streamable_http", URL: endpoint.URL}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := stack.composition.authorities.sessionProfiles
	binding, err := store.BindingForSession(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	composition := &runtimeComposition{authorities: stack.composition.authorities, workspace: stack.composition.workspace}
	resolver := &applicationTurnResolver{composition: composition, binding: binding}
	defer resolver.closeCapabilities()
	_, previous, err := resolver.currentCapabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-resolver.mcpManager.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	tools := previous.source.Tools()
	index := slices.IndexFunc(tools, func(one tool.Tool) bool { return one.Definition().Name == "revocation_test__probe" })
	if index < 0 {
		t.Fatal("missing original MCP callable")
	}
	call := tool.Call{Name: "revocation_test__probe", Input: []byte(`{}`)}
	if _, err := tools[index].Call(ctx, call); err != nil {
		t.Fatal(err)
	}
	empty := []sessionprofile.MCPServer{}
	updated, err := stack.sessionProfiles.UpdateSessionConfiguration(ctx, principal, appserver.UpdateSessionConfigurationRequest{
		WriteBase: appserver.WriteBase{OperationID: "remove", SessionID: binding.SessionID}, ExpectedConfigurationRevision: 1,
		Patch: sessionprofile.ConfigurationPatch{MCPServers: &empty},
	})
	if err != nil || updated.Configuration == nil || updated.Configuration.Revision != 2 {
		t.Fatalf("remove = %+v, %v", updated, err)
	}
	// Simulate loss of the Host configuration reader after the durable PATCH.
	composition.authorities.store = nil
	resolver.configurationCommitted(ctx, *updated.Configuration)
	if status := resolver.capabilityStatus(2); status.Warning == "" {
		t.Fatal("reconciliation failure was hidden")
	}
	if _, err := tools[index].Call(ctx, call); errorcode.CodeOf(err) != tool.ErrorCodeStaleRevision {
		t.Fatalf("old MCP authority survived the failed reconciliation: %v", err)
	}
	composition.authorities.store = stack.composition.authorities.store
	resolver.configurationCommitted(ctx, *updated.Configuration)
	if status := resolver.capabilityStatus(2); status.Warning != "" {
		t.Fatalf("reconciled revision retained warning: %s", status.Warning)
	}
}

func TestApplicationSkillCatalogDoesNotSilentlyDropMissingSelectedRoot(t *testing.T) {
	root := t.TempDir()
	healthy := filepath.Join(root, "healthy")
	if err := os.Mkdir(healthy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(healthy, "SKILL.md"), []byte("---\nname: healthy\ndescription: Healthy synthetic skill.\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "selected-but-missing")
	if _, err := applicationSkillCatalog(sessionprofile.Profile{SkillRoots: []string{healthy, missing}}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing selected Skill was silently omitted: %v", err)
	}
}
