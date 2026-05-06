package local

import (
	"testing"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
)

func TestPermissionGrantStoreAppliesPathRulesAndNetwork(t *testing.T) {
	store := newPermissionGrantStore()
	store.add(permissionGrantRequest{
		ReadRoots:      []string{"/readonly", "/upgrade"},
		WriteRoots:     []string{"/writable", "/upgrade"},
		NetworkEnabled: true,
	})

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
}
