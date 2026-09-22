package bot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const testFilesID = "bot-a1b2c3d4e5f60718293a4b5c6d7e8f90"

func newTestFiles(t *testing.T, storeDir string) *Files {
	t.Helper()
	files, err := NewFiles(storeDir, testFilesID)
	if err != nil {
		t.Fatalf("NewFiles() error = %v", err)
	}
	return files
}

func mustInitFiles(t *testing.T, files *Files) {
	t.Helper()
	if err := files.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
}

func privateFileToolSet(t *testing.T, files *Files) map[string]tool.Tool {
	t.Helper()
	tools, err := files.Tools()
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(tools) != 5 {
		t.Fatalf("Tools() count = %d, want 5", len(tools))
	}
	set := make(map[string]tool.Tool, len(tools))
	for _, item := range tools {
		set[item.Definition().Name] = item
	}
	return set
}

func filesCall(t *testing.T, item tool.Tool, args map[string]any) tool.Result {
	t.Helper()
	result, err := filesCallErr(t, item, args)
	if err != nil {
		t.Fatalf("%s error = %v", item.Definition().Name, err)
	}
	return result
}

func filesCallErr(t *testing.T, item tool.Tool, args map[string]any) (tool.Result, error) {
	t.Helper()
	input, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return item.Call(context.Background(), tool.Call{Name: item.Definition().Name, Input: input})
}

func filesPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("result.Content = %+v, want one JSON part", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	return payload
}

func filesBase(t *testing.T, files *Files) string {
	t.Helper()
	base, err := files.FileSystem().Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	return base
}

