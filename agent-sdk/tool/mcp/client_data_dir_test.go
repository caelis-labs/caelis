package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMCPStdioCreatesDataUnderExplicitRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new store")
	dataDir := filepath.Join(root, "session data")
	workDir := filepath.Join(dataDir, "work")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	mgr, err := newInitializedTestManager(ctx, []ServerSpec{{
		PluginID: "fixture", Name: "explicit-root", Command: os.Args[0],
		Args:        []string{"-test.run=^TestMCPServerHelperProcess$"},
		Env:         map[string]string{"CAELIS_MCP_HELPER": "1"},
		DataDirRoot: root, DataDir: dataDir, WorkDir: workDir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if len(mgr.Tools()) == 0 {
		t.Fatalf("server did not start in its created working directory: %+v", mgr.GetServerInfos("fixture"))
	}
	for _, path := range []string{root, dataDir, workDir} {
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Fatalf("missing real directory %q: %v", path, err)
		}
	}
}

func TestMCPDataDirRejectsEscapeAndRedirectedComponents(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := ensureDataDir(root, filepath.Join(outside, "data"), outside); err == nil {
		t.Fatal("accepted a data directory outside the trusted root")
	}
	link := filepath.Join(root, "redirect")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := ensureDataDir(root, filepath.Join(link, "data"), outside); err == nil {
		t.Fatal("accepted a symlink in the data directory path")
	}
	if _, err := os.Stat(filepath.Join(outside, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected external data directory: %v", err)
	}
	if err := ensureDataDir(root, root, link); err == nil {
		t.Fatal("accepted the trusted root as its own data directory")
	}
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(dataDir, "work")
	if err := os.Symlink(outside, workDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureDataDir(root, dataDir, workDir); err == nil {
		t.Fatal("accepted a symlink inside the data directory as the working directory")
	}
}

func TestMCPDataRootPartitionsOnlyDifferentEffectiveAnchors(t *testing.T) {
	root := t.TempDir()
	spec := ServerSpec{PluginID: "fixture", Name: "root", Command: os.Args[0], WorkDir: root, DataDir: filepath.Join(root, "plugins", "data", "instance")}
	launch := prepareLaunch(spec, nil)
	if launch.err != nil {
		t.Fatal(launch.err)
	}
	legacy := connectionKey(spec, launch)
	spec.DataDirRoot = root
	if connectionKey(spec, launch) != legacy {
		t.Fatal("equivalent explicit and legacy anchors used separate connections")
	}
	spec.DataDirRoot = filepath.Join(root, "plugins")
	if connectionKey(spec, launch) == legacy {
		t.Fatal("different validation anchors shared a connection")
	}
}
