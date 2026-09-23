package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
)

// validateApplicationExecutionPlatform rejects workspace-write when the Host
// cannot enforce its mandatory credential and process-information read ceiling.
// Linux Bubblewrap remains a candidate; the current Darwin Seatbelt policy
// does not block tested KERN_PROCARGS2 reads, and Windows lacks this read
// ceiling. Tools-only profiles remain available without a native executor.
func validateApplicationExecutionPlatform(execution string) error {
	switch execution {
	case "tools-only":
		return nil
	case "workspace-write":
		if goruntime.GOOS == "linux" {
			return nil
		}
		return errorcode.New(errorcode.Unsupported, "gatewayapp: application workspace-write requires enforceable credential and process-information read isolation; this platform supports tools-only")
	default:
		return errorcode.New(errorcode.InvalidArgument, "gatewayapp: invalid application execution profile")
	}
}

// isolatedExecutionRuntime enforces a native, non-overridable filesystem and
// network ceiling on supported application execution. Native approvals cannot
// widen it. Its directory is disposable, never a resource store.
type isolatedExecutionRuntime struct {
	sandbox.Runtime
	cwd   string
	lease *application.Store
	scope application.Scope
}

func newIsolatedExecutionRuntime(cwd, storeDir string, lease *application.Store, scope application.Scope) (*isolatedExecutionRuntime, error) {
	if err := validateApplicationExecutionPlatform("workspace-write"); err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, errors.New("gatewayapp: application execution lease is required")
	}
	securedStore, err := filepath.EvalSymlinks(storeDir)
	if err != nil {
		return nil, err
	}
	securedCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	if securedCWD != filepath.Clean(cwd) || !pathWithin(securedStore, securedCWD) || securedCWD == securedStore {
		return nil, errors.New("gatewayapp: application execution workspace escapes Host store")
	}
	route, err := sandboxrouter.Current("")
	if err != nil {
		return nil, err
	}
	roots := []string{securedCWD, "/bin", "/usr/bin", "/sbin", "/usr/sbin", "/lib", "/lib64", "/usr/lib", "/usr/lib64", "/etc/passwd", "/etc/group", "/etc/ld.so.cache", "/etc/localtime"}
	var readable []string
	for _, root := range roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if root != securedCWD && pathWithin(resolved, securedStore) {
			return nil, errors.New("gatewayapp: Host store overlaps execution system read roots")
		}
		readable = append(readable, resolved)
		// Bubblewrap starts with an empty mount tree and needs system aliases.
		if resolved != root {
			readable = append(readable, root)
		}
	}
	rt, err := sandbox.New(sandbox.Config{CWD: securedCWD, RequestedBackend: route.Backend,
		WritableRoots: []string{securedCWD}, ResourceLimits: &sandbox.ResourceLimits{
			ReadPaths: readable, WritePaths: []string{securedCWD}, Network: sandbox.NetworkDisabled,
		}})
	if err != nil {
		return nil, err
	}
	return &isolatedExecutionRuntime{Runtime: rt, cwd: securedCWD, lease: lease, scope: scope}, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *isolatedExecutionRuntime) command(req sandbox.CommandRequest) (sandbox.CommandRequest, error) {
	if req.Dir == "" {
		req.Dir = r.cwd
	}
	if !filepath.IsAbs(req.Dir) {
		req.Dir = filepath.Join(r.cwd, req.Dir)
	}
	resolved, err := filepath.EvalSymlinks(req.Dir)
	if err != nil {
		return req, err
	}
	if !pathWithin(r.cwd, resolved) {
		return req, errors.New("gatewayapp: command directory escapes application execution workspace")
	}
	req.Dir = resolved
	// Native runners merge overrides with the ambient environment. Clear each
	// inherited value before adding only fixed, non-credential process settings.
	req.Env = map[string]string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		req.Env[key] = ""
	}
	req.Env["PATH"] = "/usr/bin:/bin"
	req.Env["HOME"] = r.cwd
	req.Env["TMPDIR"] = r.cwd
	req.Env["ZDOTDIR"] = r.cwd
	return req, nil
}

func (r *isolatedExecutionRuntime) checkActive(ctx context.Context) error {
	if r.lease == nil {
		return errors.New("gatewayapp: application execution lease is unavailable")
	}
	return r.lease.CheckActive(ctx, r.scope)
}

func (r *isolatedExecutionRuntime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
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

func (r *isolatedExecutionRuntime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
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

func (r *isolatedExecutionRuntime) OpenSession(id string) (sandbox.Session, error) {
	opened, err := r.Runtime.OpenSession(id)
	if err != nil {
		return nil, err
	}
	return applicationExecutionSession{Session: opened, runtime: r}, nil
}

func (r *isolatedExecutionRuntime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	opened, err := r.Runtime.OpenSessionRef(ref)
	if err != nil {
		return nil, err
	}
	return applicationExecutionSession{Session: opened, runtime: r}, nil
}

type applicationExecutionSession struct {
	sandbox.Session
	runtime *isolatedExecutionRuntime
}

func (s applicationExecutionSession) WriteInput(ctx context.Context, input []byte) error {
	if err := s.runtime.checkActive(ctx); err != nil {
		return err
	}
	return s.Session.WriteInput(ctx, input)
}
