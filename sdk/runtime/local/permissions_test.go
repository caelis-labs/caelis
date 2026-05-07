package local

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
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

	got := store.applyToConstraints(sdksandbox.Constraints{
		Network: sdksandbox.NetworkDisabled,
		PathRules: []sdksandbox.PathRule{
			{Path: "/workspace", Access: sdksandbox.PathAccessReadWrite},
			{Path: "/readonly", Access: sdksandbox.PathAccessReadOnly},
		},
	})
	if got.Network != sdksandbox.NetworkEnabled {
		t.Fatalf("Network = %q, want enabled", got.Network)
	}
	want := map[string]sdksandbox.PathAccess{
		"/workspace": sdksandbox.PathAccessReadWrite,
		"/readonly":  sdksandbox.PathAccessReadOnly,
		"/writable":  sdksandbox.PathAccessReadWrite,
		"/upgrade":   sdksandbox.PathAccessReadWrite,
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

func TestRequestPermissionsToolReturnsStandardGrantPayload(t *testing.T) {
	store := newPermissionGrantStore()
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	tool := requestPermissionsTool{
		session:    sdksession.Session{CWD: "/workspace"},
		sessionRef: sdksession.SessionRef{SessionID: "sess-1"},
		mode:       "manual",
		runID:      "run-1",
		turnID:     "turn-1",
		now:        func() time.Time { return now },
		approval: approvalRequesterFunc(func(_ context.Context, req sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error) {
			if got := fmt.Sprint(req.Metadata["approval_reason"]); !strings.Contains(got, "need deps") {
				t.Fatalf("approval reason metadata = %q, want need deps", got)
			}
			return sdkruntime.ApprovalResponse{Approved: true}, nil
		}),
		grants: store,
	}
	result, err := tool.Call(context.Background(), sdktool.Call{
		ID:    "perm-1",
		Name:  requestPermissionsToolName,
		Input: []byte(`{"reason":"need deps","permissions":{"file_system":{"read":["docs"],"write":["build"]},"network":{"enabled":true}}}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Call() IsError = true: %#v", result.Meta)
	}
	grant, ok := result.Meta["grant"].(map[string]any)
	if !ok {
		t.Fatalf("grant payload = %#v, want map", result.Meta["grant"])
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

func TestRuntimePermissionGrantsAreSessionScoped(t *testing.T) {
	runtime := &Runtime{}
	first := runtime.permissionGrantStoreForSession(sdksession.SessionRef{SessionID: "sess-1"})
	second := runtime.permissionGrantStoreForSession(sdksession.SessionRef{SessionID: "sess-1"})
	other := runtime.permissionGrantStoreForSession(sdksession.SessionRef{SessionID: "sess-2"})
	if first != second {
		t.Fatal("permissionGrantStoreForSession returned different stores for the same session")
	}
	if first == other {
		t.Fatal("permissionGrantStoreForSession reused a store across sessions")
	}
	first.add(permissionGrantRequest{NetworkEnabled: true, Reason: "network"}, permissionGrantMetadata{})
	if got := runtime.PermissionGrantSnapshot(sdksession.SessionRef{SessionID: "sess-1"}); got.Count != 1 || !got.NetworkGranted {
		t.Fatalf("sess-1 snapshot = %+v, want one network grant", got)
	}
	if got := runtime.PermissionGrantSnapshot(sdksession.SessionRef{SessionID: "sess-2"}); got.Count != 0 || got.NetworkGranted {
		t.Fatalf("sess-2 snapshot = %+v, want no grants", got)
	}
}
