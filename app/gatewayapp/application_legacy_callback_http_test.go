package gatewayapp_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationCallbackRetirementHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	store, cwd := filepath.Join(root, "store"), t.TempDir()
	provider := &applicationHTTPModel{toolName: "js_exec", toolArgs: `{"code":"return await tools.call('LegacyLookup', {});"}`}
	host := startApplicationHTTPHost(t, store, cwd, provider)
	defer func() { host.close(t) }()
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	profile := applicationHTTPProfile()
	definitions := []sessionprofile.ToolDefinition{{Name: "LegacyLookup", Description: "Retired lookup", InputSchema: map[string]any{"type": "object"}, ResultFormat: "content-v1"}}
	legacy := profile
	legacy.Tools = definitions
	if _, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "reject-nonempty"}, Profile: &legacy}); errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatalf("nonempty profile catalog: %v", err)
	}

	rawRequest := func(method, path, operationID, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, host.server.URL+"/api/control/v1"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+fixtureHostToken("owner"))
		req.Header.Set("Content-Type", "application/json")
		if operationID != "" {
			req.Header.Set("Idempotency-Key", operationID)
		}
		resp, err := host.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, raw
	}

	// Exercise the same explicit JSON array sent by Bot. The Go profile encoder
	// omits empty catalogs, so a typed round trip alone would miss this case.
	status, raw := rawRequest(http.MethodPost, "/sessions", "create-empty", `{"operation_id":"create-empty","profile":{"version":"bot/v1","instructions":"fixture","model":"custom/gpt-4.1","execution":"tools-only","tools_version":"bot-empty","tools":[]}}`)
	var created appserver.CommandResult
	if err := wirev1.Unmarshal(raw, &created); status != http.StatusOK || err != nil || created.SessionID == "" {
		t.Fatalf("explicit empty create: HTTP %d, %s, %v", status, raw, err)
	}
	sid := created.SessionID
	binding, err := client.SessionProfile(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	legacy = binding.Profile
	legacy.Tools = definitions
	binding.Profile = legacy
	host.close(t)
	seedLegacyCallbackRows(t, store, binding)
	host = startApplicationHTTPHost(t, store, cwd, provider)
	client = host.app(fixtureHostToken("owner"))
	foreign := host.app(fixtureHostToken("foreign-owner"))
	if _, err := foreign.SessionToolCalls(ctx, sid); errorcode.CodeOf(err) != errorcode.NotFound && errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("foreign receipt read: %v", err)
	}
	calls, err := client.SessionToolCalls(ctx, sid)
	if err != nil || len(calls) != 3 {
		t.Fatalf("historical receipts: %+v, %v", calls, err)
	}
	for i, state := range []string{"cancelled", "unknown", "completed"} {
		call := calls[i]
		if call.State != state || call.ID != "original-"+state || call.CallID != "provider-reused" || call.ConfigurationRevision != 1 || call.ToolsVersion != legacy.ToolsVersion {
			t.Fatalf("settlement changed original receipt: %+v", call)
		}
		single, err := client.SessionToolCall(ctx, sid, call.ID)
		if err != nil || !reflect.DeepEqual(single, call) {
			t.Fatalf("receipt at original address: %+v, %v", single, err)
		}
	}
	if result := calls[2].Result; result == nil || result.Outcome != "succeeded" || result.StructuredContent["score"] != json.Number("9007199254740991") {
		t.Fatalf("historical result lost exact content: %+v", result)
	}
	base := "/sessions/" + sid + "/tool-calls"
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, base + "?wait=true"},
		{http.MethodPost, base + "/original-cancelled/claim"},
		{http.MethodPost, base + "/original-unknown/result"},
	} {
		if status, _ := rawRequest(request.method, request.path, "", "{}"); status < http.StatusBadRequest {
			t.Fatalf("retired route %s returned %d", request.path, status)
		}
	}

	// Until the upgrade patch commits, readback shows the historical catalog
	// while model declarations and Code Mode dispatch use current tools.
	if _, err = client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sid, OperationID: "probe-retired-tool"}, Input: "Try the old tool."}); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, sid)
	if provider.requestCount() != 2 {
		t.Fatalf("retired tool kept the Turn from reaching a model continuation: %d requests", provider.requestCount())
	}
	_, tools := applicationHTTPPrefix(t, provider.requestAt(0))
	declared, err := json.Marshal(tools)
	if err != nil || strings.Contains(string(declared), "LegacyLookup") {
		t.Fatalf("legacy declaration reached model: %s (%v)", declared, err)
	}
	messages := backgroundModelMessages(t, provider.requestAt(1))
	if !strings.Contains(string(messages[len(messages)-1]), "not_found") {
		t.Fatalf("retired Code Mode call did not return an unavailable-tool error: %s", messages[len(messages)-1])
	}
	if after, err := client.SessionToolCalls(ctx, sid); err != nil || !reflect.DeepEqual(after, calls) {
		t.Fatalf("a request dispatched a retired callback: %+v, %v", after, err)
	}

	rejected, err := client.UpdateSessionConfiguration(ctx, appserver.UpdateSessionConfigurationRequest{
		WriteBase: appserver.WriteBase{OperationID: "reject-new-catalog", SessionID: sid}, ExpectedConfigurationRevision: 1,
		Patch: sessionprofile.ConfigurationPatch{Tools: &definitions},
	})
	if errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatalf("nonempty patch: %+v, %v", rejected, err)
	}

	empty := []sessionprofile.ToolDefinition{}
	version := "bot-native-mcp"
	services := []sessionprofile.MCPServer{reviewerFixtureMCP(cwd)}
	clear := appserver.UpdateSessionConfigurationRequest{
		WriteBase: appserver.WriteBase{OperationID: "clear-callbacks", SessionID: sid}, ExpectedConfigurationRevision: 1,
		Patch: sessionprofile.ConfigurationPatch{Tools: &empty, ToolsVersion: &version, MCPServers: &services},
	}
	committed, err := client.UpdateSessionConfiguration(ctx, clear)
	if err != nil || committed.Configuration == nil || committed.Configuration.Revision != 2 ||
		committed.Configuration.Profile.ToolsVersion != version || len(committed.Configuration.Profile.Tools) != 0 ||
		!reflect.DeepEqual(services, committed.Configuration.Profile.MCPServers) {
		t.Fatalf("Bot upgrade patch: %+v, %v", committed, err)
	}
	retry, err := client.UpdateSessionConfiguration(ctx, clear)
	if err != nil || !reflect.DeepEqual(committed, retry) {
		t.Fatalf("original configuration receipt: %+v, %v", retry, err)
	}
	original, err := client.SessionProfile(ctx, sid)
	if err != nil || !reflect.DeepEqual(binding, original) {
		t.Fatalf("creation evidence changed during clear: %+v, %v", original, err)
	}
}

