package fixture

import (
	"context"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/acp"
)

// Recorder implements acp.PromptCallbacks and captures ACP updates and
// permission requests for conformance-style tests.
type Recorder struct {
	mu         sync.Mutex
	updates    []acp.SessionNotification
	permission []acp.RequestPermissionRequest
	response   acp.RequestPermissionResponse
}

func NewRecorder(resp acp.RequestPermissionResponse) *Recorder {
	return &Recorder{response: resp}
}

func (r *Recorder) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, notification)
	return nil
}

func (r *Recorder) RequestPermission(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permission = append(r.permission, req)
	return r.response, nil
}

func (r *Recorder) Notifications() []acp.SessionNotification {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]acp.SessionNotification, 0, len(r.updates))
	out = append(out, r.updates...)
	return out
}

func (r *Recorder) PermissionRequests() []acp.RequestPermissionRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]acp.RequestPermissionRequest, 0, len(r.permission))
	out = append(out, r.permission...)
	return out
}

func UpdateKinds(items []acp.SessionNotification) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item.Update == nil {
			continue
		}
		out = append(out, strings.TrimSpace(item.Update.SessionUpdateType()))
	}
	return out
}
