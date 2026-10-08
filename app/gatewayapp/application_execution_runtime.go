package gatewayapp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
	"github.com/caelis-labs/caelis/internal/workspaceidentity"
)

// validateApplicationExecutionPlatform requires an ordinary native backend for
// application execution, using the same platform selection as ordinary Sessions.
func validateApplicationExecutionPlatform(execution string) error {
	switch execution {
	case "tools-only":
		return nil
	case "workspace-write":
		if _, err := sandboxrouter.Current(""); err != nil {
			return errorcode.Wrap(errorcode.Unsupported, "gatewayapp: application native execution requires a supported native sandbox", err)
		}
		return nil
	default:
		return errorcode.New(errorcode.InvalidArgument, "gatewayapp: invalid application execution profile")
	}
}

// applicationExecutionRuntime leases the ordinary native sandbox to one bound
// application Session. Its directory may be application-owned and persistent;
// Host execution remains subject to normal per-call approval policy.
type applicationExecutionRuntime struct {
	sandbox.Runtime
	cwd        string
	access     []string
	fullAccess bool
	lease      *application.Store
	scope      application.Scope
}

func newApplicationExecutionRuntime(cwd, storeDir, authorityDir string, lease *application.Store, scope application.Scope, profile application.Profile, execution *sandbox.ExecutionConfig) (*applicationExecutionRuntime, error) {
	if err := validateApplicationExecutionPlatform("workspace-write"); err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, errors.New("gatewayapp: application execution lease is required")
	}
	if err := validateApplicationDirectory(cwd); err != nil {
		return nil, err
	}
	for _, entry := range profile.Workspace.Access {
		if err := validateApplicationDirectory(entry.Path); err != nil {
			return nil, err
		}
	}
	securedCWD := filepath.Clean(cwd)
	writable, access, err := applicationWorkspaceAccess(profile.Workspace.Access, securedCWD)
	if err != nil {
		return nil, err
	}
	backend := sandbox.Backend("")
	if profile.Permissions.Mode == "danger-full-access" {
		backend = sandbox.BackendHost
	}
	route, err := sandboxrouter.Current(backend)
	if err != nil {
		return nil, err
	}
	// Ordinary native policy: ambient reads and normal approval-based Host
	// escalation. CWD and explicitly selected read-write directories are the
	// default write grants; read-only directories add no write authority.
	rt, err := sandbox.New(sandbox.Config{CWD: securedCWD, StateDir: storeDir,
		HostAuthorityDir: authorityDir,
		Execution:        execution, BaseEnv: runtimeCommandEnvironment(),
		RequestedBackend: route.Backend, BackendCandidates: route.BackendCandidates,
		FallbackInstallHint: route.InstallHint, WritableRoots: writable})
	if err != nil {
		return nil, err
	}
	return &applicationExecutionRuntime{Runtime: rt, cwd: securedCWD, access: access, fullAccess: backend == sandbox.BackendHost, lease: lease, scope: scope}, nil
}

// validateApplicationDirectory rejects redirection of a creation-bound canonical
// directory, including Windows junctions, at activation and before later effects.
func validateApplicationDirectory(path string) error {
	resolved, err := workspaceidentity.CanonicalDirectory(path)
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(path) {
		return errors.New("gatewayapp: application directory binding changed since Session admission")
	}
	return nil
}

// applicationWorkspaceAccess resolves and verifies additional directories at
// creation and again at activation. The bound CWD is always writable; read-only
// entries only identify working directories, not a hard read ceiling.
func applicationWorkspaceAccess(entries []application.WorkspaceAccess, cwd string) ([]string, []string, error) {
	writable := []string{cwd}
	access := make([]string, 0, len(entries))
	seen := map[string]string{cwd: "read-write"}
	for _, entry := range entries {
		if !filepath.IsAbs(entry.Path) || (entry.Mode != "read-only" && entry.Mode != "read-write") {
			return nil, nil, errors.New("gatewayapp: application access requires absolute directory and supported mode")
		}
		resolved, err := workspaceidentity.CanonicalDirectory(entry.Path)
		if err != nil {
			return nil, nil, err
		}
		if previous, exists := seen[resolved]; exists {
			if previous != entry.Mode {
				return nil, nil, errors.New("gatewayapp: conflicting access permissions for one directory")
			}
			continue
		}
		seen[resolved] = entry.Mode
		access = append(access, resolved)
		if entry.Mode == "read-write" {
			writable = append(writable, resolved)
		}
	}
	return writable, access, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *applicationExecutionRuntime) command(req sandbox.CommandRequest) (sandbox.CommandRequest, error) {
	if req.Dir == "" {
		req.Dir = r.cwd
	}
	if !filepath.IsAbs(req.Dir) {
		req.Dir = filepath.Join(r.cwd, req.Dir)
	}
	resolved, err := workspaceidentity.CanonicalDirectory(req.Dir)
	if err != nil {
		return req, err
	}
	// A policy-approved require_escalated call uses the ordinary Host route;
	// its workdir may be outside the default declared sandbox directories.
	allowed := r.fullAccess || req.Constraints.Route == sandbox.RouteHost || pathWithin(r.cwd, resolved)
	for _, root := range r.access {
		allowed = allowed || pathWithin(root, resolved)
	}
	if !allowed {
		return req, errors.New("gatewayapp: command working directory is outside configured application directories")
	}
	req.Dir = resolved
	return req, nil
}

