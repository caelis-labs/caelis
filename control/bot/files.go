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
	// files is initialized. Instructions refer to it as "index.md"; the
	// files never recreates it after initialization.
	NotebookIndex = "index.md"

	botFilesDir  = "bots"
	filesDirName = "files"
)

// privateFilesBackend identifies the Bot files as one file-only execution
// boundary. It is a fact reported to tools, not a registered sandbox backend.
const privateFilesBackend = sandbox.Backend("bot-files")

// ErrFilesCommandDenied reports that a Bot file area provides file tools
// only. Command execution is never available.
var ErrFilesCommandDenied = errors.New("bot files: command execution is not available")

// Files is one Bot's bounded private file store. It implements
// sandbox.Runtime so the builtin file tools can read and write it, but exposes
// only a root-confined FileSystem and denies every command method. Identity is
// validated at construction; the root is opened lazily on first use and can
// never reach outside <store>/bots/<id>/files.
type Files struct {
	storeDir string
	id       string
	workID   string
	fs       *privateFS

	// openMu guards the lazily opened root handle and its resolved path.
	openMu   sync.Mutex
	root     *os.Root
	rootPath string

	// callMu serializes whole tool calls so read-plan-write revision checks and
	// Patch planning stay atomic against concurrent calls on this file area.
	callMu sync.Mutex
}

// NewFiles binds one Bot identity to its files under storeDir. It
// validates the identity and performs no filesystem I/O.
func NewFiles(storeDir, id string) (*Files, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return nil, errorcode.New(errorcode.InvalidArgument, "bot files: store directory is required")
	}
	if _, err := ConversationID(id); err != nil {
		return nil, err
	}
	files := &Files{storeDir: filepath.Clean(storeDir), id: id}
	files.fs = &privateFS{owner: files}
	return files, nil
}

// FilesRoot returns the files directory for one Bot. The result is
// lexical; the directory is untrusted until it is opened.
func FilesRoot(storeDir, id string) (string, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return "", errorcode.New(errorcode.InvalidArgument, "bot files: store directory is required")
	}
	if _, err := ConversationID(id); err != nil {
		return "", err
	}
	return filepath.Join(storeDir, botFilesDir, id, filesDirName), nil
}

// Init provisions the files for a newly created Bot: it ensures the root
// directory exists and writes NotebookIndex only when that file is absent. It
// never overwrites an existing index. Ordinary activation uses Prepare and must
// not recreate a deleted index.
func (n *Files) Init(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.openMu.Lock()
	defer n.openMu.Unlock()
	return n.initLocked()
}

// Ensure provisions the private file area on first use. Existing directories
// are not repaired or reseeded; Prepare validates the filesystem boundary.
func (n *Files) Ensure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := n.path()
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

func (n *Files) initLocked() error {
	root, err := n.prepareLocked(true)
	if err != nil {
		return err
	}
	if n.workID != "" {
		return nil
	}
	// Create-or-revalidate through one exclusive create: an existing index is
	// never assumed to be a provisioned index.
	file, err := root.OpenFile(NotebookIndex, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filesFileMode)
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
		return errorcode.New(errorcode.FailedPrecondition, "bot files: index is not a regular file")
	}
	return nil
}

// Prepare opens the existing files root. It never creates the root or the
// index and fails closed when the file area has not been initialized.
func (n *Files) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.openMu.Lock()
	defer n.openMu.Unlock()
	_, err := n.prepareLocked(false)
	return err
}

func (n *Files) prepareLocked(create bool) (*os.Root, error) {
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

// openRoot opens the store root, then walks each files path component with
// os.Root, rejecting symlinks and any component that changes between the
// pre-open and post-open checks.
func (n *Files) openRoot(create bool) (*os.Root, string, error) {
	base, err := filepath.EvalSymlinks(n.storeDir)
	if err != nil {
		return nil, "", errorcode.Wrap(errorcode.FailedPrecondition, "bot files: resolve store directory", err)
	}
	storeRoot, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", errorcode.Wrap(errorcode.FailedPrecondition, "bot files: open store directory", err)
	}
	current := storeRoot
	parts := []string{botFilesDir, n.id, filesDirName}
	if n.workID != "" {
		parts = []string{botFilesDir, n.id, "work", n.workID, "files"}
	}
	for _, name := range parts {
		child, childErr := openFilesChild(current, name, create)
		_ = current.Close()
		if childErr != nil {
			return nil, "", childErr
		}
		current = child
	}
	return current, filepath.Join(append([]string{base}, parts...)...), nil
}

