package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

// The retired tools' canonical messages remain model context. This uses stored
// historical results, then compares the complete provider request across restart.
func TestSessionRecoveryNextModelRequestUsesCanonicalToolResultsWithoutSpool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root, workspace := t.TempDir(), t.TempDir()
	requests := make(chan map[string]any, 8)
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		requests <- payload
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "text/event-stream")
		writePluginSystemE2ESSE(recorder, map[string]any{
			"id": "retirement-test", "object": "chat.completion.chunk", "model": "retirement-test",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": "history preserved"}, "finish_reason": "stop"}},
		})
		_, _ = fmt.Fprint(recorder, "data: [DONE]\n\n")
		return recorder.Result(), nil
	})}
	// The native acceptance job exercises the actual default backend. Other
	// environments can exercise Store upgrades without a platform sandbox.
	backend := "host"
	native := os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") == "1"
	if native {
		backend = "auto"
	}
	cfg := Config{
		AppName: "caelis", UserID: "retirement-test", StoreDir: root, WorkspaceKey: "workspace", WorkspaceCWD: workspace, SkillDirs: []string{},
		Sandbox:                   SandboxConfig{RequestedType: backend},
		ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return client, nil },
		Model:                     ModelConfig{Provider: "openai-compatible", API: model.APIOpenAICompatible, Model: "retirement-test", BaseURL: "http://retirement.test", HTTPClient: client, Token: "fixture-token", AuthType: model.AuthBearerToken, ContextWindowTokens: 128000, MaxOutputTok: 4096},
		MemoryBindingRef:          "missing-retired-binding",
		MemoryBindingSelector: func(context.Context, MemoryBindingSelectionContext) (memorybinding.BindingRef, error) {
			t.Error("retired binding selector invoked")
			return "", fmt.Errorf("retired")
		},
		MemoryLabelSelector: func(context.Context, MemoryLabelSelectionContext) ([]string, error) {
			t.Error("retired label selector invoked")
			return nil, fmt.Errorf("retired")
		},
	}
	// An unreadable-to-Memory database and invalid authority cannot block startup.
	memoryDir := filepath.Join(root, "memory", "appliance")
	if err := os.MkdirAll(memoryDir, 0700); err != nil {
		t.Fatal(err)
	}
	preserved := map[string][]byte{
		filepath.Join(memoryDir, "memory.db"):        []byte("retained database fixture - must never be opened or migrated"),
		filepath.Join(memoryDir, "management.token"): []byte("retained credential"),
		filepath.Join(workspace, "MEMORY.md"):        []byte("notebook identity"),
	}
	for path, data := range preserved {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(root, "config.json")
	legacy := []byte(`{"schema_version":2,"memory":{"enabled":true,"bindings":[{"binding_ref":"invalid"}],"unknown_legacy_field":{"keep":true}},"agent_bindings":{"bindings":[{"handle":"steward","profile_id":"missing","effort":"unknown"},{"handle":"memory-verifier","profile_id":"missing"}]}}`)
	if err := os.WriteFile(configPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	stack, err := newGatewayAppTestStack(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	select {
	case <-requests:
		t.Fatal("startup made a model request")
	default:
	}
	status, err := stack.AgentBindings().AgentBindingStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range status.Handles {
		if agentbinding.IsRetired(item.Definition.Handle) {
			t.Fatalf("retired handle advertised: %#v", item)
		}
	}
	for _, name := range []string{"steward", "memory-verifier"} {
		if _, ok := systemManagedAgentSpecFor(name); ok {
			t.Fatalf("retired system scene %s is registered", name)
		}
	}
	active, err := startGatewayAppTestSession(ctx, stack, "retained-history")
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately preserve even an old unsupported pin: it no longer grants authority.
	pin := map[string]any{"version": float64(1), "consistency_token": "retained-token", "view_ref": "old-view"}
	if _, err := stack.composition.sessions.UpdateState(ctx, session.UpdateStateRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Update: func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next["gateway.memory.binding.v1"] = pin
		next["gateway.current_session_mode"] = "manual"
		next["gateway.current_sandbox_mode"] = "full-access"
		return next, nil
	}}); err != nil {
		t.Fatal(err)
	}
	appendGatewayAppEvent(t, stack, active.SessionRef, gatewayAppUserEvent("remember the review language"))
	for i, name := range []string{"Remember", "Recall"} {
		id := fmt.Sprintf("historical-%d", i)
		args := map[string]any{"text": "review in Chinese"}
		if name == "Recall" {
			args = map[string]any{"query": "review language"}
		}
		argsJSON, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		call := model.NewMessage(model.RoleAssistant, model.NewToolUsePart(id, name, argsJSON))
		appendGatewayAppEvent(t, stack, active.SessionRef, &session.Event{Type: session.EventTypeToolCall, Visibility: session.VisibilityCanonical, Message: &call, Tool: &session.EventTool{ID: id, Name: name, Input: args}})
		result := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart(id, name, map[string]any{"text": "review in Chinese", "receipt_id": "retained-receipt"}, false))
		appendGatewayAppEvent(t, stack, active.SessionRef, &session.Event{Type: session.EventTypeToolResult, Visibility: session.VisibilityCanonical, Message: &result})
	}
	historical, err := stack.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, active.SessionID, "continue", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	before := <-requests
	assertRetiredMemoryAbsentFromRequest(t, before)
	unsupported, err := startGatewayAppTestSession(ctx, stack, "retired-inline-history")
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(controlStreamSpoolRoot(root)); err != nil {
		t.Fatal(err)
	}
	// Two SDK Sessions with a conflicting key must not block new or resumable Sessions.
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	for _, directory := range []string{workspace, t.TempDir()} {
		if _, err := store.StartSession(ctx, session.StartSessionRequest{
			AppName: cfg.AppName, UserID: cfg.UserID, Workspace: session.WorkspaceRef{Key: "ambiguous", CWD: directory},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen a mixed-generation store through the normal Host entry point.
	upgradeDocument := func(id string) string {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "*", "rollout-*-"+id+".json"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("Session document for %s = %v, %v", id, matches, err)
		}
		return matches[0]
	}
	replaceFixtureField := func(path, field, value string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		raw[field] = json.RawMessage(value)
		data, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	replaceFixtureField(upgradeDocument(active.SessionID), "version", "1")
	replaceFixtureField(upgradeDocument(unsupported.SessionID), "events", `[{"type":"user","id":"embedded"}]`)
	replaceFixtureField(configPath, "sandbox", `{"requested_type":"landlock","helper_path":"retired-helper"}`)
	cfg.Model = ModelConfig{}
	if native {
		cfg.Sandbox = SandboxConfig{}
	}
	stack, err = NewLocalStack(cfg)
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadAppConfig(root)
	if err != nil || config.Sandbox.RequestedType != "auto" || config.Sandbox.HelperPath != "" {
		t.Fatalf("upgraded Host sandbox = %#v, %v", config.Sandbox, err)
	}
	fresh, err := startGatewayAppTestSession(ctx, stack, "fresh-after-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runHeadlessOnceForGatewayAppTest(ctx, stack, fresh, fresh.SessionID, "start fresh", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	freshRequest := <-requests
	freshMessages, err := json.Marshal(freshRequest["messages"])
	if err != nil || !strings.Contains(string(freshMessages), "start fresh") || strings.Contains(string(freshMessages), "retained-receipt") {
		t.Fatalf("fresh model context = %s, %v", freshMessages, err)
	}
	if _, err := stack.composition.sessions.Session(ctx, unsupported.SessionRef); !errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("inline history error = %v", err)
	}
	if _, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, active.SessionID, "continue after restart", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	after := <-requests
	assertRetiredMemoryAbsentFromRequest(t, after)
	want := append(append([]any(nil), before["messages"].([]any)...),
		map[string]any{"role": "assistant", "content": "history preserved"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "continue after restart"}}},
	)
	if !reflect.DeepEqual(after["messages"], want) {
		gotJSON, _ := json.Marshal(after["messages"])
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("rebuilt model context differs:\ngot=%s\nwant=%s", gotJSON, wantJSON)
	}
	events, err := stack.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < len(historical) || !reflect.DeepEqual(events[:len(historical)], historical) {
		t.Fatal("historical events changed")
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state["gateway.memory.binding.v1"], pin) {
		t.Fatalf("retained pin changed: %#v", state)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requests:
		t.Fatal("unexpected background model request")
	default:
	}
	for path, data := range preserved {
		got, err := os.ReadFile(path)
		if err != nil || !reflect.DeepEqual(got, data) {
			t.Fatalf("retained data changed: %s, %v", path, err)
		}
	}
	entries, err := os.ReadDir(memoryDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("Memory initialized files: %v, %v", entries, err)
	}
	doc, err := newAppConfigStore(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc.Memory), "unknown_legacy_field") {
		t.Fatal("opaque Memory configuration lost")
	}
}

func assertRetiredMemoryAbsentFromRequest(t *testing.T, payload map[string]any) {
	t.Helper()
	tools, _ := payload["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("expected ordinary tools")
	}
	for _, raw := range tools {
		entry := raw.(map[string]any)
		fn := entry["function"].(map[string]any)
		if fn["name"] == "Remember" || fn["name"] == "Recall" {
			t.Fatalf("retired tool advertised: %v", fn["name"])
		}
	}
}
