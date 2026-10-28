package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type modelRequestAuthorityKey struct{}

// Retain a request snapshot while background generation or synchronous recovery
// uses its model and validator. References belong to this Turn; the original
// snapshot remains the sole owner of the provider release.
type modelRequestAuthority struct {
	epoch    string
	admit    func(context.Context, agent.ModelRequestAdmission) error
	validate func(context.Context) (func(), error)
	catalog  tool.Source
	// Runtime guards protect synchronous commits even without a parent guard.
	// They cannot establish the embedding authority required for speculation.
	canSpeculate bool
	mu           sync.Mutex
	refs         int
	release      func()
}

// Capture catalog and grant identity for every request, after the previous tool
// batch drains. Static and resolved models share this boundary. The parent
// retains provider/configuration ownership; a candidate retains this capture.
func (r *Runtime) captureRequestAuthority(parent *modelRequestAuthority) *modelRequestAuthority {
	if parent == nil {
		return nil
	}
	snapshot := r.withMCPCheckpointAuthority(agent.ModelRequestSnapshot{
		Revision: parent.epoch, Validate: parent.validate, DeferredTools: parent.catalog,
	})
	return &modelRequestAuthority{epoch: snapshot.Revision, validate: snapshot.Validate,
		admit: parent.admit, canSpeculate: parent.validate != nil, refs: 1, release: parent.retain()}
}

func (a *modelRequestAuthority) retain() func() {
	if a == nil {
		return func() {}
	}
	a.mu.Lock()
	a.refs++
	a.mu.Unlock()
	return a.drop
}

func (a *modelRequestAuthority) drop() {
	a.mu.Lock()
	a.refs--
	last := a.refs == 0
	a.mu.Unlock()
	if last && a.release != nil {
		a.release()
	}
}

// This private classification is emitted only by the catalog guard before the
// sole checkpoint writer can append. It never classifies permission, ownership
// or Store errors, including unknown append outcomes.
type checkpointCatalogStaleError struct{ cause error }

func (e *checkpointCatalogStaleError) Error() string { return e.cause.Error() }
func (e *checkpointCatalogStaleError) Unwrap() error { return e.cause }

type checkpointValidationKey struct{}
type checkpointValidator = func(context.Context) (func(), error)

// Compose the existing request/configuration owner with the catalog and grant
// owners. Neither refresh nor retirement depends on the compaction scheduler.
// All guards are short, nonblocking, and released in reverse acquisition order.
func (r *Runtime) withMCPCheckpointAuthority(snapshot agent.ModelRequestSnapshot) agent.ModelRequestSnapshot {
	catalog, supported := snapshot.DeferredTools.(tool.CatalogSource)
	revision := ""
	if supported {
		revision = catalog.CatalogRevision()
	}
	grants := r.mcpGrants.RevocationRevision()
	snapshot.Revision = fmt.Sprintf("%s/catalog:%s/grants:%d", snapshot.Revision, revision, grants)
	parent := snapshot.Validate
	snapshot.Validate = func(ctx context.Context) (func(), error) {
		release := func() {}
		if parent != nil {
			var err error
			release, err = parent(ctx)
			if err != nil {
				return nil, err
			}
			if release == nil {
				return nil, agent.ErrModelRequestSnapshotStale
			}
		}
		if snapshot.DeferredTools != nil {
			if !supported {
				release()
				return nil, &CapabilityError{Capability: "tool.CatalogSource"}
			}
			if revision == "" {
				release()
				return nil, agent.ErrModelRequestSnapshotStale
			}
			unlock, err := catalog.ValidateCatalog(ctx, revision)
			if err != nil {
				release()
				if errors.Is(err, tool.ErrCatalogSnapshotStale) {
					return nil, &checkpointCatalogStaleError{cause: errors.Join(agent.ErrModelRequestSnapshotStale, err)}
				}
				return nil, err
			}
			if unlock == nil {
				release()
				return nil, errors.New("runtime: catalog validation requires a release guard")
			}
			previous := release
			release = func() { unlock(); previous() }
		}
		unlock, err := r.mcpGrants.ValidateRevocation(ctx, grants)
		if err != nil {
			release()
			return nil, err
		}
		return func() { unlock(); release() }, nil
	}
	return snapshot
}

func (r *Runtime) staticCheckpointContext(ctx context.Context, source tool.Source) context.Context {
	snapshot := r.withMCPCheckpointAuthority(agent.ModelRequestSnapshot{Revision: "static", DeferredTools: source})
	return context.WithValue(ctx, checkpointValidationKey{}, snapshot.Validate)
}
