package capability

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

const testHostUserSID = "S-1-5-21-111-222-333-1001"

func TestBindWriteRootsPersistsStableRootSIDs(t *testing.T) {
	store := filepath.Join(t.TempDir(), "cap_sids.json")
	workspace := filepath.Join(t.TempDir(), "workspace")
	commandDir := filepath.Join(workspace, "nested", "command")
	envRoot := filepath.Join(t.TempDir(), "private-env")
	extra := filepath.Join(t.TempDir(), "extra")
	scope := Scope{
		HostUserSID:    testHostUserSID,
		WorkspaceRoot:  workspace,
		SandboxEnvRoot: envRoot,
		WriteRoots:     []string{workspace, commandDir, envRoot, extra},
	}

	first, err := BindWriteRoots(store, scope)
	if err != nil {
		t.Fatalf("BindWriteRoots() error = %v", err)
	}
	if len(first.AllSIDs) != 4 {
		t.Fatalf("AllSIDs = %#v, want one SID per canonical root", first.AllSIDs)
	}
	workspaceSID := first.WriteRootTo[pathutil.Normalize(workspace)]
	if workspaceSID == "" {
		t.Fatalf("WriteRootTo = %#v, want workspace SID", first.WriteRootTo)
	}
	for _, root := range []string{commandDir, envRoot, extra} {
		if sid := first.WriteRootTo[pathutil.Normalize(root)]; sid == "" || sid == workspaceSID {
			t.Fatalf("WriteRootTo[%s] = %q, want a path-bound identity distinct from workspace SID %q", root, sid, workspaceSID)
		}
	}
	for _, sid := range first.AllSIDs {
		if !strings.HasPrefix(sid, "S-1-5-21-") {
			t.Fatalf("SID = %q, want generated S-1-5-21 SID", sid)
		}
	}

	scope.WriteRoots = []string{extra, envRoot, commandDir, workspace}
	second, err := BindWriteRoots(store, scope)
	if err != nil {
		t.Fatalf("second BindWriteRoots() error = %v", err)
	}
	if first.WriteRootTo[pathutil.Normalize(workspace)] != second.WriteRootTo[pathutil.Normalize(workspace)] {
		t.Fatalf("workspace SID changed: %q -> %q", first.WriteRootTo[pathutil.Normalize(workspace)], second.WriteRootTo[pathutil.Normalize(workspace)])
	}
	if first.WriteRootTo[pathutil.Normalize(extra)] != second.WriteRootTo[pathutil.Normalize(extra)] {
		t.Fatalf("extra SID changed: %q -> %q", first.WriteRootTo[pathutil.Normalize(extra)], second.WriteRootTo[pathutil.Normalize(extra)])
	}
	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("ReadFile(store) error = %v", err)
	}
	var persisted Store
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode store: %v", err)
	}
	if persisted.Version != StoreVersion || !strings.EqualFold(persisted.HostUserSID, testHostUserSID) {
		t.Fatalf("persisted store = %+v, want schema v%d for Host user %s", persisted, StoreVersion, testHostUserSID)
	}
}

func TestBindWriteRootsUsesSameIdentityAcrossWorkspaceAndExternalRoles(t *testing.T) {
	sharedRoot := filepath.Join(t.TempDir(), "shared-root")
	workspaceBinding, err := BindWriteRoots(filepath.Join(t.TempDir(), "cap_sids.json"), Scope{
		HostUserSID:   testHostUserSID,
		WorkspaceRoot: sharedRoot,
		WriteRoots:    []string{sharedRoot},
	})
	if err != nil {
		t.Fatalf("BindWriteRoots(workspace role) error = %v", err)
	}
	externalBinding, err := BindWriteRoots(filepath.Join(t.TempDir(), "cap_sids.json"), Scope{
		HostUserSID:   testHostUserSID,
		WorkspaceRoot: filepath.Join(t.TempDir(), "other-workspace"),
		WriteRoots:    []string{sharedRoot},
	})
	if err != nil {
		t.Fatalf("BindWriteRoots(external role) error = %v", err)
	}
	key := pathutil.Normalize(sharedRoot)
	if workspaceBinding.WriteRootTo[key] == "" || workspaceBinding.WriteRootTo[key] != externalBinding.WriteRootTo[key] {
		t.Fatalf("same physical root SIDs = %q/%q, want one Host/path identity", workspaceBinding.WriteRootTo[key], externalBinding.WriteRootTo[key])
	}
}

