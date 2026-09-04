package runtime

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

type compactionRequestSnapshot struct {
	model      model.LLM
	request    *model.Request
	throughSeq uint64
	updatedAt  time.Time
}

const maxCompactionRequestSnapshots = 4

func (r *Runtime) rememberCompactionRequest(ref session.SessionRef, llm model.LLM, req *model.Request, throughSeq uint64) {
	if r == nil || llm == nil || req == nil {
		return
	}
	r.compactionRequestMu.Lock()
	defer r.compactionRequestMu.Unlock()
	r.compactionRequests[compactionSessionKey(ref)] = compactionRequestSnapshot{
		model: llm, request: model.CloneRequest(req), throughSeq: throughSeq, updatedAt: r.compactionSnapshotTime(),
	}
	r.trimCompactionRequestsLocked()
}

func (r *Runtime) completeCompactionRequest(ref session.SessionRef, llm model.LLM, req *model.Request, final *model.Response, throughSeq uint64) {
	if final == nil || len(final.Message.ToolCalls()) > 0 || strings.TrimSpace(final.Message.TextContent()) == "" {
		return
	}
	completed := model.CloneRequest(req)
	completed.Messages = append(completed.Messages, model.CloneMessage(final.Message))
	r.compactionRequestMu.Lock()
	defer r.compactionRequestMu.Unlock()
	r.compactionRequests[compactionSessionKey(ref)] = compactionRequestSnapshot{
		model: llm, request: completed, throughSeq: throughSeq, updatedAt: r.compactionSnapshotTime(),
	}
	r.trimCompactionRequestsLocked()
}

func (r *Runtime) inContextRequest(ref session.SessionRef, llm model.LLM, events []*session.Event, serviceTier model.ServiceTier) *model.Request {
	if r == nil || llm == nil {
		return nil
	}
	r.compactionRequestMu.RLock()
	snapshot, ok := r.compactionRequests[compactionSessionKey(ref)]
	r.compactionRequestMu.RUnlock()
	if !ok || snapshot.request == nil || !sameCompactionModel(snapshot.model, llm) {
		return nil
	}
	if snapshot.throughSeq != session.LastEventSeq(mainInvocationEvents(events)) {
		return nil
	}
	request := model.CloneRequest(snapshot.request)
	request.ServiceTier = serviceTier
	return request
}

func (r *Runtime) advanceCompactionRequest(ref session.SessionRef, throughSeq uint64) {
	if r == nil || throughSeq == 0 {
		return
	}
	key := compactionSessionKey(ref)
	r.compactionRequestMu.Lock()
	defer r.compactionRequestMu.Unlock()
	snapshot, ok := r.compactionRequests[key]
	if !ok {
		return
	}
	snapshot.throughSeq = max(snapshot.throughSeq, throughSeq)
	snapshot.updatedAt = r.compactionSnapshotTime()
	r.compactionRequests[key] = snapshot
}

func (r *Runtime) compactionSnapshotTime() time.Time {
	if r != nil && r.clock != nil {
		return r.clock()
	}
	return time.Now()
}

func (r *Runtime) trimCompactionRequestsLocked() {
	for len(r.compactionRequests) > maxCompactionRequestSnapshots {
		oldestKey := ""
		var oldest time.Time
		for key, snapshot := range r.compactionRequests {
			if oldestKey == "" || snapshot.updatedAt.Before(oldest) || (snapshot.updatedAt.Equal(oldest) && key < oldestKey) {
				oldestKey = key
				oldest = snapshot.updatedAt
			}
		}
		delete(r.compactionRequests, oldestKey)
	}
}

func (r *Runtime) clearCompactionRequest(ref session.SessionRef) {
	if r == nil {
		return
	}
	r.compactionRequestMu.Lock()
	delete(r.compactionRequests, compactionSessionKey(ref))
	r.compactionRequestMu.Unlock()
}

func compactionSessionKey(ref session.SessionRef) string {
	ref = session.NormalizeSessionRef(ref)
	return strings.Join([]string{ref.AppName, ref.UserID, ref.WorkspaceKey, ref.SessionID}, "\x00")
}

func sameCompactionModel(left, right model.LLM) bool {
	if left == nil || right == nil {
		return false
	}
	leftProvider, leftModel := compactionModelIdentity(left)
	rightProvider, rightModel := compactionModelIdentity(right)
	return leftProvider != "" && leftProvider == rightProvider && leftModel != "" && leftModel == rightModel
}

func compactionModelIdentity(llm model.LLM) (string, string) {
	if llm == nil {
		return "", ""
	}
	provider := ""
	if named, ok := llm.(interface{ ProviderName() string }); ok {
		provider = strings.TrimSpace(named.ProviderName())
	}
	return session.StableInvocationIdentity(provider, strings.TrimSpace(llm.Name()))
}

func (r *Runtime) runtimeCompactionAppendix(ctx context.Context, ref session.SessionRef, state map[string]any) (string, error) {
	payload := runtimeContinuityPayload{Plan: activePlanContinuity(state)}
	entries, err := r.tasks.activeSubagentSessionEntries(ctx, ref)
	if err != nil {
		return "", err
	}
	payload.ActiveSubagentHandle = activeSubagentContinuity(entries)
	return marshalRuntimeContinuity(payload)
}

func activePlanContinuity(state map[string]any) map[string]any {
	if state == nil {
		return nil
	}
	planState, ok := state["plan"].(map[string]any)
	if !ok || len(planState) == 0 {
		return nil
	}
	return session.CloneState(planState)
}

func (tm *taskRuntime) activeSubagentSessionEntries(ctx context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	ref = session.NormalizeSessionRef(ref)
	listed, err := tm.listSessionEntries(ctx, ref)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*taskapi.Entry, len(listed))
	for _, entry := range listed {
		if entry != nil && entry.Kind == taskapi.KindSubagent && compactionSessionRefMatches(entry.Session, ref) {
			byID[entry.TaskID] = taskapi.CloneEntry(entry)
		}
	}
	if tm != nil {
		tm.mu.RLock()
		subagents := make([]*subagentTask, 0, len(tm.subagents))
		for _, subagent := range tm.subagents {
			subagents = append(subagents, subagent)
		}
		tm.mu.RUnlock()
		for _, subagent := range subagents {
			subagent.mu.Lock()
			entry := subagent.entrySnapshot(tm.runtime.now())
			subagent.mu.Unlock()
			if entry != nil && compactionSessionRefMatches(entry.Session, ref) {
				byID[entry.TaskID] = entry
			}
		}
	}
	out := make([]*taskapi.Entry, 0, len(byID))
	for _, entry := range byID {
		if entry != nil && entry.Kind == taskapi.KindSubagent &&
			(entry.Running || entry.State == taskapi.StatePrepared || taskStateRunning(entry.State)) {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Handle < out[j].Handle
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func compactionSessionRefMatches(left, right session.SessionRef) bool {
	return session.NormalizeSessionRef(left) == session.NormalizeSessionRef(right)
}

func activeSubagentContinuity(entries []*taskapi.Entry) []string {
	handles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Kind != taskapi.KindSubagent {
			continue
		}
		handle := taskapi.NormalizeHandle(entry.Handle)
		if handle == "" {
			continue
		}
		handles = append(handles, handle)
	}
	return handles
}