func TestNewFilesValidatesIdentityAndStore(t *testing.T) {
	storeDir := t.TempDir()
	if _, err := NewFiles("", testFilesID); err == nil {
		t.Fatal("NewFiles() accepted an empty store directory")
	}
	for _, id := range []string{"", "bot-", testFilesID + "0", strings.ToUpper(testFilesID)} {
		if _, err := NewFiles(storeDir, id); err == nil {
			t.Fatalf("NewFiles() accepted invalid identity %q", id)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	files, err := NewFiles(missing, testFilesID)
	if err != nil || files == nil {
		t.Fatalf("NewFiles() did not accept an unprovisioned store: %v", err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("NewFiles() performed filesystem I/O: %v", statErr)
	}
}

func TestFilesRootLayout(t *testing.T) {
	root, err := FilesRoot("/store", testFilesID)
	if err != nil {
		t.Fatalf("FilesRoot() error = %v", err)
	}
	want := filepath.Join("/store", "bots", testFilesID, "files")
	if root != want {
		t.Fatalf("FilesRoot() = %q, want %q", root, want)
	}
}

func TestFilesInitCreatesRootAndIndex(t *testing.T) {
	storeDir := t.TempDir()
	files := newTestFiles(t, storeDir)
	mustInitFiles(t, files)
	base := filesBase(t, files)
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() {
		t.Fatalf("files root = %q/%v, want directory", base, err)
	}
	index, err := os.Lstat(filepath.Join(base, NotebookIndex))
	if err != nil {
		t.Fatalf("index stat error = %v", err)
	}
	if !index.Mode().IsRegular() || (runtime.GOOS != "windows" && index.Mode().Perm() != 0o600) {
		t.Fatalf("index mode = %v, want regular 0600", index.Mode())
	}
}

func TestFilesInitPreservesExistingIndex(t *testing.T) {
	storeDir := t.TempDir()
	files := newTestFiles(t, storeDir)
	mustInitFiles(t, files)
	base := filesBase(t, files)
	index := filepath.Join(base, NotebookIndex)
	if err := os.WriteFile(index, []byte("keep me\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(index) error = %v", err)
	}
	mustInitFiles(t, files)
	if got, err := os.ReadFile(index); err != nil || string(got) != "keep me\n" {
		t.Fatalf("Init overwrote the index: %q/%v", got, err)
	}
}

func TestFilesPrepareRequiresProvisionedRoot(t *testing.T) {
	storeDir := t.TempDir()
	files := newTestFiles(t, storeDir)
	if err := files.Prepare(context.Background()); err == nil {
		t.Fatal("Prepare() accepted an unprovisioned files")
	}
	if _, err := os.Stat(filepath.Join(storeDir, "bots")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Prepare() created directories: %v", err)
	}
}

func TestFilesPrepareDoesNotRecreateIndex(t *testing.T) {
	storeDir := t.TempDir()
	mustInitFiles(t, newTestFiles(t, storeDir))
	base, err := FilesRoot(storeDir, testFilesID)
	if err != nil {
		t.Fatalf("FilesRoot() error = %v", err)
	}
	index := filepath.Join(base, NotebookIndex)
	if err := os.Remove(index); err != nil {
		t.Fatalf("Remove(index) error = %v", err)
	}
	reopened := newTestFiles(t, storeDir)
	if err := reopened.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := os.Lstat(index); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Prepare() recreated a deleted index: %v", err)
	}
}

func TestFilesCommandMethodsDenied(t *testing.T) {
	files := newTestFiles(t, t.TempDir())
	if _, err := files.Run(context.Background(), sandbox.CommandRequest{}); !errors.Is(err, ErrFilesCommandDenied) {
		t.Fatalf("Run() error = %v, want command denied", err)
	}
	if _, err := files.Start(context.Background(), sandbox.CommandRequest{}); !errors.Is(err, ErrFilesCommandDenied) {
		t.Fatalf("Start() error = %v, want command denied", err)
	}
	if _, err := files.OpenSession("x"); !errors.Is(err, ErrFilesCommandDenied) {
		t.Fatalf("OpenSession() error = %v, want command denied", err)
	}
	if _, err := files.OpenSessionRef(sandbox.SessionRef{SessionID: "x"}); !errors.Is(err, ErrFilesCommandDenied) {
		t.Fatalf("OpenSessionRef() error = %v, want command denied", err)
	}
}

func TestFilesToolsConstructWithoutIO(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	files, err := NewFiles(missing, testFilesID)
	if err != nil {
		t.Fatalf("NewFiles() error = %v", err)
	}
	tools, err := files.Tools()
	if err != nil || len(tools) != 5 {
		t.Fatalf("Tools() = %v, %v", len(tools), err)
	}
	if _, err := filesCallErr(t, tools[0], map[string]any{"path": NotebookIndex}); err == nil {
		t.Fatal("Read() succeeded on an unprovisioned files")
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Tools() performed filesystem I/O: %v", statErr)
	}
}

func TestFilesToolSandboxRuntime(t *testing.T) {
	files := newTestFiles(t, t.TempDir())
	item := privateFileToolSet(t, files)["Read"]
	wrapper, ok := item.(*privateFileTool)
	if !ok {
		t.Fatalf("tool type = %T, want *privateFileTool", item)
	}
	if wrapper.SandboxRuntime() != files {
		t.Fatal("SandboxRuntime() did not return the owning files")
	}
}

func TestFilesCloseReopensForReuse(t *testing.T) {
	storeDir := t.TempDir()
	files := newTestFiles(t, storeDir)
	mustInitFiles(t, files)
	tools := privateFileToolSet(t, files)
	filesCall(t, tools["Write"], map[string]any{"path": NotebookIndex, "content": "hello\n"})
	if err := files.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	payload := filesPayload(t, filesCall(t, tools["Read"], map[string]any{"path": NotebookIndex}))
	if content, _ := payload["content"].(string); !strings.Contains(content, "hello") {
		t.Fatalf("Read after Close() = %q, want reopened content", content)
	}
}

func TestFilesInitRejectsNonRegularIndex(t *testing.T) {
	storeDir := t.TempDir()
	files := newTestFiles(t, storeDir)
	mustInitFiles(t, files)
	index := filepath.Join(filesBase(t, files), NotebookIndex)
	if err := os.Remove(index); err != nil {
		t.Fatalf("Remove(index) error = %v", err)
	}
	if err := os.Mkdir(index, 0o700); err != nil {
		t.Fatalf("Mkdir(index) error = %v", err)
	}
	if err := files.Init(context.Background()); err == nil {
		t.Fatal("Init() treated a non-regular index as provisioned")
	}
}

func TestFilesToolDefinitionKeepsSchemaDropsCommandAdvice(t *testing.T) {
	files := newTestFiles(t, t.TempDir())
	tools, err := files.Tools()
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	for _, item := range tools {
		wrapper, ok := item.(*privateFileTool)
		if !ok {
			t.Fatalf("tool type = %T, want *privateFileTool", item)
		}
		inner := wrapper.Tool.Definition()
		outer := wrapper.Definition()
		if outer.Name != inner.Name || !reflect.DeepEqual(outer.InputSchema, inner.InputSchema) {
			t.Fatalf("%s Definition() changed the name or schema", outer.Name)
		}
		if strings.Contains(outer.Description, "RunCommand") {
			t.Fatalf("%s description still advises RunCommand: %q", outer.Name, outer.Description)
		}
	}
}
