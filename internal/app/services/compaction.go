package services

import (
	"context"
	"errors"
	"fmt"
	"io"
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

var errCompactNoModelResponse = errors.New("app/services: compaction model stream ended without response")

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
	text, meta, err := s.compactText(ctx, snapshot, source, req)
	if err != nil {
		return session.Event{}, err
	}
	event := compactEvent(snapshot.Session, source, req, text, meta)
	if _, err := s.services.engine.RecordEvents(ctx, snapshot.Session.Ref, []session.Event{event}); err != nil {
		return session.Event{}, err
	}
	return session.CloneEvent(event), nil
}

func (s CompactionService) compactText(ctx context.Context, snapshot session.Snapshot, source []session.Event, req CompactSessionRequest) (string, map[string]any, error) {
	fallback := compactText(source, req.MaxChars)
	if s.services.modelProvider == nil || s.services.settings == nil {
		return fallback, nil, nil
	}
	cfg, ok, err := s.services.Models().Current(ctx, snapshot.Session.Ref)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return fallback, nil, nil
	}
	provider, err := s.services.modelProvider(ctx, cfg)
	if err != nil {
		return "", nil, err
	}
	response, err := compactProviderResponse(ctx, provider, cfg.Model, compactPrompt(source, req.MaxChars))
	if err != nil {
		if errors.Is(err, errCompactNoModelResponse) {
			return fallback, nil, nil
		}
		return "", nil, err
	}
	text := normalizeCompactModelText(response.Message.TextContent(), fallback)
	meta := map[string]any{
		"generator":      "app-services/model",
		"model_id":       strings.TrimSpace(cfg.ID),
		"model_provider": strings.TrimSpace(cfg.Provider),
		"model":          strings.TrimSpace(cfg.Model),
	}
	if response.Usage != nil {
		meta["usage"] = map[string]any{
			"input_tokens":          response.Usage.InputTokens,
			"cached_input_tokens":   response.Usage.CachedInputTokens,
			"output_tokens":         response.Usage.OutputTokens,
			"reasoning_tokens":      response.Usage.ReasoningTokens,
			"total_tokens":          response.Usage.TotalTokens,
			"context_window_tokens": response.Usage.ContextWindowTokens,
		}
	}
	return text, meta, nil
}

func compactProviderResponse(ctx context.Context, provider model.Provider, modelID string, prompt string) (model.Response, error) {
	if provider == nil {
		return model.Response{}, errors.New("app/services: compaction model provider is required")
	}
	stream, err := provider.Stream(ctx, model.Request{
		Model:    strings.TrimSpace(modelID),
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart(prompt)}}},
		Stream:   true,
		Meta: map[string]any{
			"caelis.operation": "compact",
		},
	})
	if err != nil {
		return model.Response{}, err
	}
	defer stream.Close()
	var final *model.Response
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return model.Response{}, err
		}
		if event.Response != nil {
			response := *event.Response
			final = &response
			continue
		}
		if event.Message != nil {
			message := model.CloneMessage(*event.Message)
			final = &model.Response{Message: message, Status: model.ResponseCompleted}
		}
	}
	if final == nil {
		return model.Response{}, errCompactNoModelResponse
	}
	return *final, nil
}

func compactPrompt(source []session.Event, maxChars int) string {
	return strings.Join([]string{
		"Create a durable context checkpoint for this coding-agent session.",
		"Return only the checkpoint text. Start with CONTEXT CHECKPOINT.",
		"Preserve durable objective, current progress, blockers, decisions, file facts, task handles, validation results, and next actions.",
		"Drop stale repetition, transient UI chatter, and low-value narration.",
		"",
		compactText(source, maxChars),
	}, "\n")
}

func normalizeCompactModelText(text string, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	if strings.HasPrefix(strings.ToUpper(text), "CONTEXT CHECKPOINT") {
		return text
	}
	return "CONTEXT CHECKPOINT\n\n" + text
}

func compactEvent(active session.Session, source []session.Event, req CompactSessionRequest, text string, modelMeta map[string]any) session.Event {
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
			compactMetaKey: compactMeta(source, req, modelMeta),
		},
		SessionID: active.SessionID,
	}
}

func compactMeta(source []session.Event, req CompactSessionRequest, modelMeta map[string]any) map[string]any {
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	summarizedThrough := ""
	if len(source) > 0 {
		summarizedThrough = strings.TrimSpace(source[len(source)-1].ID)
	}
	generator := "app-services/manual"
	if value, _ := modelMeta["generator"].(string); strings.TrimSpace(value) != "" {
		generator = strings.TrimSpace(value)
	}
	out := map[string]any{
		"contract_version":      compactContractVersion,
		"generator":             generator,
		"trigger":               trigger,
		"source_event_count":    len(source),
		"summarized_through_id": summarizedThrough,
	}
	for key, value := range modelMeta {
		if strings.TrimSpace(key) == "" || key == "generator" {
			continue
		}
		out[key] = value
	}
	return out
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