func TestBindWriteRootsConcurrentPersistsValidStableStore(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "cap_sids.json")
	workspace := filepath.Join(dir, "workspace")
	extraA := filepath.Join(dir, "extra-a")
	extraB := filepath.Join(dir, "extra-b")
	roots := []string{workspace, extraA, extraB}
	scope := Scope{HostUserSID: testHostUserSID, WorkspaceRoot: workspace, WriteRoots: roots}

	const workers = 32
	start := make(chan struct{})
	results := make([]Binding, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				results[i], errs[i] = BindWriteRoots(store, scope)
				return
			}
			reordered := scope
			reordered.WriteRoots = []string{extraB, workspace, extraA}
			results[i], errs[i] = BindWriteRoots(store, reordered)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("BindWriteRoots[%d]() error = %v", i, err)
		}
	}
	want := results[0].WriteRootTo
	for i, result := range results[1:] {
		for _, root := range roots {
			normalized := pathutil.Normalize(root)
			if got := result.WriteRootTo[normalized]; got == "" || got != want[normalized] {
				t.Fatalf("result[%d][%s] = %q, want stable %q", i+1, normalized, got, want[normalized])
			}
		}
	}

	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("ReadFile(store) error = %v", err)
	}
	var persisted Store
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("store JSON is invalid: %v\n%s", err, data)
	}
}

func TestBindWriteRootsConcurrentProcessesPersistsValidStableStore(t *testing.T) {
	if os.Getenv("CAELIS_CAPABILITY_PROCESS_CONCURRENCY") != "1" {
		t.Skip("set CAELIS_CAPABILITY_PROCESS_CONCURRENCY=1 to run cross-process lock integration coverage")
	}
	dir := t.TempDir()
	store := filepath.Join(dir, "cap_sids.json")
	workspace := filepath.Join(dir, "workspace")
	extraA := filepath.Join(dir, "extra-a")
	extraB := filepath.Join(dir, "extra-b")
	roots := []string{workspace, extraA, extraB}

	const workers = 10
	type childProc struct {
		cmd *exec.Cmd
		out *bytes.Buffer
	}
	children := make([]childProc, 0, workers)
	for i := 0; i < workers; i++ {
		order := "0"
		if i%2 != 0 {
			order = "1"
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestBindWriteRootsHelperProcess$")
		cmd.Env = append(os.Environ(),
			"CAELIS_CAPABILITY_BIND_HELPER=1",
			"CAELIS_CAPABILITY_BIND_STORE="+store,
			"CAELIS_CAPABILITY_BIND_WORKSPACE="+workspace,
			"CAELIS_CAPABILITY_BIND_EXTRA_A="+extraA,
			"CAELIS_CAPABILITY_BIND_EXTRA_B="+extraB,
			"CAELIS_CAPABILITY_BIND_ORDER="+order,
			"CAELIS_CAPABILITY_BIND_USER_SID="+testHostUserSID,
		)
		out := &bytes.Buffer{}
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Start(); err != nil {
			t.Fatalf("helper[%d] Start() error = %v", i, err)
		}
		children = append(children, childProc{cmd: cmd, out: out})
	}
	for i, child := range children {
		if err := child.cmd.Wait(); err != nil {
			t.Fatalf("helper[%d] Wait() error = %v\n%s", i, err, child.out.String())
		}
	}

	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("ReadFile(store) error = %v", err)
	}
	var persisted Store
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("store JSON is invalid: %v\n%s", err, data)
	}
	binding, err := LookupWriteRoots(store, Scope{HostUserSID: testHostUserSID, WorkspaceRoot: workspace, WriteRoots: roots})
	if err != nil {
		t.Fatalf("LookupWriteRoots() error = %v", err)
	}
	for _, root := range roots {
		normalized := pathutil.Normalize(root)
		if got := binding.WriteRootTo[normalized]; got == "" {
			t.Fatalf("binding[%s] empty after concurrent helper processes: %#v", normalized, binding)
		}
	}
}

func TestStoreFileLockContentionAndRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "cap_sids.json.lock")
	first, contended, err := tryAcquireStoreFileLock(lockPath)
	if err != nil || contended {
		t.Fatalf("tryAcquireStoreFileLock(first) = %v/%v, want acquired", contended, err)
	}
	second, contended, err := tryAcquireStoreFileLock(lockPath)
	if err != nil || !contended || second != nil {
		t.Fatalf("tryAcquireStoreFileLock(second) = %#v/%v/%v, want contention", second, contended, err)
	}
	if err := releaseStoreFileLock(first); err != nil {
		t.Fatalf("releaseStoreFileLock(first) error = %v", err)
	}
	third, contended, err := tryAcquireStoreFileLock(lockPath)
	if err != nil || contended {
		t.Fatalf("tryAcquireStoreFileLock(after release) = %v/%v, want acquired", contended, err)
	}
	if err := releaseStoreFileLock(third); err != nil {
		t.Fatalf("releaseStoreFileLock(third) error = %v", err)
	}
}

func TestBindWriteRootsHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_CAPABILITY_BIND_HELPER") != "1" {
		return
	}
	store := os.Getenv("CAELIS_CAPABILITY_BIND_STORE")
	workspace := os.Getenv("CAELIS_CAPABILITY_BIND_WORKSPACE")
	extraA := os.Getenv("CAELIS_CAPABILITY_BIND_EXTRA_A")
	extraB := os.Getenv("CAELIS_CAPABILITY_BIND_EXTRA_B")
	hostUserSID := os.Getenv("CAELIS_CAPABILITY_BIND_USER_SID")
	roots := []string{workspace, extraA, extraB}
	if os.Getenv("CAELIS_CAPABILITY_BIND_ORDER") == "1" {
		roots = []string{extraB, workspace, extraA}
	}
	if _, err := BindWriteRoots(store, Scope{HostUserSID: hostUserSID, WorkspaceRoot: workspace, WriteRoots: roots}); err != nil {
		t.Fatalf("BindWriteRoots() error = %v", err)
	}
}

func TestBindWriteRootsExcludesStateDirFromStableIdentity(t *testing.T) {
	store := filepath.Join(t.TempDir(), "cap_sids.json")
	workspace := filepath.Join(t.TempDir(), "workspace")
	extra := filepath.Join(t.TempDir(), "extra")

	scope := Scope{HostUserSID: testHostUserSID, WorkspaceRoot: workspace, WriteRoots: []string{workspace, extra}}
	first, err := BindWriteRoots(store, scope)
	if err != nil {
		t.Fatalf("BindWriteRoots(first) error = %v", err)
	}
	if err := os.Remove(store); err != nil {
		t.Fatalf("Remove(store) error = %v", err)
	}
	secondStore := filepath.Join(t.TempDir(), "other-state", "cap_sids.json")
	second, err := BindWriteRoots(secondStore, scope)
	if err != nil {
		t.Fatalf("BindWriteRoots(second) error = %v", err)
	}

	for _, root := range []string{workspace, extra} {
		normalized := pathutil.Normalize(root)
		if first.WriteRootTo[normalized] != second.WriteRootTo[normalized] {
			t.Fatalf("SID for %s changed after rebuild: %q -> %q", normalized, first.WriteRootTo[normalized], second.WriteRootTo[normalized])
		}
	}
}

func TestBindWriteRootsChangesIdentityWithHostUserSID(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	scope := Scope{HostUserSID: testHostUserSID, WorkspaceRoot: workspace, WriteRoots: []string{workspace}}
	first, err := BindWriteRoots(filepath.Join(t.TempDir(), "cap_sids.json"), scope)
	if err != nil {
		t.Fatalf("BindWriteRoots(first user) error = %v", err)
	}
	scope.HostUserSID = "S-1-5-21-111-222-333-1002"
	second, err := BindWriteRoots(filepath.Join(t.TempDir(), "cap_sids.json"), scope)
	if err != nil {
		t.Fatalf("BindWriteRoots(second user) error = %v", err)
	}
	if first.WorkspaceSID == second.WorkspaceSID {
		t.Fatalf("workspace SID = %q for two Host users, want user-bound identities", first.WorkspaceSID)
	}
}

