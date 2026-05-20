//go:build windows

package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestRequiredPolicyACLTargetsKeepsCapabilitiesOffAncestors(t *testing.T) {
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

	var foundAncestor bool
	var foundRootCapability bool
	rootKey := pathutil.Key(root)
	for _, target := range targets {
		isRoot := pathutil.Key(target.Path) == rootKey
		for _, entry := range target.Entries {
			if entry.Principal != capabilitySID {
				continue
			}
			if isRoot {
				foundRootCapability = true
			} else {
				t.Fatalf("capability SID was granted on ancestor target %q", target.Path)
			}
		}
		if !isRoot {
			foundAncestor = true
		}
	}
	if !foundAncestor {
		t.Fatalf("requiredPolicyACLTargets(%q) did not include any ancestor targets", root)
	}
	if !foundRootCapability {
		t.Fatalf("requiredPolicyACLTargets(%q) did not grant capability SID on the write root", root)
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

func TestAppendAncestorACLTasksOrdersParentsBeforeChildren(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("user home unavailable: %v", err)
	}
	parent := filepath.Join(home, "WorkDir")
	child := filepath.Join(parent, "demo")
	root := filepath.Join(child, "workspace")
	tasks := appendAncestorACLTasks(nil, root, []string{"S-1-1-0"}, map[string]struct{}{})
	if len(tasks) < 2 {
		t.Fatalf("appendAncestorACLTasks(%q) produced %d tasks, want at least 2", root, len(tasks))
	}
	if tasks[0].key != pathutil.Key(parent) {
		t.Fatalf("first ancestor task key = %q, want parent %q (all=%#v)", tasks[0].key, pathutil.Key(parent), tasks)
	}
	if tasks[1].key != pathutil.Key(child) {
		t.Fatalf("second ancestor task key = %q, want child %q (all=%#v)", tasks[1].key, pathutil.Key(child), tasks)
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
