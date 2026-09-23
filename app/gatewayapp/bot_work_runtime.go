package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
)

// botWorkRuntime combines rooted file access with an immutable native process
// ceiling. Approval never permits Host execution, ambient credentials or paths.
type botWorkRuntime struct {
	sandbox.Runtime
	files *bot.Files
	cwd   string
}

func newBotWorkRuntime(ctx context.Context, files *bot.Files, storeDir string) (*botWorkRuntime, error) {
	if err := files.Prepare(ctx); err != nil {
		return nil, err
	}
	cwd, err := files.FileSystem().Getwd()
	if err != nil {
		return nil, err
	}
	route, err := sandboxrouter.Current("")
	if err != nil {
		return nil, err
	}
	roots := []string{cwd, "/bin", "/usr", "/sbin"}
	if goruntime.GOOS == "darwin" {
		roots = append(roots, "/System/Library", "/Library/Frameworks", "/private/etc/passwd", "/private/etc/group", "/private/etc/localtime")
	} else {
		roots = append(roots, "/lib", "/lib64", "/etc/passwd", "/etc/group", "/etc/ld.so.cache", "/etc/localtime")
	}
	securedStore, err := filepath.EvalSymlinks(storeDir)
	if err != nil {
		return nil, err
	}
	var readable []string
	for _, root := range roots {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			if root != cwd {
				rel, err := filepath.Rel(resolved, securedStore)
				if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return nil, errors.New("bot: Host store overlaps system runtime read roots")
				}
			}
			readable = append(readable, resolved)
			// Bubblewrap starts with an empty mount tree. Keep system aliases
			// such as /bin and /lib64 available for shells and ELF interpreters.
			if goruntime.GOOS == "linux" && resolved != root {
				readable = append(readable, root)
			}
		}
	}
	rt, err := sandbox.New(sandbox.Config{CWD: cwd, RequestedBackend: route.Backend, WritableRoots: []string{cwd}, ResourceLimits: &sandbox.ResourceLimits{ReadPaths: readable, WritePaths: []string{cwd}, Network: sandbox.NetworkDisabled}})
	if err != nil {
		return nil, err
	}
	return &botWorkRuntime{Runtime: rt, files: files, cwd: cwd}, nil
}
func (r *botWorkRuntime) FileSystem() sandbox.FileSystem { return r.files.FileSystem() }
func (r *botWorkRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return r.files.FileSystem()
}
func (r *botWorkRuntime) Prepare(ctx context.Context) error {
	if err := r.files.Prepare(ctx); err != nil {
		return err
	}
	if p, ok := r.Runtime.(sandbox.PreparableRuntime); ok {
		return p.Prepare(ctx)
	}
	return nil
}
func (r *botWorkRuntime) Close() error { return errors.Join(r.Runtime.Close(), r.files.Close()) }
func (r *botWorkRuntime) command(req sandbox.CommandRequest) (sandbox.CommandRequest, error) {
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
	rel, err := filepath.Rel(r.cwd, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return req, errors.New("bot: command directory is outside owned work")
	}
	req.Dir = resolved
	// Native runners merge overrides with the ambient environment. Override
	// every inherited key before admitting only the fixed execution environment.
	req.Env = map[string]string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		req.Env[key] = ""
	}
	req.Env["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin"
	req.Env["HOME"] = r.cwd
	req.Env["TMPDIR"] = r.cwd
	req.Env["ZDOTDIR"] = r.cwd
	return req, nil
}
func (r *botWorkRuntime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	req, err := r.command(req)
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	return r.Runtime.Run(ctx, req)
}
func (r *botWorkRuntime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	req, err := r.command(req)
	if err != nil {
		return nil, err
	}
	return r.Runtime.Start(ctx, req)
}