func TestBindWriteRootsSharesExternalRootIdentityAcrossWorkspaces(t *testing.T) {
	externalRoot := filepath.Join(t.TempDir(), "shared-external")
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	store := filepath.Join(t.TempDir(), "cap_sids.json")

	first, err := BindWriteRoots(store, Scope{
		HostUserSID:   testHostUserSID,
		WorkspaceRoot: workspaceA,
		WriteRoots:    []string{workspaceA, externalRoot},
	})
	if err != nil {
		t.Fatalf("BindWriteRoots(workspace A) error = %v", err)
	}
	second, err := BindWriteRoots(store, Scope{
		HostUserSID:   testHostUserSID,
		WorkspaceRoot: workspaceB,
		WriteRoots:    []string{workspaceB, externalRoot},
	})
	if err != nil {
		t.Fatalf("BindWriteRoots(workspace B) error = %v", err)
	}
	externalKey := pathutil.Normalize(externalRoot)
	if first.WriteRootTo[externalKey] == "" || first.WriteRootTo[externalKey] != second.WriteRootTo[externalKey] {
		t.Fatalf("external root SIDs = %q/%q, want one Host/path identity", first.WriteRootTo[externalKey], second.WriteRootTo[externalKey])
	}
	if first.WorkspaceSID == second.WorkspaceSID {
		t.Fatalf("workspace SIDs = %q/%q, want workspace-scoped identities", first.WorkspaceSID, second.WorkspaceSID)
	}
	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("ReadFile(store) error = %v", err)
	}
	var persisted Store
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal(store) error = %v", err)
	}
	if got := len(persisted.ExternalRootByPath); got != 1 {
		t.Fatalf("ExternalRootByPath = %+v, want one shared path receipt", persisted.ExternalRootByPath)
	}
}

func TestBindWriteRootsMigratesV1StoreWithoutDroppingACLReceipts(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "cap_sids.json")
	workspace := filepath.Join(dir, "workspace")
	extra := filepath.Join(dir, "extra")
	legacyWorkspaceSID := "S-1-5-21-1-2-3-4"
	legacyExternalSID := "S-1-5-21-5-6-7-8"
	v1 := Store{
		WorkspaceByCWD:     map[string]string{pathutil.Key(workspace): legacyWorkspaceSID},
		WritableRootByPath: map[string]string{pathutil.Key(extra): legacyExternalSID},
	}
	data, err := json.Marshal(v1)
	if err != nil {
		t.Fatalf("Marshal(v1) error = %v", err)
	}
	if err := os.WriteFile(storePath, data, 0o600); err != nil {
		t.Fatalf("WriteFile(v1) error = %v", err)
	}

	binding, err := BindWriteRoots(storePath, Scope{
		HostUserSID:   testHostUserSID,
		WorkspaceRoot: workspace,
		WriteRoots:    []string{workspace, extra},
	})
	if err != nil {
		t.Fatalf("BindWriteRoots() error = %v", err)
	}
	data, err = os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("ReadFile(v2) error = %v", err)
	}
	var v2 Store
	if err := json.Unmarshal(data, &v2); err != nil {
		t.Fatalf("Unmarshal(v2) error = %v", err)
	}
	if v2.Version != StoreVersion || v2.LegacyV1 == nil {
		t.Fatalf("migrated store = %+v, want v%d plus legacy receipts", v2, StoreVersion)
	}
	if v2.LegacyV1.WorkspaceByCWD[pathutil.Key(workspace)] != legacyWorkspaceSID || v2.LegacyV1.WritableRootByPath[pathutil.Key(extra)] != legacyExternalSID {
		t.Fatalf("legacy receipts = %+v, want original v1 mappings", v2.LegacyV1)
	}
	if binding.WorkspaceSID == legacyWorkspaceSID || binding.WriteRootTo[pathutil.Normalize(extra)] == legacyExternalSID {
		t.Fatalf("v2 binding = %+v, want newly derived schema-v2 identities", binding)
	}
}

func TestBindWriteRootsRejectsHostUserRotationInOneStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "cap_sids.json")
	workspace := filepath.Join(t.TempDir(), "workspace")
	scope := Scope{HostUserSID: testHostUserSID, WorkspaceRoot: workspace, WriteRoots: []string{workspace}}
	if _, err := BindWriteRoots(storePath, scope); err != nil {
		t.Fatalf("BindWriteRoots(first user) error = %v", err)
	}
	scope.HostUserSID = "S-1-5-21-111-222-333-1002"
	binding, err := BindWriteRoots(storePath, scope)
	var rotation *HostUserRotationError
	if !errors.As(err, &rotation) {
		t.Fatalf("BindWriteRoots(rotated user) error = %v, want typed rotation input", err)
	}
	if binding.WorkspaceSID == "" {
		t.Fatalf("BindWriteRoots(rotated user) binding = %+v, want deterministic new identity plan", binding)
	}
	if rotation.StoredHostUserSID != testHostUserSID || rotation.CurrentHostUserSID != scope.HostUserSID {
		t.Fatalf("rotation = %+v, want stored/current Host identities", rotation)
	}
}
