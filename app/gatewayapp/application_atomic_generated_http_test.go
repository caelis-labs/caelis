package gatewayapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
)

// Exercise generated public DTOs against the real HTTP Host. Comparing the
// generator with its own schema cannot detect fields omitted from both.
func TestApplicationAtomicGeneratedWireHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	selectedDir := filepath.Join(root, "selected-skills")
	selectedRoot := filepath.Join(root, "single-skill")
	writeAtomicSkill(t, filepath.Join(selectedDir, "child"), "child-skill", "CHILD_BODY")
	writeAtomicSkill(t, selectedRoot, "single-skill", "SINGLE_BODY")
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, &atomicCapabilityProvider{})
	defer host.close(t)
	hostStatus, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "generated-wire-model", ExpectedRevision: &hostStatus.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"},
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = registerApplicationHTTP(t, ctx, host, "generated-wire", filepath.Join(root, "app.credential"))
	credential, err := os.ReadFile(filepath.Join(root, "app.credential"))
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, operationID string, input, output any) {
		t.Helper()
		var body io.Reader
		if input != nil {
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(raw)
		}
		request, err := http.NewRequestWithContext(ctx, method, host.server.URL+wirev1.APIPrefix+path, body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+string(credential))
		if input != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if operationID != "" {
			request.Header.Set("Idempotency-Key", operationID)
		}
		response, err := host.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s %s = HTTP %d: %s", method, path, response.StatusCode, raw)
		}
		if err := json.Unmarshal(raw, output); err != nil {
			t.Fatalf("decode %s %s: %v: %s", method, path, err, raw)
		}
	}

	command, workDir := os.Args[0], root
	services := []generated.ApplicationMCPServer{{Name: "documents", Transport: "stdio", Command: &command, WorkDir: &workDir}}
	dirs, roots := []string{selectedDir}, []string{selectedRoot}
	profile := generated.ApplicationProfile{
		Version: "generated-wire/1", Instructions: "Use the selected abilities.", Model: "openai-compatible/gpt-4.1",
		ToolsVersion: "generated-wire-tools/1", Execution: "tools-only", Inherit: generated.ApplicationInheritance{},
		NativeTools: []string{}, McpServers: services, SkillDirs: dirs, SkillRoots: roots,
	}
	createID := "generated-wire-create"
	create := generated.CreateApplicationSessionRequest{OperationId: &createID, Profile: profile}
	createdJSON, err := json.Marshal(create)
	if err != nil || !bytes.Contains(createdJSON, []byte(`"native_tools":[]`)) {
		t.Fatalf("generated creation lost explicit empty native tool selection: %s %v", createdJSON, err)
	}
	var created generated.CommandResult
	send(http.MethodPost, "/application/sessions", createID, create, &created)
	if string(created.Outcome) != "committed" || created.SessionId == nil || *created.SessionId == "" {
		t.Fatalf("generated creation = %+v", created)
	}
	sessionPath := "/application/sessions/" + url.PathEscape(*created.SessionId)
	checkProfile := func(got generated.ApplicationProfile) {
		t.Helper()
		if len(got.McpServers) != 1 || got.McpServers[0].Name != "documents" || !slices.Equal(got.SkillDirs, dirs) || !slices.Equal(got.SkillRoots, roots) || got.NativeTools == nil || len(got.NativeTools) != 0 {
			t.Fatalf("generated profile lost selections: %+v", got)
		}
	}
	var binding generated.ApplicationBinding
	send(http.MethodGet, sessionPath, "", nil, &binding)
	checkProfile(binding.Profile)
	var configuration generated.ApplicationConfiguration
	send(http.MethodGet, sessionPath+"/configuration", "", nil, &configuration)
	if configuration.Revision != "1" {
		t.Fatalf("configuration revision = %q", configuration.Revision)
	}
	checkProfile(configuration.Profile)
	checkStatus := func(revision string, wantSelections bool) {
		t.Helper()
		var status generated.ApplicationMCPStatus
		send(http.MethodGet, sessionPath+"/mcp-status", "", nil, &status)
		if string(status.ConfigurationRevision) != revision {
			t.Fatalf("status revision = %q, want %s", status.ConfigurationRevision, revision)
		}
		if !wantSelections {
			if len(status.Servers) != 0 || len(status.Skills) != 0 {
				t.Fatalf("cleared selection remained in status: %+v", status)
			}
			return
		}
		if len(status.Servers) != 1 || status.Servers[0].Name != "documents" || status.Servers[0].Status != "inactive" || len(status.Skills) != 2 {
			t.Fatalf("generated ability status lost selections: %+v", status)
		}
		paths := []string{status.Skills[0].Path, status.Skills[1].Path}
		if !slices.Contains(paths, selectedDir) || !slices.Contains(paths, selectedRoot) {
			t.Fatalf("generated Skill status lost selected roots: %+v", status.Skills)
		}
	}
	checkStatus("1", true)

	clearID := "generated-wire-clear"
	clear := generated.UpdateApplicationConfigurationRequest{
		OperationId: clearID, ExpectedConfigurationRevision: "1",
		Patch: generated.ApplicationConfigurationPatch{
			McpServers: []generated.ApplicationMCPServer{}, SkillDirs: []string{}, SkillRoots: []string{},
			NativeTools: []string{}, Tools: []generated.ApplicationToolDefinition{},
		},
	}
	clearJSON, err := json.Marshal(clear)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"mcp_servers", "skill_dirs", "skill_roots", "native_tools", "tools"} {
		if !strings.Contains(string(clearJSON), `"`+field+`":[]`) {
			t.Fatalf("generated CAS omitted explicit %s clear: %s", field, clearJSON)
		}
	}
	var cleared generated.ApplicationConfiguration
	send(http.MethodPost, sessionPath+"/configuration", clearID, clear, &cleared)
	if cleared.Revision != "2" || len(cleared.Profile.McpServers)+len(cleared.Profile.SkillDirs)+len(cleared.Profile.SkillRoots) != 0 {
		t.Fatalf("generated CAS did not clear abilities: %+v", cleared)
	}
	checkStatus("2", false)

	restoreID := "generated-wire-restore"
	restore := generated.UpdateApplicationConfigurationRequest{
		OperationId: restoreID, ExpectedConfigurationRevision: "2",
		Patch: generated.ApplicationConfigurationPatch{McpServers: services, SkillDirs: dirs, SkillRoots: roots},
	}
	var restored generated.ApplicationConfiguration
	send(http.MethodPost, sessionPath+"/configuration", restoreID, restore, &restored)
	if restored.Revision != "3" {
		t.Fatalf("restored revision = %q", restored.Revision)
	}
	checkProfile(restored.Profile)
	checkStatus("3", true)
	// A configuration update never rewrites the immutable creation binding.
	send(http.MethodGet, sessionPath, "", nil, &binding)
	checkProfile(binding.Profile)
}
