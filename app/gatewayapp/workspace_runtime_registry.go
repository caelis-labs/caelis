package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controlclient "github.com/caelis-labs/caelis/control/client"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// workspaceRuntime owns the live composition for one canonical workspace.
// Session remains the external interaction unit; this value is only an
// internal routing and lifecycle boundary.
type workspaceRuntime struct {
	workspace session.WorkspaceRef
	stack     *Stack
}

// sessionRuntime binds one durable Session identity to exactly one workspace
// Runtime. UserID deliberately does not participate in Runtime identity.
type sessionRuntime struct {
	sessionID string
	workspace *workspaceRuntime
}

// workspaceRuntimeRegistry is the app-scoped owner of workspace and Session
// Runtime bindings. Durable Session rows remain the source of truth; the maps
// are process-local live composition state reconstructed on demand.
type workspaceRuntimeRegistry struct {
	owner *Stack

	mu         sync.RWMutex
	workspaces map[string]*workspaceRuntime
	cwdToKey   map[string]string
	closed     bool
	building   int
	buildsIdle chan struct{}
}

func newWorkspaceRuntimeRegistry(owner *Stack) (*workspaceRuntimeRegistry, error) {
	if owner == nil {
		return nil, errors.New("gatewayapp: workspace runtime owner is required")
	}
	workspace, err := canonicalWorkspaceRef(owner.Workspace, session.WorkspaceRef{})
	if err != nil {
		return nil, err
	}
	owner.Workspace = workspace
	runtime := &workspaceRuntime{workspace: workspace, stack: owner}
	buildsIdle := make(chan struct{})
	close(buildsIdle)
	return &workspaceRuntimeRegistry{
		owner:      owner,
		workspaces: map[string]*workspaceRuntime{workspace.Key: runtime},
		cwdToKey:   map[string]string{workspace.CWD: workspace.Key},
		buildsIdle: buildsIdle,
	}, nil
}

