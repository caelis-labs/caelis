package agentsdk

import (
	"strings"
	"sync"
)

// ModelStepRef identifies an approval request's position in a model-emitted
// tool-call batch. Index is zero-based and CallCount includes every call in the
// batch, including calls that do not require approval. The shared admission
// barrier, when present, is process-local and never serialized.
type ModelStepRef struct {
	ID        string `json:"id"`
	Index     int    `json:"index"`
	CallCount int    `json:"call_count"`
	admission *modelStepAdmission
}

type modelStepAdmission struct {
	mu        sync.Mutex
	remaining int
	seen      []bool
	done      chan struct{}
}

// NewConcurrentModelStepRefs creates one shared admission barrier for all tool
// calls that Runtime will execute concurrently. The barrier closes only after
// every call has either entered approval routing or completed policy admission.
func NewConcurrentModelStepRefs(id string, callCount int) []*ModelStepRef {
	id = strings.TrimSpace(id)
	if id == "" || callCount < 2 {
		return nil
	}
	admission := &modelStepAdmission{
		remaining: callCount,
		seen:      make([]bool, callCount),
		done:      make(chan struct{}),
	}
	refs := make([]*ModelStepRef, callCount)
	for index := range refs {
		refs[index] = &ModelStepRef{ID: id, Index: index, CallCount: callCount, admission: admission}
	}
	return refs
}

// MarkAdmissionComplete records that this call can no longer create a new
// approval request for its model step. Repeated calls are harmless.
func (r *ModelStepRef) MarkAdmissionComplete() {
	if r == nil || r.admission == nil || r.Index < 0 || r.Index >= r.CallCount {
		return
	}
	admission := r.admission
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if r.Index >= len(admission.seen) || admission.seen[r.Index] {
		return
	}
	admission.seen[r.Index] = true
	admission.remaining--
	if admission.remaining == 0 {
		close(admission.done)
	}
}

// AdmissionDone returns the process-local barrier for a concurrently executed
// model step. A nil channel means the call is not part of a concurrent batch.
func (r *ModelStepRef) AdmissionDone() <-chan struct{} {
	if r == nil || r.admission == nil {
		return nil
	}
	return r.admission.done
}

// CloneModelStepRef returns an isolated view of one model-call position while
// retaining the shared admission barrier. The fields are request metadata; the
// barrier remains the Runtime-owned synchronization authority.
func CloneModelStepRef(in *ModelStepRef) *ModelStepRef {
	if in == nil {
		return nil
	}
	out := *in
	out.ID = strings.TrimSpace(in.ID)
	return &out
}
