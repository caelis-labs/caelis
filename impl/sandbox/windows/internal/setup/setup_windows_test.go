//go:build windows

package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/acl"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
)

func TestAncestorPathsStopBeforeUserProfileRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("user home unavailable: %v", err)
	}
	root := filepath.Join(home, "WorkDir", "demo", "storage")
	ancestors := ancestorPaths(root)
	for _, ancestor := range ancestors {
		if isUserProfileRootOrAbove(ancestor) {
			t.Fatalf("ancestorPaths(%q) included profile root or above: %q", root, ancestor)
		}
	}
	if !containsPathKey(ancestors, filepath.Join(home, "WorkDir", "demo")) {
		t.Fatalf("ancestorPaths(%q) = %#v, want workspace parent below profile root", root, ancestors)
	}
}

func TestRequiredPolicyACLTargetsSkipsAncestorACLTargets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace", "storage")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	capabilitySID := "S-1-15-3-1024-1-2-3-4-5-6-7-8"
	targets := requiredPolicyACLTargets(winpolicy.Policy{
		WriteRoots: []string{root},
		WriteRootCapabilitySIDs: map[string]string{
			pathutil.Key(root): capabilitySID,
		},
		CapabilitySIDs: []string{capabilitySID},
	}, "CaelisSbxOffTest", "CaelisSbxOnTest")

	var foundRootCapability bool
	rootKey := pathutil.Key(root)
	for _, target := range targets {
		isRoot := pathutil.Key(target.Path) == rootKey
		if !isRoot {
			t.Fatalf("requiredPolicyACLTargets(%q) included ancestor/non-root target %q", root, target.Path)
		}
		for _, entry := range target.Entries {
			if isRoot && entry.Principal == capabilitySID {
				foundRootCapability = true
			}
		}
	}
	if !foundRootCapability {
		t.Fatalf("requiredPolicyACLTargets(%q) did not grant capability SID on the write root", root)
	}
}

func TestRequiredPolicyACLTargetsUsesGroupAndRootCapabilityForWriteRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	rootCapabilitySID := "S-1-5-21-1-2-3-4"
	otherCapabilitySID := "S-1-5-21-5-6-7-8"
	targets := requiredPolicyACLTargets(winpolicy.Policy{
		WriteRoots:     []string{root},
		CapabilitySIDs: []string{rootCapabilitySID, otherCapabilitySID},
		WriteRootCapabilitySIDs: map[string]string{
			pathutil.Normalize(root): rootCapabilitySID,
		},
	}, "CaelisSbxOffTest", "CaelisSbxOnTest")

	entries := entriesForPath(t, targets, root, acl.Grant)
	principals := entryPrincipals(entries)
	if len(principals) != 2 {
		t.Fatalf("write root principals = %#v, want sandbox group and root capability only", principals)
	}
	for _, want := range []string{GroupName, rootCapabilitySID} {
		if !containsFold(principals, want) {
			t.Fatalf("write root principals = %#v, want %q", principals, want)
		}
	}
	for _, unwanted := range []string{"CaelisSbxOffTest", "CaelisSbxOnTest", otherCapabilitySID} {
		if containsFold(principals, unwanted) {
			t.Fatalf("write root principals = %#v, did not want %q", principals, unwanted)
		}
	}
}

