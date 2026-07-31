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
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// sessionRuntime owns one in-memory execution activation. Its assembled
// workspace configuration remains fixed until the Runtime is released. No
// generation identity is persisted with the Session.
type sessionRuntime struct {
	sessionID string
	workspace session.WorkspaceRef
	stack     *Stack

	// Lifecycle fields are guarded by sessionRuntimeRegistry.mu. Once releasing
	// is set, command routing must not return this Runtime again. inUse covers
	// synchronous Control mutation dispatches that already obtained the Runtime;
	// release waits for usesIdle before quiescing detached resources.
	releasing   bool
	releaseDone chan struct{}
	releaseErr  error
	inUse       int
	usesIdle    chan struct{}
}

// sessionRuntimeRegistry is the app-scoped owner of live Session execution
// activations. Durable Session rows own workspace identity; observation does
// not create an entry here.
type sessionRuntimeRegistry struct {
	owner     *Stack
	assembler *workspaceConfigAssembler

	mu           sync.RWMutex
	sessions     map[string]*sessionRuntime
	workspaceCWD map[string]string
	workspaceKey map[string]string
	closed       bool
	building     int
	buildsIdle   chan struct{}
}

func newSessionRuntimeRegistry(owner *Stack) (*sessionRuntimeRegistry, error) {
	if owner == nil {
		return nil, errors.New("gatewayapp: Session Runtime owner is required")
	}
	workspace, err := canonicalWorkspaceRef(owner.Workspace, session.WorkspaceRef{})
	if err != nil {
		return nil, err
	}
	assembler, err := newWorkspaceConfigAssembler(owner)
	if err != nil {
		return nil, err
	}
	owner.Workspace = workspace
	buildsIdle := make(chan struct{})
	close(buildsIdle)
	return &sessionRuntimeRegistry{
		owner:        owner,
		assembler:    assembler,
		sessions:     map[string]*sessionRuntime{},
		workspaceCWD: map[string]string{workspace.Key: workspace.CWD},
		workspaceKey: map[string]string{workspace.CWD: workspace.Key},
		buildsIdle:   buildsIdle,
	}, nil
}

// lockActivation admits one durable create or Runtime activation and
// serializes workspace identity plus stateless assembly with app configuration
// mutation.
func (r *sessionRuntimeRegistry) lockActivation(
	ctx context.Context,
) (context.Context, func(), error) {
	buildCtx, finishBuild, err := r.beginBuild(ctx)
	if err != nil {
		return nil, nil, err
	}
	gate := r.owner.reconfigureLock()
	gate.Lock()
	if r.isClosed() {
		gate.Unlock()
		finishBuild()
		return nil, nil, sessionRuntimeHostClosingError()
	}
	if err := buildCtx.Err(); err != nil {
		gate.Unlock()
		finishBuild()
		return nil, nil, err
	}
	return buildCtx, func() {
		gate.Unlock()
		finishBuild()
	}, nil
}

