package sandbox

import (
	"context"
	"fmt"
	"strings"
)

type compositeRuntime struct {
	host     Runtime
	sandbox  Runtime
	status   Status
	backends map[Backend]Runtime
}

func (r *compositeRuntime) Describe() Descriptor {
	if runtime := r.runtimeForConstraints(Constraints{}); runtime != nil {
		return runtime.Describe()
	}
	return Descriptor{}
}

func (r *compositeRuntime) FileSystem() FileSystem {
	return r.FileSystemFor(Constraints{})
}

func (r *compositeRuntime) FileSystemFor(constraints Constraints) FileSystem {
	if runtime := r.runtimeForConstraints(constraints); runtime != nil {
		return runtime.FileSystemFor(constraints)
	}
	return nil
}

func (r *compositeRuntime) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	constraints := EffectiveConstraints(req)
	runtime := r.runtimeForConstraints(constraints)
	if runtime == nil {
		return CommandResult{}, fmt.Errorf("sdk/sandbox: runtime is unavailable")
	}
	result, err := runtime.Run(ctx, req)
	if runtime != r.host && constraints.Route != RouteHost && constraints.Permission != PermissionFullAccess {
		return NormalizeSandboxPermissionFailure(result, err)
	}
	return result, err
}

func (r *compositeRuntime) Start(ctx context.Context, req CommandRequest) (Session, error) {
	runtime := r.runtimeForConstraints(EffectiveConstraints(req))
	if runtime == nil {
		return nil, fmt.Errorf("sdk/sandbox: runtime is unavailable")
	}
	return runtime.Start(ctx, req)
}

func (r *compositeRuntime) OpenSession(id string) (Session, error) {
	ref, err := splitSessionID(id)
	if err != nil {
		return nil, err
	}
	return r.OpenSessionRef(ref)
}

func (r *compositeRuntime) OpenSessionRef(ref SessionRef) (Session, error) {
	ref = CloneSessionRef(ref)
	if ref.Backend == "" || ref.SessionID == "" {
		return nil, fmt.Errorf("sdk/sandbox: session ref is incomplete")
	}
	runtime := r.backends[ref.Backend]
	if runtime == nil {
		return nil, fmt.Errorf("sdk/sandbox: backend %q is unavailable", ref.Backend)
	}
	return runtime.OpenSession(ref.SessionID)
}

func (r *compositeRuntime) SupportedBackends() []Backend {
	out := make([]Backend, 0, len(r.backends))
	for backend := range r.backends {
		out = append(out, backend)
	}
	return dedupeBackends(out)
}

func (r *compositeRuntime) Status() Status {
	return r.status
}

func (r *compositeRuntime) Close() error {
	var firstErr error
	closed := map[Runtime]struct{}{}
	for _, runtime := range r.backends {
		if runtime == nil {
			continue
		}
		if _, ok := closed[runtime]; ok {
			continue
		}
		closed[runtime] = struct{}{}
		if err := runtime.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *compositeRuntime) runtimeForConstraints(constraints Constraints) Runtime {
	constraints = NormalizeConstraints(constraints)
	if constraints.Backend == BackendHost || constraints.Route == RouteHost || constraints.Permission == PermissionFullAccess {
		return r.host
	}
	if constraints.Backend != "" {
		if runtime := r.backends[constraints.Backend]; runtime != nil {
			return runtime
		}
	}
	if r.sandbox != nil {
		return r.sandbox
	}
	return r.host
}

func splitSessionID(raw string) (SessionRef, error) {
	backendText, sessionID, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok {
		return SessionRef{}, fmt.Errorf("sdk/sandbox: session id must be encoded as <backend>:<session-id>")
	}
	ref := SessionRef{
		Backend:   Backend(strings.TrimSpace(backendText)),
		SessionID: strings.TrimSpace(sessionID),
	}
	if ref.Backend == "" || ref.SessionID == "" {
		return SessionRef{}, fmt.Errorf("sdk/sandbox: invalid session id %q", raw)
	}
	return ref, nil
}

func dedupeBackends(values []Backend) []Backend {
	if len(values) == 0 {
		return nil
	}
	out := make([]Backend, 0, len(values))
	seen := map[Backend]struct{}{}
	for _, value := range values {
		value = Backend(strings.TrimSpace(string(value)))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