// FileSystemFor retains the selected SDK policy filesystem while checking the
// application lease at every filesystem effect, including after approval resumes.
func (r *applicationExecutionRuntime) FileSystemFor(constraints sandbox.Constraints) sandbox.FileSystem {
	if provider, ok := r.Runtime.(sandbox.FileSystemProvider); ok {
		return applicationLeasedFileSystem{FileSystem: provider.FileSystemFor(constraints), runtime: r}
	}
	return applicationLeasedFileSystem{FileSystem: r.Runtime.FileSystem(), runtime: r}
}

func (r *applicationExecutionRuntime) FileSystem() sandbox.FileSystem {
	return applicationLeasedFileSystem{FileSystem: r.Runtime.FileSystem(), runtime: r}
}

type applicationLeasedFileSystem struct {
	sandbox.FileSystem
	runtime *applicationExecutionRuntime
}

func (f applicationLeasedFileSystem) Open(path string) (*os.File, error) {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return nil, err
	}
	return f.FileSystem.Open(path)
}
func (f applicationLeasedFileSystem) ReadDir(path string) ([]os.DirEntry, error) {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return nil, err
	}
	return f.FileSystem.ReadDir(path)
}
func (f applicationLeasedFileSystem) Stat(path string) (os.FileInfo, error) {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return nil, err
	}
	return f.FileSystem.Stat(path)
}
func (f applicationLeasedFileSystem) ReadFile(path string) ([]byte, error) {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return nil, err
	}
	return f.FileSystem.ReadFile(path)
}
func (f applicationLeasedFileSystem) WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return err
	}
	return f.FileSystem.WriteFile(path, data, mode)
}
func (f applicationLeasedFileSystem) MkdirAll(path string, mode os.FileMode) error {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return err
	}
	fsys, ok := f.FileSystem.(interface {
		MkdirAll(string, os.FileMode) error
	})
	if !ok {
		return errors.New("gatewayapp: sandbox filesystem cannot create directories")
	}
	return fsys.MkdirAll(path, mode)
}
func (f applicationLeasedFileSystem) Glob(pattern string) ([]string, error) {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return nil, err
	}
	return f.FileSystem.Glob(pattern)
}
func (f applicationLeasedFileSystem) WalkDir(path string, visit fs.WalkDirFunc) error {
	if err := f.runtime.checkActive(context.Background()); err != nil {
		return err
	}
	return f.FileSystem.WalkDir(path, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := f.runtime.checkActive(context.Background()); err != nil {
			return err
		}
		return visit(path, entry, walkErr)
	})
}

func (r *applicationExecutionRuntime) checkActive(ctx context.Context) error {
	if r.lease == nil {
		return errors.New("gatewayapp: application execution lease is unavailable")
	}
	if err := r.lease.CheckActive(ctx, r.scope); err != nil {
		return err
	}
	if err := validateApplicationDirectory(r.cwd); err != nil {
		return err
	}
	for _, root := range r.access {
		if err := validateApplicationDirectory(root); err != nil {
			return err
		}
	}
	return nil
}

func (r *applicationExecutionRuntime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if err := r.checkActive(ctx); err != nil {
		return sandbox.CommandResult{}, err
	}
	req, err := r.command(req)
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	if err := r.checkActive(ctx); err != nil {
		return sandbox.CommandResult{}, err
	}
	return r.Runtime.Run(ctx, req)
}

func (r *applicationExecutionRuntime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if err := r.checkActive(ctx); err != nil {
		return nil, err
	}
	req, err := r.command(req)
	if err != nil {
		return nil, err
	}
	if err := r.checkActive(ctx); err != nil {
		return nil, err
	}
	opened, err := r.Runtime.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	return applicationExecutionSession{Session: opened, runtime: r}, nil
}

func (r *applicationExecutionRuntime) OpenSession(id string) (sandbox.Session, error) {
	opened, err := r.Runtime.OpenSession(id)
	if err != nil {
		return nil, err
	}
	return applicationExecutionSession{Session: opened, runtime: r}, nil
}

func (r *applicationExecutionRuntime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	opened, err := r.Runtime.OpenSessionRef(ref)
	if err != nil {
		return nil, err
	}
	return applicationExecutionSession{Session: opened, runtime: r}, nil
}

type applicationExecutionSession struct {
	sandbox.Session
	runtime *applicationExecutionRuntime
}

func (s applicationExecutionSession) WriteInput(ctx context.Context, input []byte) error {
	if err := s.runtime.checkActive(ctx); err != nil {
		return err
	}
	return s.Session.WriteInput(ctx, input)
}