func openFilesChild(parent *os.Root, name string, create bool) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, errorcode.Wrap(errorcode.FailedPrecondition, "bot files is not provisioned", err)
		}
		if err := parent.Mkdir(name, filesDirMode); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, errorcode.Wrap(errorcode.Internal, "bot files: create directory", err)
		}
		before, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, errorcode.Wrap(errorcode.FailedPrecondition, "bot files: inspect directory", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, errorcode.New(errorcode.FailedPrecondition, "bot files: path component is not a real directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, errorcode.Wrap(errorcode.FailedPrecondition, "bot files: open directory", err)
	}
	opened, statErr := child.Stat(".")
	after, lstatErr := parent.Lstat(name)
	if statErr != nil || lstatErr != nil ||
		after.Mode()&os.ModeSymlink != 0 || !after.IsDir() ||
		!os.SameFile(before, after) || !os.SameFile(opened, after) {
		_ = child.Close()
		return nil, errorcode.New(errorcode.FailedPrecondition, "bot files: path component changed while opening")
	}
	return child, nil
}

// Tools returns the five builtin file tools bound to this file area. Each call
// is serialized on the files so revision checks and Patch planning cannot
// interleave with another call.
func (n *Files) Tools() ([]tool.Tool, error) {
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
		&privateFileTool{owner: n, Tool: read},
		&privateFileTool{owner: n, Tool: write},
		&privateFileTool{owner: n, Tool: patch},
		&privateFileTool{owner: n, Tool: glob},
		&privateFileTool{owner: n, Tool: search},
	}, nil
}

// Close waits for in-flight calls to finish and releases the root handle. The
// files stays usable and reopens lazily on the next call.
func (n *Files) Close() error {
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
	_ sandbox.Runtime           = (*Files)(nil)
	_ sandbox.PreparableRuntime = (*Files)(nil)
	_ tool.Tool                 = (*privateFileTool)(nil)
)

type privateFileTool struct {
	owner *Files
	tool.Tool
}

// privateFileDescriptions replace model-facing advice that references tools the
// files does not admit, without changing any tool schema.
var privateFileDescriptions = map[string]string{
	filesystem.GlobToolName:   "Find files matching a glob pattern. Use * for direct children and **/* for recursive discovery. Results are capped at 100 files.",
	filesystem.SearchToolName: "Search file contents with a regular expression. Matching is case-sensitive by default; use (?i) in pattern for case-insensitive search. Results are capped at 100 lines; a line over 8MiB fails the search rather than reporting no matches. Use Read for surrounding context.",
}

// SandboxRuntime exposes the files as the tool's sandbox descriptor source,
// matching the runtime policy layer's sandboxRuntimeProvider contract.
func (t *privateFileTool) SandboxRuntime() sandbox.Runtime { return t.owner }

// Definition reports the builtin schema with any files-inapplicable advice
// replaced.
func (t *privateFileTool) Definition() tool.Definition {
	definition := t.Tool.Definition()
	if description, ok := privateFileDescriptions[definition.Name]; ok {
		definition.Description = description
	}
	return definition
}

func (t *privateFileTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	t.owner.callMu.Lock()
	defer t.owner.callMu.Unlock()
	if err := t.owner.Prepare(ctx); err != nil {
		return tool.Result{}, err
	}
	return t.Tool.Call(ctx, call)
}

func (n *Files) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:      privateFilesBackend,
		Isolation:    sandbox.IsolationProcess,
		Capabilities: sandbox.CapabilitySet{FileSystem: true},
		DefaultConstraints: sandbox.Constraints{
			Backend:    privateFilesBackend,
			Permission: sandbox.PermissionWorkspaceWrite,
			Isolation:  sandbox.IsolationProcess,
			Network:    sandbox.NetworkDisabled,
		},
	}
}

func (n *Files) FileSystem() sandbox.FileSystem { return n.fs }

func (n *Files) FileSystemFor(sandbox.Constraints) sandbox.FileSystem { return n.fs }

func (n *Files) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, ErrFilesCommandDenied
}

func (n *Files) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, ErrFilesCommandDenied
}

func (n *Files) OpenSession(string) (sandbox.Session, error) {
	return nil, ErrFilesCommandDenied
}

func (n *Files) OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error) {
	return nil, ErrFilesCommandDenied
}

func (n *Files) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{privateFilesBackend}
}

func (n *Files) Status() sandbox.Status {
	return sandbox.Status{RequestedBackend: privateFilesBackend, ResolvedBackend: privateFilesBackend}
}

// NewWorkFiles binds a Control-allocated work handle to a separate private
// workspace. It cannot select a user-provided directory or another Bot's files.
func NewWorkFiles(storeDir, botID, workID string) (*Files, error) {
	if !validWorkID(workID) {
		return nil, errorcode.New(errorcode.InvalidArgument, "bot: invalid work identity")
	}
	files, err := NewFiles(storeDir, botID)
	if err != nil {
		return nil, err
	}
	files.workID = workID
	return files, nil
}

func (n *Files) path() (string, error) {
	if n.workID != "" {
		return filepath.Join(n.storeDir, botFilesDir, n.id, "work", n.workID, "files"), nil
	}
	return FilesRoot(n.storeDir, n.id)
}
