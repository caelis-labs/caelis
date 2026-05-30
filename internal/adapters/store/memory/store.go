// Package memory provides an in-memory core session store for the new runtime
// contracts. It is intended for tests and local ephemeral composition.
package memory

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
)

type Store struct {
	mu       sync.RWMutex
	nextID   atomic.Uint64
	sessions map[string]*record
}

type record struct {
	session session.Session
	events  []session.Event
	state   session.State
}

func New() *Store {
	return &Store{sessions: map[string]*record{}}
}

func (s *Store) Create(ctx context.Context, req session.StartRequest) (session.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.Session{}, err
	}
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	if appName == "" || userID == "" {
		return session.Session{}, fmt.Errorf("%w: app name and user id are required", session.ErrInvalid)
	}
	id := strings.TrimSpace(req.PreferredSessionID)
	if id == "" {
		id = fmt.Sprintf("sess-%d", s.nextID.Add(1))
	}
	ref := session.NormalizeRef(session.Ref{
		AppName:      appName,
		UserID:       userID,
		SessionID:    id,
		WorkspaceKey: req.Workspace.Key,
	})
	now := time.Now().UTC()
	next := session.Session{
		Ref:       ref,
		Workspace: req.Workspace,
		Title:     strings.TrimSpace(req.Title),
		Meta:      maps.Clone(req.Meta),
		Controller: session.ControllerBinding{
			Kind:       session.ControllerBuiltin,
			ID:         "builtin",
			AttachedAt: now,
			Source:     "memory-store",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[id]; ok {
		return session.CloneSession(existing.session), nil
	}
	s.sessions[id] = &record{session: next, state: session.State{}}
	return session.CloneSession(next), nil
}

func (s *Store) Load(ctx context.Context, ref session.Ref) (session.Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.Snapshot{}, err
	}
	rec, err := s.get(ref)
	if err != nil {
		return session.Snapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return session.Snapshot{
		Session: session.CloneSession(rec.session),
		Events:  cloneEvents(rec.events, false),
		State:   cloneState(rec.state),
		Cursor:  session.Cursor(strconv.Itoa(len(rec.events))),
	}, nil
}

func (s *Store) Append(ctx context.Context, ref session.Ref, events []session.Event) (session.Cursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(events) == 0 {
		rec, err := s.get(ref)
		if err != nil {
			return "", err
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		return session.Cursor(strconv.Itoa(len(rec.events))), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(ref.SessionID)
	rec, ok := s.sessions[id]
	if !ok {
		return "", session.ErrNotFound
	}
	for _, event := range events {
		next := session.CloneEvent(event)
		if strings.TrimSpace(next.ID) == "" {
			next.ID = fmt.Sprintf("evt-%d", len(rec.events)+1)
		}
		if strings.TrimSpace(next.SessionID) == "" {
			next.SessionID = id
		}
		if next.Time.IsZero() {
			next.Time = time.Now().UTC()
		}
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		rec.events = append(rec.events, next)
	}
	rec.session.UpdatedAt = time.Now().UTC()
	return session.Cursor(strconv.Itoa(len(rec.events))), nil
}

func (s *Store) Events(ctx context.Context, query session.EventQuery) (session.EventPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.EventPage{}, err
	}
	rec, err := s.get(query.Ref)
	if err != nil {
		return session.EventPage{}, err
	}
	after, err := parseCursor(query.After)
	if err != nil {
		return session.EventPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if after > len(rec.events) {
		after = len(rec.events)
	}
	limit := query.Limit
	out := make([]session.Event, 0)
	for _, event := range rec.events[after:] {
		if !query.IncludeTransient && session.IsTransient(event) {
			after++
			continue
		}
		out = append(out, session.CloneEvent(event))
		after++
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return session.EventPage{
		Events:     out,
		NextCursor: session.Cursor(strconv.Itoa(after)),
	}, nil
}

func (s *Store) UpdateState(ctx context.Context, ref session.Ref, patch session.StatePatch) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if patch == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[strings.TrimSpace(ref.SessionID)]
	if !ok {
		return session.ErrNotFound
	}
	next, err := patch(cloneState(rec.state))
	if err != nil {
		return err
	}
	rec.state = cloneState(next)
	rec.session.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *Store) get(ref session.Ref) (*record, error) {
	if s == nil {
		return nil, session.ErrNotFound
	}
	ref = session.NormalizeRef(ref)
	if ref.SessionID == "" {
		return nil, fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.sessions[ref.SessionID]
	if !ok {
		return nil, session.ErrNotFound
	}
	return rec, nil
}

func parseCursor(cursor session.Cursor) (int, error) {
	raw := strings.TrimSpace(string(cursor))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: invalid event cursor", session.ErrInvalid)
	}
	return value, nil
}

func cloneEvents(in []session.Event, includeTransient bool) []session.Event {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.Event, 0, len(in))
	for _, event := range in {
		if !includeTransient && session.IsTransient(event) {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func cloneState(in session.State) session.State {
	if in == nil {
		return nil
	}
	return session.State(maps.Clone(in))
}
