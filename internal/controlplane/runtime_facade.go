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
	inner  agent.Runtime
	runsMu sync.Mutex
	runs   map[string]agent.Runner
}

func newRuntimeFacade(inner agent.Runtime) runtimeFacade {
	return runtimeFacade{inner: inner, runs: map[string]agent.Runner{}}
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
	f.runsMu.Lock()
	result.Handle = f.runs[strings.TrimSpace(req.RunID)]
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

func (f *runtimeFacade) rememberRun(runID string, runner agent.Runner) {
	f.runsMu.Lock()
	f.runs[strings.TrimSpace(runID)] = runner
	f.runsMu.Unlock()
}

func (f *runtimeFacade) forgetRun(runID string) {
	f.runsMu.Lock()
	delete(f.runs, strings.TrimSpace(runID))
	f.runsMu.Unlock()
}

// wrapLiveHandle records the outer decorated runner so AttachLiveRun returns the
// same handle identity the original Run produced.
func (f *runtimeFacade) wrapLiveHandle(result agent.RunResult, wrap func(agent.Runner, string) agent.Runner) agent.RunResult {
	if result.Handle == nil {
		return result
	}
	runID := result.Handle.RunID()
	result.Handle = wrap(result.Handle, runID)
	f.rememberRun(runID, result.Handle)
	return result
}
