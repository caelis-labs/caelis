package appserver

import "context"

const (
	ActionBackgroundGrantCreate Action = "session.background-grant.create"
	ActionBackgroundGrantRevoke Action = "session.background-grant.revoke"
)

type CreateSessionBackgroundGrantRequest struct {
	WriteBase
	Source                   string `json:"source"`
	AuthorizationOperationID string `json:"authorization_operation_id"`
}

type RevokeSessionBackgroundGrantRequest struct {
	WriteBase
	GrantID string `json:"grant_id"`
}

func (s *SessionProfileService) CreateSessionBackgroundGrant(ctx context.Context, p Principal, req CreateSessionBackgroundGrantRequest) (CommandResult, error) {
	return s.config.Commands.execute(ctx, p, ActionBackgroundGrantCreate, req.WriteBase, req.SessionID, req)
}
func (s *SessionProfileService) RevokeSessionBackgroundGrant(ctx context.Context, p Principal, req RevokeSessionBackgroundGrantRequest) (CommandResult, error) {
	return s.config.Commands.execute(ctx, p, ActionBackgroundGrantRevoke, req.WriteBase, req.GrantID, req)
}
