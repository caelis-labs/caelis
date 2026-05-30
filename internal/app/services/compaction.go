package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

const (
	compactMetaKey         = "compact"
	compactContractVersion = 1
	defaultCompactMaxChars = 12000
)

type CompactionService struct {
	services Services
}

type CompactSessionRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
	Trigger    string      `json:"trigger,omitempty"`
	MaxChars   int         `json:"max_chars,omitempty"`
}

func (s CompactionService) Compact(ctx context.Context, req CompactSessionRequest) (session.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.services.engine == nil {
		return session.Event{}, errors.New("app/services: runtime engine is required")
	}
	ref := defaultSessionRef(s.services.runtime, req.SessionRef)
	if strings.TrimSpace(ref.SessionID) == "" {
		return session.Event{}, fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	snapshot, err := s.services.Sessions().Load(ctx, ref)
	if err != nil {
		return session.Event{}, err
	}
	source := compactSourceEvents(snapshot.Events)
	event := compactEvent(snapshot.Session, source, req)
	if _, err := s.services.engine.RecordEvents(ctx, snapshot.Session.Ref, []session.Event{event}); err != nil {
		return session.Event{}, err
	}
	return session.CloneEvent(event), nil
}

func compactEvent(active session.Session, source []session.Event, req CompactSessionRequest) session.Event {
	text := compactText(source, req.MaxChars)
	message := model.Message{
		Role: model.RoleUser,
		Parts: []model.Part{
			model.NewTextPart(text),
		},
		Meta: map[string]any{
			"caelis_compact_checkpoint": true,
		},
	}
	return session.Event{
		Type:       session.EventCompact,
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "caelis", Name: "caelis"},
		Message:    &message,
		Meta: map[string]any{
			compactMetaKey: compactMeta(source, req),
		},
		SessionID: active.SessionID,
	}
}

func compactMeta(source []session.Event, req CompactSessionRequest) map[string]any {
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	summarizedThrough := ""
	if len(source) > 0 {
		summarizedThrough = strings.TrimSpace(source[len(source)-1].ID)
	}
	return map[string]any{
		"contract_version":      compactContractVersion,
		"generator":             "app-services/manual",
		"trigger":               trigger,
		"source_event_count":    len(source),
		"summarized_through_id": summarizedThrough,
	}
}

func compactSourceEvents(events []session.Event) []session.Event {
	if len(events) == 0 {
		return nil
	}
	start := 0
	for i := len(events) - 1; i >= 0; i-- {
		if session.IsTransient(events[i]) {
			continue
		}
		if isCompactCheckpoint(events[i]) {
			start = i
			break
		}
	}
	out := make([]session.Event, 0, len(events)-start)
	for _, event := range events[start:] {
		if session.IsTransient(event) || event.Message == nil {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func compactText(events []session.Event, maxChars int) string {
	if maxChars <= 0 {
		maxChars = defaultCompactMaxChars
	}
	lines := []string{
		"CONTEXT CHECKPOINT",
		"",
		"The following checkpoint replaces earlier model-visible session history.",
		"",
		"## Source Summary",
	}
	if len(events) == 0 {
		lines = append(lines, "- No prior model-visible conversation content.")
		return strings.Join(lines, "\n")
	}
	remaining := maxChars
	for _, event := range events {
		role := compactEventRole(event)
		text := compactEventText(event)
		if text == "" {
			continue
		}
		line := "- " + role + ": " + compactOneLine(text)
		if len(line) > remaining {
			if remaining <= len("- omitted: ...") {
				break
			}
			line = line[:remaining-len("...")] + "..."
		}
		lines = append(lines, line)
		remaining -= len(line)
		if remaining <= 0 {
			break
		}
	}
	if len(lines) == 5 {
		lines = append(lines, "- No prior text content.")
	}
	return strings.Join(lines, "\n")
}

func compactEventRole(event session.Event) string {
	if event.Message != nil && strings.TrimSpace(string(event.Message.Role)) != "" {
		return strings.TrimSpace(string(event.Message.Role))
	}
	if event.Type != "" {
		return strings.TrimSpace(string(event.Type))
	}
	return "event"
}

func compactEventText(event session.Event) string {
	if event.Message == nil {
		return ""
	}
	return strings.TrimSpace(event.Message.TextContent())
}

func compactOneLine(text string) string {
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func isCompactCheckpoint(event session.Event) bool {
	if event.Type == session.EventCompact {
		return true
	}
	if event.Meta == nil {
		return false
	}
	_, ok := event.Meta[compactMetaKey]
	return ok
}
