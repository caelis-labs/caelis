package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestPermissionGrantStoreAppliesPathRulesAndNetwork(t *testing.T) {
	store := newPermissionGrantStore()
	store.add(permissionGrantRequest{
		ReadRoots:      []string{"/readonly", "/upgrade"},
		WriteRoots:     []string{"/writable", "/upgrade"},
		NetworkEnabled: true,
	}, permissionGrantMetadata{
		Mode:      "manual",
		RunID:     "run-1",
		TurnID:    "turn-1",
		CreatedAt: time.Unix(123, 0),
	})
	store.add(permissionGrantRequest{
		ReadRoots: []string{"/readonly", "", "/readonly"},
	}, permissionGrantMetadata{})

	got := store.applyToConstraints(sandbox.Constraints{
		Network: sandbox.NetworkDisabled,
		PathRules: []sandbox.PathRule{
			{Path: "/workspace", Access: sandbox.PathAccessReadWrite},
			{Path: "/readonly", Access: sandbox.PathAccessReadOnly},
		},
	})
	if got.Network != sandbox.NetworkEnabled {
		t.Fatalf("Network = %q, want enabled", got.Network)
	}
	want := map[string]sandbox.PathAccess{
		"/workspace": sandbox.PathAccessReadWrite,
		"/readonly":  sandbox.PathAccessReadOnly,
		"/writable":  sandbox.PathAccessReadWrite,
		"/upgrade":   sandbox.PathAccessReadWrite,
	}
	if len(got.PathRules) != len(want) {
		t.Fatalf("PathRules len = %d, want %d: %#v", len(got.PathRules), len(want), got.PathRules)
	}
	for _, rule := range got.PathRules {
		access, ok := want[rule.Path]
		if !ok {
			t.Fatalf("unexpected path rule: %#v", rule)
		}
		if rule.Access != access {
			t.Fatalf("PathRules[%q] access = %q, want %q", rule.Path, rule.Access, access)
		}
	}
	snapshot := store.snapshot()
	if snapshot.Count != 2 || !snapshot.NetworkGranted || snapshot.ReadRootCount != 2 || snapshot.WriteRootCount != 2 {
		t.Fatalf("snapshot = %+v, want two grants with deduped read/write roots and network", snapshot)
	}
}

func TestRequestPermissionsSchemaDisallowsUnknownRootProperties(t *testing.T) {
	t.Parallel()

	def := (requestPermissionsTool{}).Definition()
	if got := def.InputSchema["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties = %#v, want false", got)
	}
}

