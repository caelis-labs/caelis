package collaboration

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const credentialLifetime = 24 * time.Hour

// Grant authorizes one external participant activation. It is never model data.
// The Host binds it after the ACP handshake and revokes it with the connection.
type Grant struct {
	service       *Service
	token         string
	identity      Identity
	threadID      string
	remoteSession string
	expires       time.Time
	active        bool
	confirmed     bool
	revoked       bool
}

// Prepare creates a short-lived pending grant. Only a successful Bind extends
// its lifetime; tool calls cannot renew it. A new activation gets a new grant.
func (s *Service) Prepare(identity Identity, threadID string) *Grant {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for token, grant := range s.credentials {
		if !now.Before(grant.expires) || grant.revoked {
			delete(s.credentials, token)
		}
	}
	g := &Grant{service: s, token: uuid.NewString() + uuid.NewString(), identity: identity, threadID: threadID, expires: now.Add(2 * time.Minute)}
	s.credentials[g.token] = g
	return g
}

// Token returns the bearer for the private MCP process environment.
func (g *Grant) Token() string { return g.token }

// Bind activates this grant for exactly one ACP Session. Rebinding never renews
// its deadline and cannot move it to another Session.
func (g *Grant) Bind(remoteSession string) error {
	s := g.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.revoked || !s.now().Before(g.expires) || remoteSession == "" || g.threadID == "" {
		return errors.New("collaboration grant expired or invalid")
	}
	if g.active {
		if g.remoteSession != remoteSession {
			return errors.New("collaboration grant cannot change ACP Session")
		}
		return nil
	}
	for _, previous := range s.credentials {
		if previous != g && previous.identity == g.identity && previous.threadID == g.threadID {
			previous.revoked = true
		}
	}
	g.remoteSession, g.active, g.expires = remoteSession, true, s.now().Add(credentialLifetime)
	return nil
}

// Close immediately revokes the activation, including pending grants.
func (g *Grant) Close() {
	g.service.mu.Lock()
	defer g.service.mu.Unlock()
	g.revoked = true
	delete(g.service.credentials, g.token)
}

// Valid reports whether the connection can start another turn without rebinding.
// Refresh happens at an idle boundary, before the hard deadline is reached.
func (g *Grant) Valid() bool {
	g.service.mu.Lock()
	defer g.service.mu.Unlock()
	return g.active && !g.revoked && g.service.now().Add(time.Hour).Before(g.expires)
}

// Authenticate validates expiry and the current canonical participant instance.
// The returned deadline also bounds long-running mailbox waits.
func (s *Service) Authenticate(ctx context.Context, token string) (Identity, time.Time, error) {
	// First-tool calls can race the short parent participant commit after ACP
	// startup. Wait only during initial attachment, never after an observed detach.
	attachCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		s.mu.Lock()
		g := s.credentials[token]
		if g == nil || !g.active || g.revoked || !s.now().Before(g.expires) {
			s.mu.Unlock()
			return Identity{}, time.Time{}, errors.New("invalid or expired collaboration credential")
		}
		identity, threadID, remote, expires, confirmed := g.identity, g.threadID, g.remoteSession, g.expires, g.confirmed
		s.mu.Unlock()
		threads, err := s.backend.List(ctx, identity.Session)
		if err != nil {
			// Failed discovery does not establish that the participant detached.
			return Identity{}, time.Time{}, err
		}
		found := false
		for _, thread := range threads {
			if thread.Handle != identity.Member {
				continue
			}
			found = true
			if thread.ID == threadID && thread.SessionID == remote {
				s.mu.Lock()
				valid := !g.revoked && s.now().Before(g.expires)
				if valid {
					g.confirmed = true
				}
				s.mu.Unlock()
				if valid {
					return identity, expires, nil
				}
			}
		}
		if confirmed || found {
			g.Close()
			return Identity{}, time.Time{}, errors.New("collaboration participant is no longer attached")
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-attachCtx.Done():
			timer.Stop()
			return Identity{}, time.Time{}, errors.New("collaboration participant attachment is not ready")
		case <-timer.C:
		}
	}
}

// Revoke removes all external grants for a detached participant.
func (s *Service) Revoke(identity Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, grant := range s.credentials {
		if grant.identity == identity {
			grant.revoked = true
			delete(s.credentials, token)
		}
	}
}

type credentialContextKey struct{}

// CallAuthenticated keeps authorization live across long-running waits.
func (s *Service) CallAuthenticated(ctx context.Context, token string, req Request) ([]byte, error) {
	identity, expires, err := s.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(context.WithValue(ctx, credentialContextKey{}, token), expires)
	defer cancel()
	return s.Call(ctx, identity, req)
}