func TestRequiredPolicyACLTargetsDenyWriteUsesOverlappingRootCapability(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	extra := filepath.Join(base, "extra")
	gitDir := filepath.Join(workspace, ".git")
	for _, path := range []string{workspace, extra, gitDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	workspaceCapabilitySID := "S-1-5-21-11-12-13-14"
	extraCapabilitySID := "S-1-5-21-21-22-23-24"
	targets := requiredPolicyACLTargets(winpolicy.Policy{
		WriteRoots:     []string{workspace, extra},
		DenyWritePaths: []string{gitDir},
		CapabilitySIDs: []string{workspaceCapabilitySID, extraCapabilitySID},
		WriteRootCapabilitySIDs: map[string]string{
			pathutil.Normalize(workspace): workspaceCapabilitySID,
			pathutil.Normalize(extra):     extraCapabilitySID,
		},
	}, "CaelisSbxOffTest", "CaelisSbxOnTest")

	entries := entriesForPath(t, targets, gitDir, acl.Deny)
	principals := entryPrincipals(entries)
	if len(principals) != 1 {
		t.Fatalf("deny-write principals = %#v, want only overlapping root capability", principals)
	}
	if principals[0] != workspaceCapabilitySID {
		t.Fatalf("deny-write principals = %#v, want %q", principals, workspaceCapabilitySID)
	}
	for _, unwanted := range []string{GroupName, "CaelisSbxOffTest", "CaelisSbxOnTest", extraCapabilitySID} {
		if containsFold(principals, unwanted) {
			t.Fatalf("deny-write principals = %#v, did not want %q", principals, unwanted)
		}
	}
}

func TestWriteRootCapabilitySIDsForPathFallsBackToAllWriteRoots(t *testing.T) {
	base := t.TempDir()
	left := filepath.Join(base, "left")
	right := filepath.Join(base, "right")
	unrelated := filepath.Join(base, "unrelated")
	leftCapabilitySID := "S-1-5-21-31-32-33-34"
	rightCapabilitySID := "S-1-5-21-41-42-43-44"

	got := writeRootCapabilitySIDsForPath(winpolicy.Policy{
		WriteRoots: []string{left, right},
		WriteRootCapabilitySIDs: map[string]string{
			pathutil.Normalize(left):  leftCapabilitySID,
			pathutil.Normalize(right): rightCapabilitySID,
		},
	}, unrelated)
	if len(got) != 2 || !containsFold(got, leftCapabilitySID) || !containsFold(got, rightCapabilitySID) {
		t.Fatalf("writeRootCapabilitySIDsForPath() = %#v, want all write-root capabilities", got)
	}
}

func TestResolvePrincipalSIDsKeepsSIDStringsAndDedupes(t *testing.T) {
	got, err := resolvePrincipalSIDs("S-1-5-32-544", "S-1-5-32-544", " S-1-5-18 ")
	if err != nil {
		t.Fatalf("resolvePrincipalSIDs() error = %v", err)
	}
	want := []string{"S-1-5-32-544", "S-1-5-18"}
	if len(got) != len(want) {
		t.Fatalf("resolvePrincipalSIDs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolvePrincipalSIDs()[%d] = %q, want %q (all=%#v)", i, got[i], want[i], got)
		}
	}
}

func TestSandboxUserProfileDirsOnlyMatchesExactUserProfiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SystemDrive", root)
	usersRoot := filepath.Join(root, "Users")
	for _, name := range []string{
		"CaelisSbxOffabcd1234",
		"CaelisSbxOffabcd1234.HOST",
		"CaelisSbxOffabcd12345",
		"OtherUser",
	} {
		if err := os.MkdirAll(filepath.Join(usersRoot, name), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", name, err)
		}
	}
	got := sandboxUserProfileDirs("CaelisSbxOffabcd1234")
	if len(got) != 2 {
		t.Fatalf("sandboxUserProfileDirs() = %#v, want exact profile and suffixed profile", got)
	}
	for _, unwanted := range []string{
		filepath.Join(usersRoot, "CaelisSbxOffabcd12345"),
		filepath.Join(usersRoot, "OtherUser"),
	} {
		if containsPathKey(got, unwanted) {
			t.Fatalf("sandboxUserProfileDirs() = %#v, did not want %q", got, unwanted)
		}
	}
}

func TestRunACLTasksSerializesSamePathKey(t *testing.T) {
	var active int32
	var overlapped atomic.Bool
	task := func() error {
		if atomic.AddInt32(&active, 1) != 1 {
			overlapped.Store(true)
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nil
	}
	if err := runACLTasks([]aclTask{
		{key: "same-path", run: task},
		{key: "same-path", run: task},
	}); err != nil {
		t.Fatalf("runACLTasks() error = %v", err)
	}
	if overlapped.Load() {
		t.Fatal("runACLTasks ran same-path ACL updates concurrently")
	}
}

func TestRunACLTasksSerializesOverlappingPathKeys(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "repo", ".git")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll(child) error = %v", err)
	}
	var active int32
	var overlapped atomic.Bool
	task := func() error {
		if atomic.AddInt32(&active, 1) != 1 {
			overlapped.Store(true)
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nil
	}
	if err := runACLTasks([]aclTask{
		{key: pathutil.Key(parent), run: task},
		{key: pathutil.Key(child), run: task},
	}); err != nil {
		t.Fatalf("runACLTasks() error = %v", err)
	}
	if overlapped.Load() {
		t.Fatal("runACLTasks ran ancestor/descendant ACL updates concurrently")
	}
}

