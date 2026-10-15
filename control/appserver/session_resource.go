package appserver

import (
	"context"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"time"
)

// SessionResourceRequest transfers immutable bytes under the same Session owner
// and operation namespace as all other Control mutations.
type SessionResourceRequest struct {
	WriteBase
	Name      string     `json:"name"`
	MediaType string     `json:"media_type"`
	Data      []byte     `json:"data"`
	SHA256    string     `json:"sha256"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// SessionResourceContent transfers verified immutable bytes, never a local path.
type SessionResourceContent struct {
	Resource sessionprofile.Resource `json:"resource"`
	Data     []byte                  `json:"data"`
}

func (s *SessionProfileService) CreateSessionResource(ctx context.Context, p Principal, req SessionResourceRequest) (CommandResult, error) {
	return s.config.Commands.execute(ctx, p, ActionSessionResource, req.WriteBase, req.SessionID, req)
}