func TestRequestPermissionsToolReturnsStandardGrantPayload(t *testing.T) {
	store := newPermissionGrantStore()
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	workspace := t.TempDir()
	for _, dir := range []string{"docs", "build"} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", dir, err)
		}
	}
	permissionsTool := requestPermissionsTool{
		session:    session.Session{CWD: workspace},
		sessionRef: session.SessionRef{SessionID: "sess-1"},
		mode:       "manual",
		runID:      "run-1",
		turnID:     "turn-1",
		now:        func() time.Time { return now },
		approval: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			if got := fmt.Sprint(req.Metadata["approval_reason"]); !strings.Contains(got, "need deps") {
				t.Fatalf("approval reason metadata = %q, want need deps", got)
			}
			return agent.ApprovalResponse{Approved: true}, nil
		}),
		grants: store,
	}
	result, err := permissionsTool.Call(context.Background(), tool.Call{
		ID:    "perm-1",
		Name:  requestPermissionsToolName,
		Input: []byte(`{"reason":"need deps","read":["docs"],"write":["build"],"network":true}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Call() IsError = true: %#v", result.Meta)
	}
	grant, ok := permissionToolResultMeta(t, result)["grant"].(map[string]any)
	if !ok {
		t.Fatalf("grant metadata = %#v, want map", permissionToolResultMeta(t, result)["grant"])
	}
	for key, want := range map[string]string{
		"reason":     "need deps",
		"mode":       "manual",
		"run_id":     "run-1",
		"turn_id":    "turn-1",
		"created_at": now.Format(time.RFC3339Nano),
	} {
		if got := fmt.Sprint(grant[key]); got != want {
			t.Fatalf("grant[%s] = %q, want %q", key, got, want)
		}
	}
	if snapshot := store.snapshot(); snapshot.Count != 1 || !snapshot.NetworkGranted || snapshot.ReadRootCount != 1 || snapshot.WriteRootCount != 1 {
		t.Fatalf("snapshot = %+v, want recorded grant", snapshot)
	}
}

func permissionToolResultPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	return payload
}

func permissionToolResultMeta(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	caelis, _ := result.Metadata["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	if toolMeta == nil {
		t.Fatalf("result.Metadata caelis.runtime.tool = %#v", result.Metadata)
	}
	return toolMeta
}

func TestRequestPermissionsToolRejectsMissingFilesystemPath(t *testing.T) {
	workspace := t.TempDir()
	permissionsTool := requestPermissionsTool{
		session:    session.Session{CWD: workspace},
		sessionRef: session.SessionRef{SessionID: "sess-1"},
		approval: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			t.Fatal("approval requester should not be called for missing filesystem paths")
			return agent.ApprovalResponse{}, nil
		}),
		grants: newPermissionGrantStore(),
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "read",
			input: `{"reason":"need read","read":["missing-read"]}`,
			want:  "request_permissions read path",
		},
		{
			name:  "write",
			input: `{"reason":"need write","write":["missing-write"]}`,
			want:  "request an existing parent directory",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result, err := permissionsTool.Call(context.Background(), tool.Call{
				ID:    "perm-1",
				Name:  requestPermissionsToolName,
				Input: []byte(tt.input),
			})
			if err == nil {
				t.Fatal("Call() error = nil, want missing path error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Call() error = %v, want substring %q", err, tt.want)
			}
			if result.ID != "" || result.Name != "" || len(result.Content) != 0 || len(result.Meta) != 0 || result.IsError {
				t.Fatalf("result = %#v, want zero result on parse error", result)
			}
		})
	}
}

func TestRequestPermissionsToolPersistsGrantInSessionState(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	activeSession, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis-test",
		UserID:  "user",
		Workspace: session.WorkspaceRef{
			Key: "workspace",
			CWD: workspace,
		},
		PreferredSessionID: "sess-grants",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	target := filepath.Join(workspace, ".config", "ghostty", "config")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte("theme=dark\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", target, err)
	}
	store := newPermissionGrantStore()
	permissionsTool := requestPermissionsTool{
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
		sessions:   sessions,
		mode:       "manual",
		runID:      "run-1",
		turnID:     "turn-1",
		approval: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			return agent.ApprovalResponse{Approved: true}, nil
		}),
		grants: store,
	}

	result, err := permissionsTool.Call(ctx, tool.Call{
		ID:    "perm-1",
		Name:  requestPermissionsToolName,
		Input: []byte(fmt.Sprintf(`{"reason":"edit ghostty","write":[%q]}`, target)),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Call() IsError = true: %#v", result.Meta)
	}

	state, err := sessions.SnapshotState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	records := permissionGrantRecordsFromState(state[permissionGrantStateKey])
	if len(records) != 1 {
		t.Fatalf("persisted records len = %d, want 1: %#v", len(records), records)
	}
	if records[0].WriteRoots[0] != target {
		t.Fatalf("WriteRoots = %#v, want %q", records[0].WriteRoots, target)
	}
	if want := filepath.Dir(target); records[0].ShellWriteRoots[0] != want {
		t.Fatalf("ShellWriteRoots = %#v, want %q", records[0].ShellWriteRoots, want)
	}

	hydrated := newPermissionGrantStore()
	hydrated.hydrate(records)
	constraints := hydrated.applyToConstraints(sandbox.Constraints{})
	if !hasSandboxPathRule(constraints.PathRules, filepath.Dir(target), sandbox.PathAccessReadWrite) {
		t.Fatalf("PathRules = %#v, want parent dir shell write root", constraints.PathRules)
	}
}

func TestRuntimePermissionGrantsAreSessionScoped(t *testing.T) {
	runtime := &Runtime{}
	first := runtime.permissionGrantStoreForSession(session.SessionRef{SessionID: "sess-1"})
	second := runtime.permissionGrantStoreForSession(session.SessionRef{SessionID: "sess-1"})
	other := runtime.permissionGrantStoreForSession(session.SessionRef{SessionID: "sess-2"})
	if first != second {
		t.Fatal("permissionGrantStoreForSession returned different stores for the same session")
	}
	if first == other {
		t.Fatal("permissionGrantStoreForSession reused a store across sessions")
	}
	first.add(permissionGrantRequest{NetworkEnabled: true, Reason: "network"}, permissionGrantMetadata{})
	if got := runtime.PermissionGrantSnapshot(session.SessionRef{SessionID: "sess-1"}); got.Count != 1 || !got.NetworkGranted {
		t.Fatalf("sess-1 snapshot = %+v, want one network grant", got)
	}
	if got := runtime.PermissionGrantSnapshot(session.SessionRef{SessionID: "sess-2"}); got.Count != 0 || got.NetworkGranted {
		t.Fatalf("sess-2 snapshot = %+v, want no grants", got)
	}
}

func hasSandboxPathRule(rules []sandbox.PathRule, path string, access sandbox.PathAccess) bool {
	for _, rule := range rules {
		if rule.Path == path && rule.Access == access {
			return true
		}
	}
	return false
}
