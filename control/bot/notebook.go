package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

const (
	// NotebookIndex is the root-relative seed file created once when a Bot
	// notebook is initialized. Instructions refer to it as "index.md"; the
	// notebook never recreates it after initialization.
	NotebookIndex = "index.md"

	notebookBotsDir = "bots"
	notebookDirName = "notebook"
)

// notebookBackend identifies the Bot notebook as one file-only execution
// boundary. It is a fact reported to tools, not a registered sandbox backend.
const notebookBackend = sandbox.Backend("bot-notebook")

// ErrNotebookCommandDenied reports that a Bot notebook provides file tools
// only. Command execution is never available.
var ErrNotebookCommandDenied = errors.New("bot notebook: command execution is not available")

// Notebook is one Bot's bounded private file store. It implements
// sandbox.Runtime so the builtin file tools can read and write it, but exposes
// only a root-confined FileSystem and denies every command method. Identity is
// validated at construction; the root is opened lazily on first use and can
// never reach outside <store>/bots/<id>/notebook.
type Notebook struct {
	storeDir string
	id       string
	fs       *notebookFS

	// openMu guards the lazily opened root handle and its resolved path.
	openMu   sync.Mutex
	root     *os.Root
	rootPath string

	// callMu serializes whole tool calls so read-plan-write revision checks and
	// Patch planning stay atomic against concurrent calls on this notebook.
	callMu sync.Mutex
}

// NewNotebook binds one Bot identity to its notebook under storeDir. It
// validates the identity and performs no filesystem I/O.
func NewNotebook(storeDir, id string) (*Notebook, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return nil, errorcode.New(errorcode.InvalidArgument, "bot notebook: store directory is required")
	}
	if _, err := ConversationID(id); err != nil {
		return nil, err
	}
	notebook := &Notebook{storeDir: filepath.Clean(storeDir), id: id}
	notebook.fs = &notebookFS{owner: notebook}
	return notebook, nil
}

// NotebookRoot returns the notebook directory for one Bot. The result is
// lexical; the directory is untrusted until it is opened.
func NotebookRoot(storeDir, id string) (string, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return "", errorcode.New(errorcode.InvalidArgument, "bot notebook: store directory is required")
	}
	if _, err := ConversationID(id); err != nil {
		return "", err
	}
	return filepath.Join(storeDir, notebookBotsDir, id, notebookDirName), nil
}

// Init provisions the notebook for a newly created Bot: it ensures the root
// directory exists and writes NotebookIndex only when that file is absent. It
// never overwrites an existing index. Ordinary activation uses Prepare and must
// not recreate a deleted index.
func (n *Notebook) Init(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.openMu.Lock()
	defer n.openMu.Unlock()
	return n.initLocked()
}

// ProvisionInitial gives a Bot created before notebooks were universal its
// private notebook exactly once: it creates the root and seed index only when
// the root is absent. An existing root is left exactly as found, so a deleted
// index.md is never recreated, existing notes are never replaced, and a tampered
// path is reported by the tool boundary instead of being repaired.
func (n *Notebook) ProvisionInitial(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := NotebookRoot(n.storeDir, n.id)
	if err != nil {
		return err
	}
	switch _, err := os.Lstat(root); {
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	n.openMu.Lock()
	defer n.openMu.Unlock()
	return n.initLocked()
}

func (n *Notebook) initLocked() error {
	root, err := n.prepareLocked(true)
	if err != nil {
		return err
	}
	// Create-or-revalidate through one exclusive create: an existing index is
	// never assumed to be a provisioned index.
	file, err := root.OpenFile(NotebookIndex, os.O_CREATE|os.O_EXCL|os.O_WRONLY, notebookFileMode)
	switch {
	case err == nil:
		return file.Close()
	case !errors.Is(err, os.ErrExist):
		return err
	}
	info, err := root.Lstat(NotebookIndex)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errorcode.New(errorcode.FailedPrecondition, "bot notebook: index is not a regular file")
	}
	return nil
}

// Prepare opens the existing notebook root. It never creates the root or the
// index and fails closed when the notebook has not been initialized.
func (n *Notebook) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.openMu.Lock()
	defer n.openMu.Unlock()
	_, err := n.prepareLocked(false)
	return err
}

func (n *Notebook) prepareLocked(create bool) (*os.Root, error) {
	if n.root != nil {
		return n.root, nil
	}
	root, rootPath, err := n.openRoot(create)
	if err != nil {
		return nil, err
	}
	n.root = root
	n.rootPath = rootPath
	return root, nil
}

// openRoot opens the store root, then walks each notebook path component with
// os.Root, rejecting symlinks and any component that changes between the
// pre-open and post-open checks.
func (n *Notebook) openRoot(create bool) (*os.Root, string, error) {
	base, err := filepath.EvalSymlinks(n.storeDir)
	if err != nil {
		return nil, "", errorcode.Wrap(errorcode.FailedPrecondition, "bot notebook: resolve store directory", err)
	}
	storeRoot, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", errorcode.Wrap(errorcode.FailedPrecondition, "bot notebook: open store directory", err)
	}
	current := storeRoot
	for _, name := range []string{notebookBotsDir, n.id, notebookDirName} {
		child, childErr := openNotebookChild(current, name, create)
		_ = current.Close()
		if childErr != nil {
			return nil, "", childErr
		}
		current = child
	}
	return current, filepath.Join(base, notebookBotsDir, n.id, notebookDirName), nil
}

