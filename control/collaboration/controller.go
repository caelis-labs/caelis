package collaboration

import (
	"context"
	"encoding/json"
	"errors"
)

// ThreadStarter routes an authenticated controller request to the existing
// Runtime Spawn owner. The epoch is derived from the grant, never tool arguments.
type ThreadStarter interface {
	Start(context.Context, Identity, string, json.RawMessage) (json.RawMessage, error)
}

func (s *Service) startThread(ctx context.Context, i Identity, args json.RawMessage) (json.RawMessage, error) {
	token, _ := ctx.Value(credentialContextKey{}).(string)
	if token == "" || i.Member != "parent" {
		return nil, errors.New("StartThread requires a current controller grant")
	}
	if _, _, err := s.Authenticate(ctx, token); err != nil {
		return nil, err
	}
	s.mu.Lock()
	grant := s.credentials[token]
	if grant == nil || grant.revoked || grant.identity != i {
		s.mu.Unlock()
		return nil, errors.New("controller grant revoked")
	}
	epoch := grant.threadID
	s.mu.Unlock()
	starter, ok := s.backend.(ThreadStarter)
	if !ok {
		return nil, errors.New("controller creation unavailable")
	}
	return starter.Start(ctx, i, epoch, args)
}

// PreparePrompt records one setup attempt for the exact remote context. Renewal,
// reconnect and Host restart preserve it. An uncertain prompt is never reinjected.
func (g *Grant) PreparePrompt(ctx context.Context) (string, error) {
	s := g.service
	s.mu.Lock()
	if g.revoked || !g.active || !s.now().Before(g.expires) || g.identity.Member != "parent" {
		s.mu.Unlock()
		return "", errors.New("controller grant unavailable")
	}
	id, remote := g.identity.Session, g.remoteSession
	s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO collaboration_setups(session,remote) VALUES(?,?)`, id, remote)
	if err != nil {
		return "", err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return "", err
	}
	return RenderControllerPromptSlice(), nil
}

// ForgetPrompt permits reinjection only when the transport proves no submission.
func (g *Grant) ForgetPrompt(ctx context.Context) error {
	g.service.mu.Lock()
	id, remote := g.identity.Session, g.remoteSession
	g.service.mu.Unlock()
	_, err := g.service.db.ExecContext(ctx, `DELETE FROM collaboration_setups WHERE session=? AND remote=?`, id, remote)
	return err
}

// RenderControllerPromptSlice is lightweight tool discovery instruction. It
// contains no credentials, private context, or internal execution identities.
func RenderControllerPromptSlice() string {
	return SliceOpenTag + "\nYou are the Session maintainer (parent). Use Caelis StartThread to create configured collaborators; ListThreads, ReadThread and WaitThread observe their work. SendMessage exchanges peer messages; ReadMessages reads the shared collaboration conversation incrementally. " + DiscoveryInstruction() + " Search for Caelis StartThread before delegating work. These tools use the Caelis Session's configured participants and permissions.\n" + SliceCloseTag
}