// resolveCreateWorkspaceLocked validates one durable create without assembling
// execution state. The caller must hold the activation lock through durable
// Session creation and bindCreatedWorkspaceLocked so concurrent creates cannot
// claim conflicting workspace aliases.
func (r *sessionRuntimeRegistry) resolveCreateWorkspaceLocked(
	ctx context.Context,
	principal controlclient.Principal,
	requested session.WorkspaceRef,
	preferredSessionID string,
) (session.WorkspaceRef, error) {
	if r == nil || r.owner == nil || r.owner.Sessions == nil {
		return session.WorkspaceRef{}, errors.New("gatewayapp: Session Runtime registry is unavailable")
	}
	workspace, err := canonicalWorkspaceRef(requested, r.owner.Workspace)
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	preferredSessionID = strings.TrimSpace(preferredSessionID)
	if preferredSessionID != "" {
		existing, loadErr := r.owner.Sessions.Session(ctx, session.SessionRef{SessionID: preferredSessionID})
		switch {
		case loadErr == nil:
			if !principal.HasRole("admin") &&
				strings.TrimSpace(existing.UserID) != strings.TrimSpace(principal.ID) {
				return session.WorkspaceRef{}, controlclient.ErrUnauthorized
			}
			actual, canonicalErr := canonicalSessionWorkspace(existing)
			if canonicalErr != nil {
				return session.WorkspaceRef{}, canonicalErr
			}
			if actual != workspace {
				return session.WorkspaceRef{}, errorcode.New(
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
			return session.WorkspaceRef{}, loadErr
		}
	}
	if err := r.validateWorkspaceIdentity(workspace); err != nil {
		return session.WorkspaceRef{}, err
	}
	return workspace, nil
}

func (r *sessionRuntimeRegistry) bindCreatedWorkspaceLocked(
	active session.Session,
	workspace session.WorkspaceRef,
) error {
	actual, err := canonicalSessionWorkspace(active)
	if err != nil {
		return err
	}
	if actual != workspace {
		return fmt.Errorf(
			"gatewayapp: Session %q belongs to workspace %q at %q, not %q at %q",
			active.SessionID,
			actual.Key,
			actual.CWD,
			workspace.Key,
			workspace.CWD,
		)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.owner == nil || r.owner.isClosing() {
		return sessionRuntimeHostClosingError()
	}
	if err := r.validateWorkspaceIdentityLocked(workspace); err != nil {
		return err
	}
	r.recordWorkspaceIdentityLocked(workspace)
	return nil
}

func (r *sessionRuntimeRegistry) bindActivatedLocked(
	active session.Session,
	runtime *sessionRuntime,
) error {
	if runtime == nil || runtime.stack == nil {
		return errors.New("gatewayapp: activated Session Runtime is unavailable")
	}
	if err := validateSessionRuntime(active, runtime); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(active.SessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.owner == nil || r.owner.isClosing() {
		return sessionRuntimeHostClosingError()
	}
	if existing := r.sessions[sessionID]; existing != nil && existing != runtime {
		if existing.releasing {
			return sessionRuntimeReleasingError(sessionID)
		}
		return fmt.Errorf("gatewayapp: Session %q already has a live Runtime", sessionID)
	}
	if err := r.validateWorkspaceIdentityLocked(runtime.workspace); err != nil {
		return err
	}
	runtime.sessionID = sessionID
	r.sessions[sessionID] = runtime
	r.recordWorkspaceIdentityLocked(runtime.workspace)
	return nil
}

// activateSession returns the existing fixed Runtime or assembles current
// workspace configuration once when the Session has no live activation.
func (r *sessionRuntimeRegistry) activateSession(
	ctx context.Context,
	sessionID string,
) (*sessionRuntime, session.Session, error) {
	if r == nil || r.owner == nil || r.owner.Sessions == nil {
		return nil, session.Session{}, errors.New("gatewayapp: Session Runtime registry is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, session.Session{}, errors.New("gatewayapp: Session ID is required")
	}
	if r.isReleasing(sessionID) {
		return nil, session.Session{}, sessionRuntimeReleasingError(sessionID)
	}
	if loaded, ok := r.loaded(sessionID); ok {
		active, err := r.owner.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
		return loaded, active, err
	}

	buildCtx, unlock, err := r.lockActivation(ctx)
	if err != nil {
		return nil, session.Session{}, err
	}
	defer unlock()
	if r.isReleasing(sessionID) {
		return nil, session.Session{}, sessionRuntimeReleasingError(sessionID)
	}
	if loaded, ok := r.loaded(sessionID); ok {
		active, err := r.owner.Sessions.Session(buildCtx, session.SessionRef{SessionID: sessionID})
		return loaded, active, err
	}
	active, err := r.owner.Sessions.Session(buildCtx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		return nil, session.Session{}, err
	}
	closed, err := controlclient.IsSessionClosed(buildCtx, r.owner.Sessions, active.SessionRef)
	if err != nil {
		return nil, active, err
	}
	if closed {
		return nil, active, controlclient.ErrSessionClosed
	}
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, active, err
	}
	if err := r.validateWorkspaceIdentity(workspace); err != nil {
		return nil, active, err
	}
	stack, err := r.assembler.assembleLocked(buildCtx, workspace)
	if err != nil {
		return nil, active, err
	}
	runtime := &sessionRuntime{sessionID: sessionID, workspace: workspace, stack: stack}
	if err := r.bindActivatedLocked(active, runtime); err != nil {
		_ = stack.closeWorkspaceResources()
		return nil, active, err
	}
	return runtime, active, nil
}

func (r *sessionRuntimeRegistry) loaded(sessionID string) (*sessionRuntime, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	runtime := r.sessions[strings.TrimSpace(sessionID)]
	if runtime == nil || runtime.releasing {
		return nil, false
	}
	return runtime, true
}

// acquireRuntimeUse prevents release from quiescing one Runtime between command
// routing and completion of a synchronous Control mutation.
func (r *sessionRuntimeRegistry) acquireRuntimeUse(runtime *sessionRuntime) (func(), error) {
	if r == nil || runtime == nil {
		return nil, errors.New("gatewayapp: Session Runtime is unavailable")
	}
	r.mu.Lock()
	if r.closed || r.owner == nil || r.owner.isClosing() {
		r.mu.Unlock()
		return nil, sessionRuntimeHostClosingError()
	}
	if current := r.sessions[runtime.sessionID]; current != runtime || runtime.releasing {
		r.mu.Unlock()
		return nil, sessionRuntimeReleasingError(runtime.sessionID)
	}
	releaseUse := r.retainRuntimeLocked(runtime)
	r.mu.Unlock()
	return releaseUse, nil
}

func (r *sessionRuntimeRegistry) acquireLoadedRuntime(sessionID string) (*sessionRuntime, func(), error) {
	if r == nil {
		return nil, nil, errors.New("gatewayapp: Session Runtime registry is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	r.mu.Lock()
	if r.closed || r.owner == nil || r.owner.isClosing() {
		r.mu.Unlock()
		return nil, nil, sessionRuntimeHostClosingError()
	}
	runtime := r.sessions[sessionID]
	if runtime == nil {
		r.mu.Unlock()
		return nil, nil, nil
	}
	if runtime.releasing {
		r.mu.Unlock()
		return nil, nil, sessionRuntimeReleasingError(sessionID)
	}
	releaseUse := r.retainRuntimeLocked(runtime)
	r.mu.Unlock()
	return runtime, releaseUse, nil
}

// acquireControlRuntime resolves the fixed Runtime for an active Session or a
// disposable, current workspace composition for an idle observation. The
// disposable branch deliberately has no cache or registry entry.
func (r *sessionRuntimeRegistry) acquireControlRuntime(
	ctx context.Context,
	sessionID string,
	activate bool,
) (*sessionRuntime, session.Session, func(context.Context) error, error) {
	if r == nil || r.owner == nil {
		return nil, session.Session{}, nil, errors.New("gatewayapp: Session Runtime registry is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, session.Session{}, nil, errors.New("gatewayapp: Session ID is required")
	}
	if activate {
		runtime, active, err := r.activateSession(ctx, sessionID)
		if err != nil {
			return nil, active, nil, err
		}
		release, err := r.acquireRuntimeUse(runtime)
		if err != nil {
			return nil, active, nil, err
		}
		return runtime, active, func(context.Context) error { release(); return nil }, nil
	}

	buildCtx, unlock, err := r.lockActivation(ctx)
	if err != nil {
		return nil, session.Session{}, nil, err
	}
	defer unlock()
	loaded, releaseUse, err := r.acquireLoadedRuntime(sessionID)
	if err != nil {
		return nil, session.Session{}, nil, err
	}
	if loaded != nil {
		active, loadErr := r.owner.Sessions.Session(buildCtx, session.SessionRef{SessionID: sessionID})
		if loadErr != nil {
			releaseUse()
			return nil, session.Session{}, nil, loadErr
		}
		return loaded, active, func(context.Context) error { releaseUse(); return nil }, nil
	}
	active, err := r.owner.Sessions.Session(buildCtx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		return nil, session.Session{}, nil, err
	}
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, active, nil, err
	}
	if err := r.validateWorkspaceIdentity(workspace); err != nil {
		return nil, active, nil, err
	}
	stack, err := r.assembler.assembleLocked(buildCtx, workspace)
	if err != nil {
		return nil, active, nil, err
	}
	runtime := &sessionRuntime{sessionID: sessionID, workspace: workspace, stack: stack}
	return runtime, active, func(closeCtx context.Context) error {
		if closeCtx == nil {
			closeCtx = context.Background()
		}
		return errors.Join(stack.Quiesce(closeCtx), stack.closeWorkspaceResources())
	}, nil
}

// retainRuntimeLocked records one routed synchronous command. The caller holds
// r.mu and owns the returned idempotent release function.
func (r *sessionRuntimeRegistry) retainRuntimeLocked(runtime *sessionRuntime) func() {
	if runtime.inUse == 0 {
		runtime.usesIdle = make(chan struct{})
	}
	runtime.inUse++

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			runtime.inUse--
			if runtime.inUse == 0 {
				close(runtime.usesIdle)
				runtime.usesIdle = nil
			}
			r.mu.Unlock()
		})
	}
}

func (r *sessionRuntimeRegistry) release(ctx context.Context, sessionID string) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID = strings.TrimSpace(sessionID)
	r.mu.Lock()
	runtime := r.sessions[sessionID]
	if runtime == nil {
		r.mu.Unlock()
		return nil
	}
	if runtime.releasing {
		done := runtime.releaseDone
		r.mu.Unlock()
		select {
		case <-done:
			r.mu.RLock()
			err := runtime.releaseErr
			r.mu.RUnlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	runtime.releasing = true
	runtime.releaseDone = make(chan struct{})
	done := runtime.releaseDone
	r.mu.Unlock()

	releaseErr := r.waitRuntimeUnused(ctx, runtime)
	if releaseErr == nil {
		releaseErr = runtime.stack.Quiesce(ctx)
	}
	if releaseErr == nil {
		releaseErr = runtime.stack.closeWorkspaceResources()
	}
	r.mu.Lock()
	runtime.releaseErr = releaseErr
	if releaseErr == nil && r.sessions[sessionID] == runtime {
		delete(r.sessions, sessionID)
	}
	close(done)
	r.mu.Unlock()
	return releaseErr
}

func (r *sessionRuntimeRegistry) releaseSession(ctx context.Context, sessionID string) error {
	return r.release(ctx, sessionID)
}

func (r *sessionRuntimeRegistry) snapshot() []*sessionRuntime {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*sessionRuntime, 0, len(r.sessions))
	for _, runtime := range r.sessions {
		out = append(out, runtime)
	}
	return out
}

func (r *sessionRuntimeRegistry) closeAdmission(ctx context.Context) ([]*sessionRuntime, error) {
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
		releaseErr := r.waitForReleases(ctx)
		useErr := r.waitForRuntimeUses(ctx)
		return r.snapshot(), errors.Join(releaseErr, useErr)
	case <-ctx.Done():
		return r.snapshot(), ctx.Err()
	}
}

func (r *sessionRuntimeRegistry) waitRuntimeUnused(ctx context.Context, runtime *sessionRuntime) error {
	r.mu.RLock()
	if runtime == nil || runtime.inUse == 0 {
		r.mu.RUnlock()
		return nil
	}
	idle := runtime.usesIdle
	r.mu.RUnlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *sessionRuntimeRegistry) waitForRuntimeUses(ctx context.Context) error {
	for {
		r.mu.RLock()
		pending := make([]<-chan struct{}, 0)
		for _, runtime := range r.sessions {
			if runtime == nil || runtime.inUse == 0 || runtime.usesIdle == nil {
				continue
			}
			pending = append(pending, runtime.usesIdle)
		}
		r.mu.RUnlock()
		if len(pending) == 0 {
			return nil
		}
		for _, idle := range pending {
			select {
			case <-idle:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (r *sessionRuntimeRegistry) waitForReleases(ctx context.Context) error {
	for {
		r.mu.RLock()
		pending := make([]<-chan struct{}, 0)
		for _, runtime := range r.sessions {
			if runtime == nil || !runtime.releasing || runtime.releaseDone == nil {
				continue
			}
			select {
			case <-runtime.releaseDone:
			default:
				pending = append(pending, runtime.releaseDone)
			}
		}
		r.mu.RUnlock()
		if len(pending) == 0 {
			return nil
		}
		for _, done := range pending {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (r *sessionRuntimeRegistry) beginBuild(
	ctx context.Context,
) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed || r.owner == nil || r.owner.isClosing() {
		r.mu.Unlock()
		return nil, nil, sessionRuntimeHostClosingError()
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

func (r *sessionRuntimeRegistry) isClosed() bool {
	if r == nil {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}

func (r *sessionRuntimeRegistry) isReleasing(sessionID string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	runtime := r.sessions[strings.TrimSpace(sessionID)]
	return runtime != nil && runtime.releasing
}

func (r *sessionRuntimeRegistry) validateWorkspaceIdentity(workspace session.WorkspaceRef) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.validateWorkspaceIdentityLocked(workspace)
}

func (r *sessionRuntimeRegistry) validateWorkspaceIdentityLocked(workspace session.WorkspaceRef) error {
	if existingCWD := r.workspaceCWD[workspace.Key]; existingCWD != "" && existingCWD != workspace.CWD {
		return errorcode.New(
			errorcode.InvalidArgument,
			fmt.Sprintf(
				"gatewayapp: workspace key %q is already bound to %q, not %q",
				workspace.Key,
				existingCWD,
				workspace.CWD,
			),
		)
	}
	if existingKey := r.workspaceKey[workspace.CWD]; existingKey != "" && existingKey != workspace.Key {
		return errorcode.New(
			errorcode.InvalidArgument,
			fmt.Sprintf(
				"gatewayapp: workspace directory %q is already bound as %q, not %q",
				workspace.CWD,
				existingKey,
				workspace.Key,
			),
		)
	}
	return nil
}

func (r *sessionRuntimeRegistry) recordWorkspaceIdentityLocked(workspace session.WorkspaceRef) {
	r.workspaceCWD[workspace.Key] = workspace.CWD
	r.workspaceKey[workspace.CWD] = workspace.Key
}

func validateSessionRuntime(active session.Session, runtime *sessionRuntime) error {
	if runtime == nil || runtime.stack == nil {
		return errors.New("gatewayapp: Session Runtime is unavailable")
	}
	sessionID := strings.TrimSpace(active.SessionID)
	if sessionID == "" {
		return errors.New("gatewayapp: cannot bind an empty Session ID")
	}
	actual, err := canonicalSessionWorkspace(active)
	if err != nil {
		return err
	}
	if actual != runtime.workspace {
		return fmt.Errorf(
			"gatewayapp: Session %q belongs to workspace %q at %q, not %q at %q",
			sessionID,
			actual.Key,
			actual.CWD,
			runtime.workspace.Key,
			runtime.workspace.CWD,
		)
	}
	return nil
}

func canonicalSessionWorkspace(active session.Session) (session.WorkspaceRef, error) {
	return canonicalWorkspaceRef(session.WorkspaceRef{
		Key: active.WorkspaceKey,
		CWD: active.CWD,
	}, session.WorkspaceRef{})
}

func sessionRuntimeHostClosingError() error {
	return errorcode.New(errorcode.Unavailable, "gatewayapp: host is closing")
}

func sessionRuntimeReleasingError(sessionID string) error {
	return errorcode.New(
		errorcode.Unavailable,
		fmt.Sprintf("gatewayapp: Session %q Runtime is releasing", strings.TrimSpace(sessionID)),
	)
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
