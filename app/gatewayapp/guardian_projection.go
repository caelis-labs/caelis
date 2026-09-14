package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const guardianSourceProjection = "guardian_source_projection"

// guardianProjection consumes canonical events once. This bounded cache is
// disposable; its source sequence is independent of completed review history.
type guardianProjection struct {
	mu     sync.Mutex
	after  uint64
	cursor guardianParentCanonicalCursor
	events []*session.Event
}

func (p *guardianProjection) read(ctx context.Context, service session.Service, ref session.SessionRef) ([]*session.Event, guardianParentCanonicalCursor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	reader, ok := service.(session.PagedReader)
	checkpoints, checkpointOK := service.(session.EventCheckpointReader)
	if !ok || !checkpointOK {
		return nil, p.cursor, fmt.Errorf("guardian requires canonical event pagination and checkpoints")
	}
	cut, err := checkpoints.EventCheckpoint(ctx, ref)
	if err != nil {
		return nil, p.cursor, err
	}
	if cut.ThroughSeq < p.after {
		p.after, p.cursor, p.events = 0, guardianParentCanonicalCursor{}, nil
	}
	for p.after < cut.ThroughSeq {
		page, err := reader.EventsPage(ctx, session.EventPageRequest{SessionRef: ref, AfterSeq: p.after, ThroughSeq: cut.ThroughSeq, Limit: 64, Visibility: session.EventPageCanonical})
		if err != nil {
			return nil, p.cursor, err
		}
		for _, event := range page.Events {
			if event == nil || event.Seq <= p.after {
				continue
			}
			p.cursor = guardianParentCanonicalCursor{EventID: event.ID, EventSeq: event.Seq}
			if projected := guardianProjectEvent(event); projected != nil {
				p.events = append(p.events, projected)
				// Retention follows source order, not the caller's page size or
				// approval cadence, so a reopened source rebuilds the same cut.
				p.events = guardianTrimUsers(p.events, 24000)
				p.events = guardianTrimTurns(p.events, 48000, "")
			}
		}
		if !page.HasMore {
			p.after = cut.ThroughSeq
			break
		}
		if page.NextSeq <= p.after {
			return nil, p.cursor, fmt.Errorf("guardian event page did not advance")
		}
		p.after = page.NextSeq
	}
	return session.CloneEvents(p.events), p.cursor, nil
}

// guardianProjectEvent is independent of the pending action, paging and review
// completion. Calls and late results retain their distinct source identities.
func guardianProjectEvent(e *session.Event) *session.Event {
	if e == nil || !session.IsCanonicalHistoryEvent(e) {
		return nil
	}
	if e.Meta[guardianSourceProjection] == true {
		return session.CloneEvent(e)
	}
	var text string
	user := session.EventTypeOf(e) == session.EventTypeUser && (e.Actor.Kind == session.ActorKindUser || e.Actor.Kind == "")
	if user {
		text = fmt.Sprintf("User message [session=%s seq=%d event=%s]:\n%s", e.SessionID, e.Seq, e.ID, guardianFold(guardianVisibleText(e), 8192))
	} else if (session.EventTypeOf(e) == session.EventTypeToolCall || session.EventTypeOf(e) == session.EventTypeToolResult) && session.IsMainInvocationVisibleEvent(e) && e.Tool != nil {
		kind, value := "Operation", e.Tool.Input
		if session.EventTypeOf(e) == session.EventTypeToolResult {
			kind, value = "Tool result", e.Tool.Output
		}
		text = fmt.Sprintf("%s [session=%s seq=%d event=%s tool_call_id=%s tool=%s status=%s]\n%s", kind, e.SessionID, e.Seq, e.ID, e.Tool.ID, e.Tool.Name, e.Tool.Status, guardianEvidenceJSON(value))
		if kind == "Tool result" {
			facts := map[string]any{}
			for _, key := range []string{"status", "error", "error_kind", "exit_code", "is_error", "truncated"} {
				if v, ok := value[key]; ok {
					facts[key] = v
				}
			}
			if len(facts) > 0 {
				text = fmt.Sprintf("Result status fields (untrusted evidence): %s\n", guardianEvidenceJSON(facts)) + text
			}
		}
		if len(value) == 0 && kind == "Tool result" {
			text += "\n" + guardianFold(guardianVisibleText(e), 2048)
		}
	} else {
		return nil
	}
	one := guardianEvidenceEvent(text)
	one.ID, one.Seq, one.SessionID = e.ID, e.Seq, e.SessionID
	one.Meta[guardianSourceProjection] = true
	if user {
		one.Meta[guardianUserSource] = fmt.Sprintf("%s:%d", e.SessionID, e.Seq)
	} else {
		// Fixed source blocks make retention independent of approval boundaries.
		one.Meta[guardianTurnKey] = fmt.Sprintf("source:%s:%d", e.SessionID, e.Seq/4)
	}
	return one
}

// Bound before serialization: untrusted nested outputs must not require a full
// JSON copy merely to determine that only a small fragment fits.
func guardianEvidenceJSON(value any) string {
	nodes := 128
	var bound func(any, int) any
	bound = func(v any, depth int) any {
		nodes--
		if nodes < 0 || depth > 8 {
			return "[omitted: structural limit]"
		}
		switch v := v.(type) {
		case string:
			return guardianFold(v, 2048)
		case json.RawMessage:
			return guardianFold(string(v), 2048)
		case map[string]any:
			// Keep deterministic key selection with constant temporary memory.
			// Preserve failure/status fields ahead of arbitrary payload keys.
			keys := make([]string, 0, 33)
			for key := range v {
				i := sort.Search(len(keys), func(i int) bool { return guardianEvidenceKeyLess(key, keys[i]) })
				if i >= 32 {
					continue
				}
				keys = append(keys, "")
				copy(keys[i+1:], keys[i:])
				keys[i] = key
				if len(keys) > 32 {
					keys = keys[:32]
				}
			}
			out := make(map[string]any)
			if len(v) > len(keys) {
				out["_omitted"] = "structural limit"
			}
			for i, key := range keys {
				if i >= 32 || nodes <= 0 {
					out["_omitted"] = "structural limit"
					break
				}
				out[guardianFold(key, 128)] = bound(v[key], depth+1)
			}
			return out
		case []any:
			out := make([]any, 0, min(len(v), 32))
			for i, item := range v {
				if i >= 32 || nodes <= 0 {
					out = append(out, "[omitted]")
					break
				}
				out = append(out, bound(item, depth+1))
			}
			return out
		case nil, bool, float64, float32, int, int64, uint64, json.Number:
			return v
		default:
			return "[unavailable structured value]"
		}
	}
	raw, _ := json.Marshal(bound(value, 0))
	return guardianFold(string(raw), 3072)
}

func guardianEvidenceKeyLess(a, b string) bool {
	priority := func(key string) bool {
		switch key {
		case "error", "error_kind", "exit_code", "status", "is_error", "truncated", "stderr":
			return true
		default:
			return false
		}
	}
	if priority(a) != priority(b) {
		return priority(a)
	}
	return a < b
}
