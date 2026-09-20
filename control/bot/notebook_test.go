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

const testNotebookID = "bot-a1b2c3d4e5f60718293a4b5c6d7e8f90"

func newTestNotebook(t *testing.T, storeDir string) *Notebook {
	t.Helper()
	notebook, err := NewNotebook(storeDir, testNotebookID)
	if err != nil {
		t.Fatalf("NewNotebook() error = %v", err)
	}
	return notebook
}

func mustInitNotebook(t *testing.T, notebook *Notebook) {
	t.Helper()
	if err := notebook.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
}

func notebookToolSet(t *testing.T, notebook *Notebook) map[string]tool.Tool {
	t.Helper()
	tools, err := notebook.Tools()
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

func notebookCall(t *testing.T, item tool.Tool, args map[string]any) tool.Result {
	t.Helper()
	result, err := notebookCallErr(t, item, args)
	if err != nil {
		t.Fatalf("%s error = %v", item.Definition().Name, err)
	}
	return result
}

func notebookCallErr(t *testing.T, item tool.Tool, args map[string]any) (tool.Result, error) {
	t.Helper()
	input, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return item.Call(context.Background(), tool.Call{Name: item.Definition().Name, Input: input})
}

func notebookPayload(t *testing.T, result tool.Result) map[string]any {
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

func notebookBase(t *testing.T, notebook *Notebook) string {
	t.Helper()
	base, err := notebook.FileSystem().Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	return base
}

func TestNewNotebookValidatesIdentityAndStore(t *testing.T) {
	storeDir := t.TempDir()
	if _, err := NewNotebook("", testNotebookID); err == nil {
		t.Fatal("NewNotebook() accepted an empty store directory")
	}
	for _, id := range []string{"", "bot-", testNotebookID + "0", strings.ToUpper(testNotebookID)} {
		if _, err := NewNotebook(storeDir, id); err == nil {
			t.Fatalf("NewNotebook() accepted invalid identity %q", id)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	notebook, err := NewNotebook(missing, testNotebookID)
	if err != nil || notebook == nil {
		t.Fatalf("NewNotebook() did not accept an unprovisioned store: %v", err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("NewNotebook() performed filesystem I/O: %v", statErr)
	}
}

func TestNotebookRootLayout(t *testing.T) {
	root, err := NotebookRoot("/store", testNotebookID)
	if err != nil {
		t.Fatalf("NotebookRoot() error = %v", err)
	}
	want := filepath.Join("/store", "bots", testNotebookID, "notebook")
	if root != want {
		t.Fatalf("NotebookRoot() = %q, want %q", root, want)
	}
}

func TestNotebookInitCreatesRootAndIndex(t *testing.T) {
	storeDir := t.TempDir()
	notebook := newTestNotebook(t, storeDir)
	mustInitNotebook(t, notebook)
	base := notebookBase(t, notebook)
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() {
		t.Fatalf("notebook root = %q/%v, want directory", base, err)
	}
	index, err := os.Lstat(filepath.Join(base, NotebookIndex))
	if err != nil {
		t.Fatalf("index stat error = %v", err)
	}
	if !index.Mode().IsRegular() || (runtime.GOOS != "windows" && index.Mode().Perm() != 0o600) {
		t.Fatalf("index mode = %v, want regular 0600", index.Mode())
	}
}

func TestNotebookInitPreservesExistingIndex(t *testing.T) {
	storeDir := t.TempDir()
	notebook := newTestNotebook(t, storeDir)
	mustInitNotebook(t, notebook)
	base := notebookBase(t, notebook)
	index := filepath.Join(base, NotebookIndex)
	if err := os.WriteFile(index, []byte("keep me\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(index) error = %v", err)
	}
	mustInitNotebook(t, notebook)
	if got, err := os.ReadFile(index); err != nil || string(got) != "keep me\n" {
		t.Fatalf("Init overwrote the index: %q/%v", got, err)
	}
}

func TestNotebookPrepareRequiresProvisionedRoot(t *testing.T) {
	storeDir := t.TempDir()
	notebook := newTestNotebook(t, storeDir)
	if err := notebook.Prepare(context.Background()); err == nil {
		t.Fatal("Prepare() accepted an unprovisioned notebook")
	}
	if _, err := os.Stat(filepath.Join(storeDir, "bots")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Prepare() created directories: %v", err)
	}
}

func TestNotebookPrepareDoesNotRecreateIndex(t *testing.T) {
	storeDir := t.TempDir()
	mustInitNotebook(t, newTestNotebook(t, storeDir))
	base, err := NotebookRoot(storeDir, testNotebookID)
	if err != nil {
		t.Fatalf("NotebookRoot() error = %v", err)
	}
	index := filepath.Join(base, NotebookIndex)
	if err := os.Remove(index); err != nil {
		t.Fatalf("Remove(index) error = %v", err)
	}
	reopened := newTestNotebook(t, storeDir)
	if err := reopened.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := os.Lstat(index); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Prepare() recreated a deleted index: %v", err)
	}
}

func TestNotebookCommandMethodsDenied(t *testing.T) {
	notebook := newTestNotebook(t, t.TempDir())
	if _, err := notebook.Run(context.Background(), sandbox.CommandRequest{}); !errors.Is(err, ErrNotebookCommandDenied) {
		t.Fatalf("Run() error = %v, want command denied", err)
	}
	if _, err := notebook.Start(context.Background(), sandbox.CommandRequest{}); !errors.Is(err, ErrNotebookCommandDenied) {
		t.Fatalf("Start() error = %v, want command denied", err)
	}
	if _, err := notebook.OpenSession("x"); !errors.Is(err, ErrNotebookCommandDenied) {
		t.Fatalf("OpenSession() error = %v, want command denied", err)
	}
	if _, err := notebook.OpenSessionRef(sandbox.SessionRef{SessionID: "x"}); !errors.Is(err, ErrNotebookCommandDenied) {
		t.Fatalf("OpenSessionRef() error = %v, want command denied", err)
	}
}

func TestNotebookToolsConstructWithoutIO(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	notebook, err := NewNotebook(missing, testNotebookID)
	if err != nil {
		t.Fatalf("NewNotebook() error = %v", err)
	}
	tools, err := notebook.Tools()
	if err != nil || len(tools) != 5 {
		t.Fatalf("Tools() = %v, %v", len(tools), err)
	}
	if _, err := notebookCallErr(t, tools[0], map[string]any{"path": NotebookIndex}); err == nil {
		t.Fatal("Read() succeeded on an unprovisioned notebook")
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Tools() performed filesystem I/O: %v", statErr)
	}
}

func TestNotebookToolSandboxRuntime(t *testing.T) {
	notebook := newTestNotebook(t, t.TempDir())
	item := notebookToolSet(t, notebook)["Read"]
	wrapper, ok := item.(*notebookTool)
	if !ok {
		t.Fatalf("tool type = %T, want *notebookTool", item)
	}
	if wrapper.SandboxRuntime() != notebook {
		t.Fatal("SandboxRuntime() did not return the owning notebook")
	}
}

func TestNotebookCloseReopensForReuse(t *testing.T) {
	storeDir := t.TempDir()
	notebook := newTestNotebook(t, storeDir)
	mustInitNotebook(t, notebook)
	tools := notebookToolSet(t, notebook)
	notebookCall(t, tools["Write"], map[string]any{"path": NotebookIndex, "content": "hello\n"})
	if err := notebook.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	payload := notebookPayload(t, notebookCall(t, tools["Read"], map[string]any{"path": NotebookIndex}))
	if content, _ := payload["content"].(string); !strings.Contains(content, "hello") {
		t.Fatalf("Read after Close() = %q, want reopened content", content)
	}
}

func TestNotebookInitRejectsNonRegularIndex(t *testing.T) {
	storeDir := t.TempDir()
	notebook := newTestNotebook(t, storeDir)
	mustInitNotebook(t, notebook)
	index := filepath.Join(notebookBase(t, notebook), NotebookIndex)
	if err := os.Remove(index); err != nil {
		t.Fatalf("Remove(index) error = %v", err)
	}
	if err := os.Mkdir(index, 0o700); err != nil {
		t.Fatalf("Mkdir(index) error = %v", err)
	}
	if err := notebook.Init(context.Background()); err == nil {
		t.Fatal("Init() treated a non-regular index as provisioned")
	}
}

func TestNotebookToolDefinitionKeepsSchemaDropsCommandAdvice(t *testing.T) {
	notebook := newTestNotebook(t, t.TempDir())
	tools, err := notebook.Tools()
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	for _, item := range tools {
		wrapper, ok := item.(*notebookTool)
		if !ok {
			t.Fatalf("tool type = %T, want *notebookTool", item)
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