func (r *workspaceRuntimeRegistry) resolveWorkspace(
	ctx context.Context,
	requested session.WorkspaceRef,
) (*workspaceRuntime, error) {
	if r == nil || r.owner == nil {
		return nil, errors.New("gatewayapp: workspace runtime registry is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workspace, err := canonicalWorkspaceRef(requested, r.owner.Workspace)
	if err != nil {
		return nil, err
	}

	if existing, found, err := r.loadedWorkspace(workspace); found || err != nil {
		return existing, err
	}

	buildCtx, finishBuild, err := r.beginWorkspaceBuild(ctx)
	if err != nil {
		return nil, err
	}
	defer finishBuild()

	// Runtime construction changes the Host's live generation set. Serialize it
	// with app-wide reconfiguration, but never hold the registry lock across the
	// comparatively expensive composition build.
	gate := r.owner.reconfigureLock()
	gate.Lock()
	defer gate.Unlock()
	if existing, found, err := r.loadedWorkspace(workspace); found || err != nil {
		return existing, err
	}

	stack, err := r.owner.newWorkspaceStackLocked(buildCtx, workspace)
	if err != nil {
		return nil, err
	}
	resolved := &workspaceRuntime{workspace: workspace, stack: stack}

	r.mu.Lock()
	if r.closed || r.owner.isClosing() {
		r.mu.Unlock()
		_ = stack.closeWorkspaceResources()
		return nil, workspaceHostClosingError()
	}
	r.workspaces[workspace.Key] = resolved
	r.cwdToKey[workspace.CWD] = workspace.Key
	r.mu.Unlock()
	return resolved, nil
}

func (r *workspaceRuntimeRegistry) loadedWorkspace(
	workspace session.WorkspaceRef,
) (*workspaceRuntime, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.owner.isClosing() {
		return nil, false, workspaceHostClosingError()
	}
	if existing := r.workspaces[workspace.Key]; existing != nil {
		if existing.workspace.CWD != workspace.CWD {
			return nil, false, errorcode.New(
				errorcode.InvalidArgument,
				fmt.Sprintf(
					"gatewayapp: workspace key %q is already bound to %q, not %q",
					workspace.Key,
					existing.workspace.CWD,
					workspace.CWD,
				),
			)
		}
		return existing, true, nil
	}
	if existingKey := r.cwdToKey[workspace.CWD]; existingKey != "" {
		return nil, false, errorcode.New(
			errorcode.InvalidArgument,
			fmt.Sprintf(
				"gatewayapp: workspace directory %q is already bound as %q, not %q",
				workspace.CWD,
				existingKey,
				workspace.Key,
			),
		)
	}
	return nil, false, nil
}

func (r *workspaceRuntimeRegistry) resolveCreateWorkspace(
	ctx context.Context,
	principal controlclient.Principal,
	requested session.WorkspaceRef,
	preferredSessionID string,
) (*workspaceRuntime, error) {
	if r == nil || r.owner == nil || r.owner.Sessions == nil {
		return nil, errors.New("gatewayapp: workspace runtime registry is unavailable")
	}
	workspace, err := canonicalWorkspaceRef(requested, r.owner.Workspace)
	if err != nil {
		return nil, err
	}
	preferredSessionID = strings.TrimSpace(preferredSessionID)
	if preferredSessionID != "" {
		existing, loadErr := r.owner.Sessions.Session(ctx, session.SessionRef{SessionID: preferredSessionID})
		switch {
		case loadErr == nil:
			if !principal.HasRole("admin") &&
				strings.TrimSpace(existing.UserID) != strings.TrimSpace(principal.ID) {
				return nil, controlclient.ErrUnauthorized
			}
			actual, canonicalErr := canonicalWorkspaceRef(session.WorkspaceRef{
				Key: existing.WorkspaceKey,
				CWD: existing.CWD,
			}, session.WorkspaceRef{})
			if canonicalErr != nil {
				return nil, canonicalErr
			}
			if actual != workspace {
				return nil, errorcode.New(
					errorcode.InvalidArgument,
					fmt.Sprintf(
						"gatewayapp: Session %q already belongs to workspace %q at %q",
						preferredSessionID,
						actual.Key,
						actual.CWD,
					),
				)
			}
		case !errors.Is(loadErr, session.ErrSessionNotFound):
			return nil, loadErr
		}
	}
	return r.resolveWorkspace(ctx, workspace)
}

func (r *workspaceRuntimeRegistry) resolveSession(
	ctx context.Context,
	sessionID string,
) (*sessionRuntime, session.Session, error) {
	if r == nil || r.owner == nil || r.owner.Sessions == nil {
		return nil, session.Session{}, errors.New("gatewayapp: Session runtime registry is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, session.Session{}, errors.New("gatewayapp: Session ID is required")
	}
	active, err := r.owner.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		return nil, session.Session{}, err
	}
	workspace, err := r.resolveWorkspace(ctx, session.WorkspaceRef{
		Key: active.WorkspaceKey,
		CWD: active.CWD,
	})
	if err != nil {
		return nil, active, err
	}
	binding, err := newSessionRuntime(active, workspace)
	if err != nil {
		return nil, active, err
	}
	return binding, active, nil
}

func newSessionRuntime(
	active session.Session,
	workspace *workspaceRuntime,
) (*sessionRuntime, error) {
	if workspace == nil || workspace.stack == nil {
		return nil, errors.New("gatewayapp: workspace runtime binding is unavailable")
	}
	sessionID := strings.TrimSpace(active.SessionID)
	if sessionID == "" {
		return nil, errors.New("gatewayapp: cannot bind an empty Session ID")
	}
	actual, err := canonicalWorkspaceRef(session.WorkspaceRef{
		Key: active.WorkspaceKey,
		CWD: active.CWD,
	}, session.WorkspaceRef{})
	if err != nil {
		return nil, err
	}
	if actual != workspace.workspace {
		return nil, fmt.Errorf(
			"gatewayapp: Session %q belongs to workspace %q at %q, not %q at %q",
			sessionID,
			actual.Key,
			actual.CWD,
			workspace.workspace.Key,
			workspace.workspace.CWD,
		)
	}
	return &sessionRuntime{sessionID: sessionID, workspace: workspace}, nil
}

func (r *workspaceRuntimeRegistry) snapshot() []*workspaceRuntime {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*workspaceRuntime, 0, len(r.workspaces))
	for _, runtime := range r.workspaces {
		out = append(out, runtime)
	}
	return out
}

func (r *workspaceRuntimeRegistry) closeAdmission(ctx context.Context) ([]*workspaceRuntime, error) {
	if r == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	r.closed = true
	buildsIdle := r.buildsIdle
	r.mu.Unlock()
	select {
	case <-buildsIdle:
		return r.snapshot(), nil
	case <-ctx.Done():
		return r.snapshot(), ctx.Err()
	}
}

func (r *workspaceRuntimeRegistry) beginWorkspaceBuild(
	ctx context.Context,
) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed || r.owner == nil || r.owner.isClosing() {
		r.mu.Unlock()
		return nil, nil, workspaceHostClosingError()
	}
	if r.building == 0 {
		r.buildsIdle = make(chan struct{})
	}
	r.building++
	lifecycleCtx := r.owner.lifecycleCtx
	r.mu.Unlock()

	buildCtx, cancel := context.WithCancel(ctx)
	stopLifecycleCancel := func() bool { return false }
	if lifecycleCtx != nil {
		stopLifecycleCancel = context.AfterFunc(lifecycleCtx, cancel)
	}
	return buildCtx, func() {
		stopLifecycleCancel()
		cancel()
		r.mu.Lock()
		r.building--
		if r.building == 0 {
			close(r.buildsIdle)
		}
		r.mu.Unlock()
	}, nil
}

func workspaceHostClosingError() error {
	return errorcode.New(errorcode.Unavailable, "gatewayapp: host is closing")
}