func TestBatchACLTasksKeepsIndependentPathKeysTogether(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	batches := batchACLTasks([]aclTask{
		{key: pathutil.Key(left), run: func() error { return nil }},
		{key: pathutil.Key(right), run: func() error { return nil }},
	})
	if len(batches) != 1 {
		t.Fatalf("batchACLTasks() produced %d batches, want 1", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Fatalf("batchACLTasks()[0] has %d groups, want 2", len(batches[0]))
	}
}

func TestRecordedWorkspaceACLCleanupRootsSkipsDefaultReadRoots(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("user home unavailable: %v", err)
	}
	workspace := filepath.Join(home, "WorkDir", "demo")
	readRoot := filepath.Join(home, "WorkDir", "readonly")
	writeRoot := filepath.Join(home, "WorkDir", "demo", "storage")
	traverseRoot := filepath.Join(home, "WorkDir")
	roots := recordedWorkspaceACLCleanupRoots(setupstate.WorkspaceRecord{
		WorkspaceRoot:  workspace,
		ReadRoots:      []string{`C:\Windows`, `C:\Program Files`, readRoot},
		WriteRoots:     []string{writeRoot},
		TraverseRoots:  []string{traverseRoot},
		DenyReadPaths:  []string{filepath.Join(home, ".ssh")},
		DenyWritePaths: []string{filepath.Join(workspace, ".git")},
	})

	for _, defaultRoot := range []string{`C:\Windows`, `C:\Program Files`} {
		if containsPathKey(roots, defaultRoot) {
			t.Fatalf("cleanup roots included default read root %q: %#v", defaultRoot, roots)
		}
		for _, ancestor := range ancestorPaths(defaultRoot) {
			if containsPathKey(roots, ancestor) {
				t.Fatalf("cleanup roots included default read ancestor %q from %q: %#v", ancestor, defaultRoot, roots)
			}
		}
	}
	for _, want := range []string{workspace, readRoot, writeRoot, traverseRoot, filepath.Join(home, ".ssh"), filepath.Join(workspace, ".git")} {
		if !containsPathKey(roots, want) {
			t.Fatalf("cleanup roots = %#v, want %q", roots, want)
		}
	}
}

func TestResetCleanupPlanUsesCurrentState(t *testing.T) {
	stateRoot := t.TempDir()
	dirs := setupstate.NewDirs(stateRoot)
	workspace := filepath.Join(stateRoot, "workspace")
	writeRoot := filepath.Join(workspace, "storage")
	readRoot := filepath.Join(stateRoot, "readonly")
	if err := os.MkdirAll(writeRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(writeRoot) error = %v", err)
	}
	if err := os.MkdirAll(readRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(readRoot) error = %v", err)
	}
	if err := setupstate.WriteWorkspace(dirs.WorkspacePath, setupstate.WorkspaceRecord{
		WorkspaceRoot:   workspace,
		ReadRoots:       []string{readRoot},
		WriteRoots:      []string{writeRoot},
		TraverseRoots:   []string{workspace},
		OfflineUsername: "CaelisSbxOffTest",
		OnlineUsername:  "CaelisSbxOnTest",
		CapabilitySIDs:  []string{"S-1-5-21-5-6-7-8"},
		WriteRootCapabilitySIDs: map[string]string{
			pathutil.Key(writeRoot): "S-1-5-21-9-10-11-12",
		},
	}); err != nil {
		t.Fatalf("WriteWorkspace() error = %v", err)
	}
	capStore := capabilityStoreSnapshot{
		WritableRootByPath: map[string]string{
			pathutil.Key(writeRoot): "S-1-5-21-13-14-15-16",
		},
	}
	data, err := json.Marshal(capStore)
	if err != nil {
		t.Fatalf("Marshal cap store error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dirs.CapPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(cap dir) error = %v", err)
	}
	if err := os.WriteFile(dirs.CapPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile(cap store) error = %v", err)
	}

	plan := resetCleanupPlanFromState(Payload{
		OperationID:     "new-op",
		StateRoot:       stateRoot,
		OfflineUsername: "CaelisSbxOffNew",
		OnlineUsername:  "CaelisSbxOnNew",
	}, dirs)
	for _, want := range []string{"CaelisSbxOffNew", "CaelisSbxOnNew"} {
		if !containsFold(plan.Users, want) {
			t.Fatalf("plan.Users = %#v, want %q", plan.Users, want)
		}
	}
	for _, want := range []string{workspace, readRoot, writeRoot} {
		if !containsPathKey(plan.ACLRoots, want) {
			t.Fatalf("plan.ACLRoots = %#v, want %q", plan.ACLRoots, want)
		}
	}
	for _, want := range []string{"CaelisSbxOffTest", "CaelisSbxOnTest", "S-1-5-21-13-14-15-16"} {
		if !containsFold(plan.ACLPrincipals, want) {
			t.Fatalf("plan.ACLPrincipals = %#v, want %q", plan.ACLPrincipals, want)
		}
	}
	if !containsPathKey(plan.StateDirs, dirs.Sandbox) || !containsPathKey(plan.StateDirs, dirs.Bin) || !containsPathKey(plan.StateDirs, dirs.Secrets) {
		t.Fatalf("plan.StateDirs = %#v, want sandbox/bin/secrets", plan.StateDirs)
	}
}