func seedLegacyCallbackRows(t *testing.T, store string, binding sessionprofile.Binding) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(store, "control", "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	marshal := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	sid := binding.SessionID
	exec(`UPDATE session_profiles SET body=? WHERE session=?`, marshal(binding), sid)
	exec(`UPDATE session_configurations SET body=? WHERE session=? AND revision=1`, marshal(sessionprofile.Configuration{SessionID: sid, Revision: 1, Profile: binding.Profile}), sid)
	for i, state := range []string{"pending", "claimed", "completed"} {
		call := sessionprofile.Call{
			ID: "original-" + []string{"cancelled", "unknown", "completed"}[i],
			CallContext: sessionprofile.CallContext{Scope: binding.Scope, SessionID: sid, TurnID: "turn-" + state,
				ItemID: "item", CallID: "provider-reused", ToolsVersion: binding.Profile.ToolsVersion, ConfigurationRevision: 1,
				Source: sessionprofile.Source{Kind: "user", OperationID: "prompt-" + state}},
			Name: "LegacyLookup", Arguments: json.RawMessage(`{"original":true}`), State: "pending",
		}
		var result []byte
		if state == "completed" {
			result = []byte(`{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"text","text":"historical payload"}],"structuredContent":{"score":9007199254740991},"turn_complete":true}`)
		}
		body := marshal(call)
		digest := sha256.Sum256(body)
		exec(`INSERT INTO client_tool_calls VALUES (?,?,?,?,?,?,?,?,?)`, call.PrincipalID, sid, call.ID, call.TurnID, call.ItemID, hex.EncodeToString(digest[:]), body, state, result)
	}
}
