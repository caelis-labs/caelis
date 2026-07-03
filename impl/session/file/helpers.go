package file

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/ports/session"
)

func (s *Store) nextID(prefix string, custom func() string) string {
	if custom != nil {
		if id := strings.TrimSpace(custom()); id != "" {
			return id
		}
	}
	if strings.TrimSpace(prefix) == "session" {
		return nextSessionID()
	}
	n := s.idCounter.Add(1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

func (s *Store) ensureUniqueEventID(event *session.Event, existing map[string]struct{}) {
	if event == nil {
		return
	}
	id := strings.TrimSpace(event.ID)
	if id != "" {
		if _, used := existing[id]; !used {
			event.ID = id
			return
		}
	}
	for attempt := 0; attempt < 8; attempt++ {
		id = strings.TrimSpace(s.nextID("event", s.eventIDGenerator))
		if id == "" {
			continue
		}
		if _, used := existing[id]; !used {
			event.ID = id
			return
		}
	}
	for {
		id = fmt.Sprintf("event-%d", s.idCounter.Add(1))
		if _, used := existing[id]; !used {
			event.ID = id
			return
		}
	}
}

func existingEventIDSet(events []*session.Event) map[string]struct{} {
	out := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func (s *Store) now() time.Time {
	return s.clock()
}

func matchesRef(sess session.Session, ref session.SessionRef) bool {
	ref = session.NormalizeSessionRef(ref)
	if ref.SessionID != "" && sess.SessionID != ref.SessionID {
		return false
	}
	if ref.AppName != "" && sess.AppName != ref.AppName {
		return false
	}
	if ref.UserID != "" && sess.UserID != ref.UserID {
		return false
	}
	if ref.WorkspaceKey != "" && sess.WorkspaceKey != ref.WorkspaceKey {
		return false
	}
	return true
}

func sanitizeSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "session"
	}
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func workspaceDirName(workspaceKey string) string {
	workspaceKey = strings.TrimSpace(workspaceKey)
	if workspaceKey == "" {
		return "workspace"
	}
	return sanitizeSessionID(workspaceKey)
}

func pathCacheKey(sessionID string, workspaceKey string) string {
	return sanitizeSessionID(sessionID)
}

func nextSessionID() string {
	var raw [7]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UTC().UnixNano())
	}
	token := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
	if len(token) > 12 {
		token = token[:12]
	}
	return "s-" + token
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shouldPersistEvent(event *session.Event) bool {
	return event != nil && !session.IsTransient(event)
}

func persistedEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if !shouldPersistEvent(event) {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	return cloneState(in)
}

func cloneState(in map[string]any) map[string]any {
	out := session.CloneState(in)
	if out == nil {
		return map[string]any{}
	}
	return out
}