func TestExecuteRejectsExpiredResetPayloadBeforeElevation(t *testing.T) {
	err := ExecuteWithProgress(Payload{
		Version:   PayloadVersion,
		Kind:      SetupKindReset,
		StateRoot: t.TempDir(),
		ExpiresAt: time.Now().Add(-time.Second),
	}, nil)
	if err == nil {
		t.Fatal("ExecuteWithProgress() error = nil, want expired operation")
	}
}

func BenchmarkRequiredPolicyACLTargetsWorkspaceCarveouts(b *testing.B) {
	base := b.TempDir()
	const roots = 48
	policy := winpolicy.Policy{
		WriteRootCapabilitySIDs: map[string]string{},
	}
	for i := 0; i < roots; i++ {
		rawRoot := filepath.Join(base, "repo-"+string(rune('a'+i%26)), "workspace-"+string(rune('a'+(i/26))))
		rawGitDir := filepath.Join(rawRoot, ".git")
		if err := os.MkdirAll(rawGitDir, 0o755); err != nil {
			b.Fatalf("MkdirAll(%q) error = %v", rawGitDir, err)
		}
		root := pathutil.Normalize(rawRoot)
		gitDir := pathutil.Normalize(rawGitDir)
		sid := "S-1-5-21-100-200-300-" + strconv.Itoa(1000+i)
		policy.WriteRoots = append(policy.WriteRoots, root)
		policy.DenyWritePaths = append(policy.DenyWritePaths, gitDir)
		policy.CapabilitySIDs = append(policy.CapabilitySIDs, sid)
		policy.WriteRootCapabilitySIDs[root] = sid
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		targets := requiredPolicyACLTargets(policy, "CaelisSbxOffTest", "CaelisSbxOnTest")
		if len(targets) == 0 {
			b.Fatal("requiredPolicyACLTargets() returned no targets")
		}
	}
}

func entriesForPath(t *testing.T, targets []policyACLTarget, path string, mode acl.Mode) []acl.Entry {
	t.Helper()
	key := pathutil.Key(path)
	for _, target := range targets {
		if pathutil.Key(target.Path) != key {
			continue
		}
		var out []acl.Entry
		for _, entry := range target.Entries {
			if entry.Mode == mode {
				out = append(out, entry)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	t.Fatalf("target path %q with mode %q not found in %#v", path, mode, targets)
	return nil
}

func entryPrincipals(entries []acl.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Principal)
	}
	return out
}

func containsPathKey(paths []string, want string) bool {
	wantKey := pathutil.Key(want)
	for _, path := range paths {
		if pathutil.Key(path) == wantKey {
			return true
		}
	}
	return false
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
