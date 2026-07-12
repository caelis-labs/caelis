package controlplane

import (
	"context"
	"fmt"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

// runtimeFacade is the shared decorator shell for Control-owned Runtime wrappers.
// Lease fencing and watchdog observation differ; capability passthrough and live
// runner bookkeeping do not.
type runtimeFacade struct {
	inner         agent.Runtime
	runsMu        sync.Mutex
	runs          map[liveRunKey]liveRunEntry
	runGeneration uint64
}

func newRuntimeFacade(inner agent.Runtime) runtimeFacade {
	return runtimeFacade{inner: inner, runs: map[liveRunKey]liveRunEntry{}}
}

type liveRunKey struct {
	sessionID string
	runID     string
}

type liveRunEntry struct {
	runner     agent.Runner
	generation uint64
}

func normalizedLiveRunKey(ref session.SessionRef, runID string) liveRunKey {
	return liveRunKey{
		sessionID: strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID),
		runID:     strings.TrimSpace(runID),
	}
}

func (f *runtimeFacade) RunState(ctx context.Context, ref session.SessionRef) (agent.RunState, error) {
	return f.inner.RunState(ctx, ref)
}

func (f *runtimeFacade) Streams() stream.Service {
	provider, _ := f.inner.(agent.StreamProvider)
	if provider == nil {
		return nil
	}
	return provider.Streams()
}

func (f *runtimeFacade) AttachLiveRun(ctx context.Context, req agent.AttachLiveRunRequest) (agent.RunResult, error) {
	attacher, ok := f.inner.(agent.LiveRunAttacher)
	if !ok {
		return agent.RunResult{}, &agent.RunNotAttachableError{SessionRef: req.SessionRef, RunID: req.RunID, Detail: "decorated runtime does not support live attachment"}
	}
	result, err := attacher.AttachLiveRun(ctx, req)
	if err != nil {
		return result, err
	}
	key := normalizedLiveRunKey(req.SessionRef, req.RunID)
	f.runsMu.Lock()
	result.Handle = f.runs[key].runner
	f.runsMu.Unlock()
	if result.Handle == nil {
		return agent.RunResult{}, &agent.RunNotAttachableError{SessionRef: req.SessionRef, RunID: req.RunID, Detail: "decorated live runner is unavailable"}
	}
	return result, nil
}

func (f *runtimeFacade) ResolveApproval(ctx context.Context, req agent.ResolveApprovalRequest) error {
	resolver, ok := f.inner.(agent.ApprovalResolver)
	if !ok {
		return fmt.Errorf("controlplane: decorated runtime does not support approval resolution")
	}
	return resolver.ResolveApproval(ctx, req)
}

func (f *runtimeFacade) AttachParticipant(ctx context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
	participants, ok := f.inner.(agent.ParticipantControlPlane)
	if !ok {
		return session.Session{}, fmt.Errorf("controlplane: decorated runtime does not support participants")
	}
	return participants.AttachParticipant(ctx, req)
}

func (f *runtimeFacade) DetachParticipant(ctx context.Context, req agent.DetachParticipantRequest) (session.Session, error) {
	participants, ok := f.inner.(agent.ParticipantControlPlane)
	if !ok {
		return session.Session{}, fmt.Errorf("controlplane: decorated runtime does not support participants")
	}
	return participants.DetachParticipant(ctx, req)
}

func (f *runtimeFacade) participants() (agent.ParticipantControlPlane, error) {
	participants, ok := f.inner.(agent.ParticipantControlPlane)
	if !ok {
		return nil, fmt.Errorf("controlplane: decorated runtime does not support participants")
	}
	return participants, nil
}

// wrapLiveHandle records the outer decorated runner so AttachLiveRun returns the
// same handle identity the original Run produced.
func (f *runtimeFacade) wrapLiveHandle(
	result agent.RunResult,
	ref session.SessionRef,
	wrap func(agent.Runner, func()) agent.Runner,
) agent.RunResult {
	if result.Handle == nil {
		return result
	}
	key := normalizedLiveRunKey(ref, result.Handle.RunID())
	f.runsMu.Lock()
	f.runGeneration++
	generation := f.runGeneration
	// Reserve the generation before calling arbitrary wrapper code. An older
	// wrap that finishes later can no longer overwrite a newer reservation.
	f.runs[key] = liveRunEntry{generation: generation}
	f.runsMu.Unlock()
	result.Handle = wrap(result.Handle, func() {
		f.runsMu.Lock()
		if current, ok := f.runs[key]; ok && current.generation == generation {
			delete(f.runs, key)
		}
		f.runsMu.Unlock()
	})
	f.runsMu.Lock()
	if current, ok := f.runs[key]; ok && current.generation == generation {
		f.runs[key] = liveRunEntry{runner: result.Handle, generation: generation}
	}
	f.runsMu.Unlock()
	return result
}
