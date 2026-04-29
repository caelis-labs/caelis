package cmdsession

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
)

var (
	// ErrSessionNotFound is returned when a session ID is not found.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionAlreadyExists is returned when trying to register a duplicate session.
	ErrSessionAlreadyExists = errors.New("session already exists")
	errSessionServiceClosed = errors.New("async session service is closed")
)

// SessionManager manages multiple async sessions.
type SessionManager struct {
	sessions    map[string]*AsyncSession
	mu          sync.RWMutex
	maxSessions int
	closed      bool
}

// SessionManagerConfig configures the session manager.
type SessionManagerConfig struct {
	MaxSessions     int           // Maximum concurrent sessions (0 = unlimited)
	CleanupInterval time.Duration // Interval for cleaning up completed sessions
	MaxSessionAge   time.Duration // Maximum age for completed sessions before cleanup
}

// DefaultSessionManagerConfig returns a default configuration.
func DefaultSessionManagerConfig() SessionManagerConfig {
	return SessionManagerConfig{
		MaxSessions:     100,
		CleanupInterval: 5 * time.Minute,
		MaxSessionAge:   30 * time.Minute,
	}
}

// NewSessionManager creates a new session manager.
func NewSessionManager(cfg SessionManagerConfig) *SessionManager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 100
	}

	sm := &SessionManager{
		sessions:    make(map[string]*AsyncSession),
		maxSessions: cfg.MaxSessions,
	}

	// Start periodic cleanup
	if cfg.CleanupInterval > 0 {
		go sm.cleanupLoop(cfg.CleanupInterval, cfg.MaxSessionAge)
	}

	return sm
}

func (sm *SessionManager) cleanupLoop(interval, maxAge time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		sm.mu.Lock()
		if sm.closed {
			sm.mu.Unlock()
			return
		}

		now := time.Now()
		var toDelete []string

		for id, session := range sm.sessions {
			if session.HasExited() {
				// Clean up sessions that have been completed for too long
				if now.Sub(session.LastActivityTime()) > maxAge {
					toDelete = append(toDelete, id)
				}
			}
		}

		for _, id := range toDelete {
			if session, ok := sm.sessions[id]; ok {
				session.Close()
				delete(sm.sessions, id)
			}
		}
		sm.mu.Unlock()
	}
}

// StartSession creates and starts a new async session.
func (sm *SessionManager) StartSession(cfg AsyncSessionConfig) (*AsyncSession, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.closed {
		return nil, errSessionServiceClosed
	}

	// Check session limit
	activeCount := 0
	for _, s := range sm.sessions {
		if !s.HasExited() {
			activeCount++
		}
	}
	if activeCount >= sm.maxSessions {
		return nil, fmt.Errorf("maximum sessions (%d) reached", sm.maxSessions)
	}

	session := NewAsyncSession(cfg)
	if err := session.Start(); err != nil {
		return nil, err
	}

	sm.sessions[session.ID] = session
	return session, nil
}

// GetSession retrieves a session by ID.
func (sm *SessionManager) GetSession(id string) (*AsyncSession, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

// GetSessionStatus retrieves the status of a session.
func (sm *SessionManager) GetSessionStatus(id string) (SessionStatus, error) {
	session, err := sm.GetSession(id)
	if err != nil {
		return SessionStatus{}, err
	}
	return session.Status(), nil
}

// WriteInput sends input to a session.
func (sm *SessionManager) WriteInput(id string, input []byte) error {
	session, err := sm.GetSession(id)
	if err != nil {
		return err
	}
	return session.WriteInput(input)
}

// ReadOutput reads output from a session.
func (sm *SessionManager) ReadOutput(id string, stdoutMarker, stderrMarker int64) (stdout, stderr []byte, newStdoutMarker, newStderrMarker int64, err error) {
	session, err := sm.GetSession(id)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	stdout, stderr, newStdoutMarker, newStderrMarker = session.ReadOutput(stdoutMarker, stderrMarker)
	return stdout, stderr, newStdoutMarker, newStderrMarker, nil
}

// TerminateSession forcefully terminates a session.
func (sm *SessionManager) TerminateSession(id string) error {
	session, err := sm.GetSession(id)
	if err != nil {
		return err
	}
	return session.Terminate()
}

// RemoveSession terminates and removes a session.
func (sm *SessionManager) RemoveSession(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}

	session.Close()
	delete(sm.sessions, id)
	return nil
}

// ListSessions returns information about all sessions.
func (sm *SessionManager) ListSessions() []SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	infos := make([]SessionInfo, 0, len(sm.sessions))
	for _, session := range sm.sessions {
		infos = append(infos, session.Info())
	}
	return infos
}

// ListActiveSessions returns information about running sessions only.
func (sm *SessionManager) ListActiveSessions() []SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var infos []SessionInfo
	for _, session := range sm.sessions {
		if !session.HasExited() {
			infos = append(infos, session.Info())
		}
	}
	return infos
}

// WaitSession waits for a session to complete.
func (sm *SessionManager) WaitSession(ctx context.Context, id string) (int, error) {
	session, err := sm.GetSession(id)
	if err != nil {
		return -1, err
	}
	return session.Wait(ctx)
}

// WaitSessionWithTimeout waits for a session with a timeout.
func (sm *SessionManager) WaitSessionWithTimeout(id string, timeout time.Duration) (int, error) {
	session, err := sm.GetSession(id)
	if err != nil {
		return -1, err
	}
	return session.WaitWithTimeout(timeout)
}

// GetResult gets the command result if the session has exited.
func (sm *SessionManager) GetResult(id string) (sdksandbox.CommandResult, error) {
	session, err := sm.GetSession(id)
	if err != nil {
		return sdksandbox.CommandResult{}, err
	}
	return session.GetResult()
}

// SessionCount returns the total number of sessions.
func (sm *SessionManager) SessionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

// ActiveSessionCount returns the number of running sessions.
func (sm *SessionManager) ActiveSessionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	count := 0
	for _, s := range sm.sessions {
		if !s.HasExited() {
			count++
		}
	}
	return count
}

// Close terminates all sessions and closes the manager.
func (sm *SessionManager) Close() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.closed {
		return nil
	}
	sm.closed = true

	var firstErr error
	for id, session := range sm.sessions {
		if err := session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(sm.sessions, id)
	}

	return firstErr
}