func openNotebookChild(parent *os.Root, name string, create bool) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, errorcode.Wrap(errorcode.FailedPrecondition, "bot notebook is not provisioned", err)
		}
		if err := parent.Mkdir(name, notebookDirMode); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, errorcode.Wrap(errorcode.Internal, "bot notebook: create directory", err)
		}
		before, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, errorcode.Wrap(errorcode.FailedPrecondition, "bot notebook: inspect directory", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, errorcode.New(errorcode.FailedPrecondition, "bot notebook: path component is not a real directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, errorcode.Wrap(errorcode.FailedPrecondition, "bot notebook: open directory", err)
	}
	opened, statErr := child.Stat(".")
	after, lstatErr := parent.Lstat(name)
	if statErr != nil || lstatErr != nil ||
		after.Mode()&os.ModeSymlink != 0 || !after.IsDir() ||
		!os.SameFile(before, after) || !os.SameFile(opened, after) {
		_ = child.Close()
		return nil, errorcode.New(errorcode.FailedPrecondition, "bot notebook: path component changed while opening")
	}
	return child, nil
}

// Tools returns the five builtin file tools bound to this notebook. Each call
// is serialized on the notebook so revision checks and Patch planning cannot
// interleave with another call.
func (n *Notebook) Tools() ([]tool.Tool, error) {
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), n)
	if err != nil {
		return nil, err
	}
	write, err := filesystem.NewWrite(n)
	if err != nil {
		return nil, err
	}
	patch, err := filesystem.NewPatch(n)
	if err != nil {
		return nil, err
	}
	glob, err := filesystem.NewGlob(n)
	if err != nil {
		return nil, err
	}
	search, err := filesystem.NewSearch(n)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{
		&notebookTool{owner: n, Tool: read},
		&notebookTool{owner: n, Tool: write},
		&notebookTool{owner: n, Tool: patch},
		&notebookTool{owner: n, Tool: glob},
		&notebookTool{owner: n, Tool: search},
	}, nil
}

// Close waits for in-flight calls to finish and releases the root handle. The
// notebook stays usable and reopens lazily on the next call.
func (n *Notebook) Close() error {
	n.callMu.Lock()
	defer n.callMu.Unlock()
	n.openMu.Lock()
	defer n.openMu.Unlock()
	root := n.root
	n.root = nil
	n.rootPath = ""
	if root == nil {
		return nil
	}
	return root.Close()
}

var (
	_ sandbox.Runtime           = (*Notebook)(nil)
	_ sandbox.PreparableRuntime = (*Notebook)(nil)
	_ tool.Tool                 = (*notebookTool)(nil)
)

type notebookTool struct {
	owner *Notebook
	tool.Tool
}

// notebookDescriptions replace model-facing advice that references tools the
// notebook does not admit, without changing any tool schema.
var notebookDescriptions = map[string]string{
	filesystem.GlobToolName:   "Find files matching a glob pattern. Use * for direct children and **/* for recursive discovery. Results are capped at 100 files.",
	filesystem.SearchToolName: "Search file contents with a regular expression. Matching is case-sensitive by default; use (?i) in pattern for case-insensitive search. Results are capped at 100 lines; a line over 8MiB fails the search rather than reporting no matches. Use Read for surrounding context.",
}

// SandboxRuntime exposes the notebook as the tool's sandbox descriptor source,
// matching the runtime policy layer's sandboxRuntimeProvider contract.
func (t *notebookTool) SandboxRuntime() sandbox.Runtime { return t.owner }

// Definition reports the builtin schema with any notebook-inapplicable advice
// replaced.
func (t *notebookTool) Definition() tool.Definition {
	definition := t.Tool.Definition()
	if description, ok := notebookDescriptions[definition.Name]; ok {
		definition.Description = description
	}
	return definition
}

func (t *notebookTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	t.owner.callMu.Lock()
	defer t.owner.callMu.Unlock()
	if err := t.owner.Prepare(ctx); err != nil {
		return tool.Result{}, err
	}
	return t.Tool.Call(ctx, call)
}

func (n *Notebook) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:      notebookBackend,
		Isolation:    sandbox.IsolationProcess,
		Capabilities: sandbox.CapabilitySet{FileSystem: true},
		DefaultConstraints: sandbox.Constraints{
			Backend:    notebookBackend,
			Permission: sandbox.PermissionWorkspaceWrite,
			Isolation:  sandbox.IsolationProcess,
			Network:    sandbox.NetworkDisabled,
		},
	}
}

func (n *Notebook) FileSystem() sandbox.FileSystem { return n.fs }

func (n *Notebook) FileSystemFor(sandbox.Constraints) sandbox.FileSystem { return n.fs }

func (n *Notebook) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, ErrNotebookCommandDenied
}

func (n *Notebook) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, ErrNotebookCommandDenied
}

func (n *Notebook) OpenSession(string) (sandbox.Session, error) {
	return nil, ErrNotebookCommandDenied
}

func (n *Notebook) OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error) {
	return nil, ErrNotebookCommandDenied
}

func (n *Notebook) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{notebookBackend}
}

func (n *Notebook) Status() sandbox.Status {
	return sandbox.Status{RequestedBackend: notebookBackend, ResolvedBackend: notebookBackend}
}