func (r *workspaceRuntimeRegistry) invalidatePlacementSnapshots() {
	for _, runtime := range r.snapshot() {
		if runtime == nil || runtime.stack == nil {
			continue
		}
		runtime.stack.invalidateOwnPlacementSnapshot()
	}
}

// newWorkspaceStackLocked builds one workspace-scoped composition. The caller
// must hold reconfigureLock so the cloned generation cannot race app-wide
// configuration mutation.
func (s *Stack) newWorkspaceStackLocked(
	ctx context.Context,
	workspace session.WorkspaceRef,
) (*Stack, error) {
	if s == nil {
		return nil, errors.New("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	runtimeConfig := cloneWorkspaceRuntimeConfig(s.runtime)
	sandboxConfig := cloneSandboxConfig(s.sandbox)
	s.mu.RUnlock()

	child := &Stack{Workspace: workspace, runtime: runtimeConfig, sandbox: sandboxConfig}
	s.shareWorkspaceHostState(child)
	if err := child.rebuildGatewayLockedContext(ctx); err != nil {
		return nil, fmt.Errorf(
			"gatewayapp: build workspace runtime %q at %q: %w",
			workspace.Key,
			workspace.CWD,
			err,
		)
	}
	return child, nil
}

// shareWorkspaceHostState is the single contract for state shared with a
// workspace composition Stack. Host-only registries, public client facades,
// operation stores, Task stream adapters, cancellation ownership, and test
// seams deliberately remain unset on the child.
func (s *Stack) shareWorkspaceHostState(child *Stack) {
	child.Sessions = s.Sessions
	child.AppName = s.AppName
	child.UserID = s.UserID // Compatibility only; not a Runtime partition key.
	child.lookup = s.lookup
	child.store = s.store
	child.storeDir = s.storeDir
	child.leaseOwnerID = s.leaseOwnerID
	child.reconfigureGate = s.reconfigureLock()
	child.assemblyMutationGate = s.assemblyMutationLock()
	child.taskStore = s.taskStore
	child.controlFeeds = s.controlFeeds
	child.approvalRecovery = s.approvalRecovery
	child.lifecycleCtx = s.lifecycleCtx
	child.codexAuth = s.codexAuth
	child.grokAuth = s.grokAuth
	child.apiKeyCredentials = s.apiKeyCredentials
	child.providerUsage = s.providerUsage
}

func cloneWorkspaceRuntimeConfig(config stackRuntimeConfig) stackRuntimeConfig {
	config.SkillDirs = cloneStringSlicePreserveNil(config.SkillDirs)
	config.Plugins = clonePluginConfigs(config.Plugins)
	config.BaseAssembly = assembly.CloneResolvedAssembly(config.BaseAssembly)
	config.Assembly = assembly.CloneResolvedAssembly(config.BaseAssembly)
	config.PluginSkills = nil
	config.SkillCatalog = skill.Catalog{}
	config.BaseMetadata = nil
	config.EstimatedPromptPrefixTokens = 0
	return config
}

func cloneSandboxConfig(config SandboxConfig) SandboxConfig {
	config.WritableRoots = append([]string(nil), config.WritableRoots...)
	config.ReadOnlySubpaths = append([]string(nil), config.ReadOnlySubpaths...)
	return config
}

func canonicalWorkspaceRef(
	requested session.WorkspaceRef,
	fallback session.WorkspaceRef,
) (session.WorkspaceRef, error) {
	key := strings.TrimSpace(requested.Key)
	cwd := strings.TrimSpace(requested.CWD)
	if key == "" && cwd == "" {
		key = strings.TrimSpace(fallback.Key)
		cwd = strings.TrimSpace(fallback.CWD)
	} else if cwd == "" && key == strings.TrimSpace(fallback.Key) {
		cwd = strings.TrimSpace(fallback.CWD)
	}
	if cwd == "" {
		return session.WorkspaceRef{}, errorcode.New(errorcode.InvalidArgument, "gatewayapp: workspace CWD is required")
	}
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return session.WorkspaceRef{}, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: resolve workspace CWD", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return session.WorkspaceRef{}, errorcode.Wrap(
			errorcode.InvalidArgument,
			fmt.Sprintf("gatewayapp: resolve workspace CWD %q", cwd),
			err,
		)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return session.WorkspaceRef{}, errorcode.Wrap(
			errorcode.InvalidArgument,
			fmt.Sprintf("gatewayapp: inspect workspace CWD %q", resolved),
			err,
		)
	}
	if !info.IsDir() {
		return session.WorkspaceRef{}, errorcode.New(
			errorcode.InvalidArgument,
			fmt.Sprintf("gatewayapp: workspace CWD %q is not a directory", resolved),
		)
	}
	if key == "" {
		key = filepath.Base(resolved)
	}
	if key == "" || key == "." || key == string(filepath.Separator) {
		return session.WorkspaceRef{}, errorcode.New(errorcode.InvalidArgument, "gatewayapp: workspace key is required")
	}
	return session.WorkspaceRef{Key: key, CWD: filepath.Clean(resolved)}, nil
}
