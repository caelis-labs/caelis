package capability

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

func TestBindWriteRootsPersistsStableRootSIDs(t *testing.T) {
	store := filepath.Join(t.TempDir(), "cap_sids.json")
	workspace := filepath.Join(t.TempDir(), "workspace")
	extra := filepath.Join(t.TempDir(), "extra")

	first, err := BindWriteRoots(store, workspace, []string{workspace, extra})
	if err != nil {
		t.Fatalf("BindWriteRoots() error = %v", err)
	}
	if len(first.AllSIDs) != 2 {
		t.Fatalf("AllSIDs = %#v, want two SIDs", first.AllSIDs)
	}
	if first.WriteRootTo[pathutil.Normalize(workspace)] == "" || first.WriteRootTo[pathutil.Normalize(extra)] == "" {
		t.Fatalf("WriteRootTo = %#v, want workspace and extra mappings", first.WriteRootTo)
	}
	for _, sid := range first.AllSIDs {
		if !strings.HasPrefix(sid, "S-1-5-21-") {
			t.Fatalf("SID = %q, want generated S-1-5-21 SID", sid)
		}
	}

	second, err := BindWriteRoots(store, workspace, []string{extra, workspace})
	if err != nil {
		t.Fatalf("second BindWriteRoots() error = %v", err)
	}
	if first.WriteRootTo[pathutil.Normalize(workspace)] != second.WriteRootTo[pathutil.Normalize(workspace)] {
		t.Fatalf("workspace SID changed: %q -> %q", first.WriteRootTo[pathutil.Normalize(workspace)], second.WriteRootTo[pathutil.Normalize(workspace)])
	}
	if first.WriteRootTo[pathutil.Normalize(extra)] != second.WriteRootTo[pathutil.Normalize(extra)] {
		t.Fatalf("extra SID changed: %q -> %q", first.WriteRootTo[pathutil.Normalize(extra)], second.WriteRootTo[pathutil.Normalize(extra)])
	}
}

func TestBindWriteRootsConcurrentPersistsValidStableStore(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "cap_sids.json")
	workspace := filepath.Join(dir, "workspace")
	extraA := filepath.Join(dir, "extra-a")
	extraB := filepath.Join(dir, "extra-b")
	roots := []string{workspace, extraA, extraB}

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
				results[i], errs[i] = BindWriteRoots(store, workspace, roots)
				return
			}
			results[i], errs[i] = BindWriteRoots(store, workspace, []string{extraB, workspace, extraA})
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
	if _, err := os.Stat(store + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock file remains after concurrent bind: %v", err)
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
	binding, err := LookupWriteRoots(store, workspace, roots)
	if err != nil {
		t.Fatalf("LookupWriteRoots() error = %v", err)
	}
	for _, root := range roots {
		normalized := pathutil.Normalize(root)
		if got := binding.WriteRootTo[normalized]; got == "" {
			t.Fatalf("binding[%s] empty after concurrent helper processes: %#v", normalized, binding)
		}
	}
	if _, err := os.Stat(store + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock file remains after concurrent helper processes: %v", err)
	}
}

func TestStoreLockContentionDistinguishesRaceFromPermissionFailure(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "cap_sids.json.lock")
	accessDenied := &os.PathError{Op: "open", Path: lockPath, Err: windowsAccessDenied}

	if !storeLockContention(lockPath, accessDenied) {
		t.Fatalf("storeLockContention() = false for access denied in writable lock directory, want transient contention")
	}
	if err := os.WriteFile(lockPath, []byte("locked"), 0o600); err != nil {
		t.Fatalf("WriteFile(lockPath) error = %v", err)
	}
	if !storeLockContention(lockPath, accessDenied) {
		t.Fatalf("storeLockContention() = false for existing lock file, want contention")
	}

	blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedParent, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(blockedParent) error = %v", err)
	}
	blockedLockPath := filepath.Join(blockedParent, "cap_sids.json.lock")
	blockedAccessDenied := &os.PathError{Op: "open", Path: blockedLockPath, Err: windowsAccessDenied}
	if storeLockContention(blockedLockPath, blockedAccessDenied) {
		t.Fatalf("storeLockContention() = true for unwritable lock directory, want original permission error")
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
	roots := []string{workspace, extraA, extraB}
	if os.Getenv("CAELIS_CAPABILITY_BIND_ORDER") == "1" {
		roots = []string{extraB, workspace, extraA}
	}
	if _, err := BindWriteRoots(store, workspace, roots); err != nil {
		t.Fatalf("BindWriteRoots() error = %v", err)
	}
}

func TestBindWriteRootsReusesStableSIDsAfterStoreRebuild(t *testing.T) {
	store := filepath.Join(t.TempDir(), "cap_sids.json")
	workspace := filepath.Join(t.TempDir(), "workspace")
	extra := filepath.Join(t.TempDir(), "extra")

	first, err := BindWriteRoots(store, workspace, []string{workspace, extra})
	if err != nil {
		t.Fatalf("BindWriteRoots(first) error = %v", err)
	}
	if err := os.Remove(store); err != nil {
		t.Fatalf("Remove(store) error = %v", err)
	}
	second, err := BindWriteRoots(store, workspace, []string{workspace, extra})
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
