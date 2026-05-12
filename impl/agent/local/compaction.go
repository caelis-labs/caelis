package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

// CompactionConfig controls codex-style checkpoint compaction.
type CompactionConfig struct {
	Enabled                     bool
	WatermarkRatio              float64
	ForceWatermarkRatio         float64
	DefaultContextWindowTokens  int
	ReserveOutputTokens         int
	SafetyMarginTokens          int
	SegmentTokenBudget          int
	MaxSegmentDepth             int
	MaxRetryAttempts            int
	RetryBaseDelay              time.Duration
	RetryMaxDelay               time.Duration
	EstimatedPromptPrefixTokens int
}

func normalizeCompactionConfig(cfg CompactionConfig) CompactionConfig {
	if cfg.DefaultContextWindowTokens <= 0 {
		cfg.DefaultContextWindowTokens = 200000
	}
	if cfg.ReserveOutputTokens <= 0 {
		cfg.ReserveOutputTokens = 5000
	}
	if cfg.SafetyMarginTokens <= 0 {
		cfg.SafetyMarginTokens = 2048
	}
	if cfg.SegmentTokenBudget <= 0 {
		cfg.SegmentTokenBudget = 24000
	}
	if cfg.MaxSegmentDepth <= 0 {
		cfg.MaxSegmentDepth = 8
	}
	if cfg.MaxRetryAttempts <= 0 {
		cfg.MaxRetryAttempts = 3
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = 500 * time.Millisecond
	}
	if cfg.RetryMaxDelay <= 0 {
		cfg.RetryMaxDelay = 8 * time.Second
	}
	if cfg.EstimatedPromptPrefixTokens < 0 {
		cfg.EstimatedPromptPrefixTokens = 0
	}
	return cfg
}

type codexStyleCompactor struct {
	cfg CompactionConfig
}

func newCodexStyleCompactor(cfg CompactionConfig) compact.Engine {
	return &codexStyleCompactor{cfg: normalizeCompactionConfig(cfg)}
}

func (c *codexStyleCompactor) Prepare(ctx context.Context, req compact.Request) (compact.Result, error) {
	promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
	usagePromptEvents := promptEventsWithPending(promptEvents, req.PendingEvents)
	result := compact.Result{
		PromptEvents: promptEvents,
		Usage:        c.snapshotUsage(req, usagePromptEvents),
	}
	if !c.cfg.Enabled || req.Model == nil {
		return result, nil
	}
	decision, err := c.decide(ctx, result.Usage, req)
	if err != nil || !decision.ShouldCompact {
		return result, err
	}
	compacted, err := c.compact(ctx, req, decision.Reason)
	if err != nil {
		return result, err
	}
	return compacted, nil
}

func (c *codexStyleCompactor) Force(ctx context.Context, req compact.Request, trigger string) (compact.Result, error) {
	promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
	result := compact.Result{
		PromptEvents: promptEvents,
		Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
	}
	if compactableEventCount(req.Events) == 0 {
		return result, nil
	}
	if req.Model == nil {
		return compact.Result{}, errors.New("impl/agent/local: compact model is required")
	}
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		trigger = "manual"
	}
	return c.compact(ctx, req, trigger)
}

func (c *codexStyleCompactor) CompactOnOverflow(ctx context.Context, req compact.Request, cause error) (compact.Result, error) {
	if !c.cfg.Enabled || req.Model == nil {
		promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
		return compact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, cause
	}
	if !isCompactionOverflowError(cause) {
		return compact.Result{}, cause
	}
	return c.compact(ctx, req, "overflow_recovery")
}

func (c *codexStyleCompactor) decide(_ context.Context, usage compact.UsageSnapshot, req compact.Request) (compact.TriggerDecision, error) {
	if usage.EffectiveInputBudget <= 0 || req.Model == nil {
		return compact.TriggerDecision{}, nil
	}
	if compactableEventCount(req.Events) == 0 {
		return compact.TriggerDecision{}, nil
	}
	softRatio, forceRatio := dynamicWatermarks(usage.ContextWindowTokens, c.cfg.WatermarkRatio, c.cfg.ForceWatermarkRatio)
	ratio := float64(usage.TotalTokens) / float64(usage.EffectiveInputBudget)
	switch {
	case ratio >= forceRatio:
		return compact.TriggerDecision{ShouldCompact: true, Reason: "context_limit"}, nil
	case ratio >= softRatio:
		return compact.TriggerDecision{ShouldCompact: true, Reason: "context_watermark"}, nil
	default:
		return compact.TriggerDecision{}, nil
	}
}

func (c *codexStyleCompactor) compact(ctx context.Context, req compact.Request, trigger string) (compact.Result, error) {
	baseEvent, baseData, _ := compact.LatestCompactEvent(req.Events)
	baseText := compactTextFromEvent(baseEvent)
	delta := compactableEvents(req.Events)
	if len(delta) == 0 {
		promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
		return compact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, nil
	}
	summaryEvents := session.CloneEvents(delta)
	if len(summaryEvents) == 0 {
		promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
		return compact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, nil
	}

	compactText, err := c.generateCompactMarkdown(ctx, req.Model, baseText, summaryEvents)
	if err != nil {
		return compact.Result{}, err
	}
	data := compact.CompactEventData{
		Revision:            baseData.Revision + 1,
		ContractVersion:     compact.CompactContractVersion,
		SummarizedThroughID: lastEventID(delta),
		Generator:           "model_markdown",
		Trigger:             strings.TrimSpace(trigger),
		SourceEventCount:    len(summaryEvents),
	}
	compactEvent := buildCompactEvent(req.Session, compactText, data)
	promptEvents := compact.PromptEventsFromLatestCompact([]*session.Event{compactEvent})
	usage := c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents))
	data.TotalTokens = usage.TotalTokens
	data.ContextWindowTokens = usage.ContextWindowTokens
	if compactEvent.Meta == nil {
		compactEvent.Meta = map[string]any{}
	}
	compactEvent.Meta[compact.MetaKeyCompact] = compact.CompactEventDataValue(data)
	return compact.Result{
		Compacted:    true,
		CompactText:  compactText,
		CompactEvent: compactEvent,
		PromptEvents: promptEvents,
		Usage:        usage,
	}, nil
}

func (c *codexStyleCompactor) snapshotUsage(req compact.Request, promptEvents []*session.Event) compact.UsageSnapshot {
	window := resolveContextWindowTokens(req.Model, c.cfg.DefaultContextWindowTokens)
	return snapshotUsageWithResolvedWindow(promptEvents, window, c.cfg)
}

// ComputeUsageSnapshot applies the same provider-aware usage snapshot logic
// used by compaction, but without mutating session history.
func ComputeUsageSnapshot(events []*session.Event, pendingEvents []*session.Event, contextWindow int, cfg CompactionConfig) compact.UsageSnapshot {
	promptEvents := compact.PromptEventsFromLatestCompact(events)
	return snapshotUsageWithResolvedWindow(promptEventsWithPending(promptEvents, pendingEvents), contextWindow, cfg)
}

func snapshotUsageWithResolvedWindow(promptEvents []*session.Event, window int, cfg CompactionConfig) compact.UsageSnapshot {
	cfg = normalizeCompactionConfig(cfg)
	if window <= 0 {
		window = cfg.DefaultContextWindowTokens
	}
	reserve := resolveReserveOutputTokens(window, cfg.ReserveOutputTokens)
	safety := resolveSafetyMarginTokens(window, cfg.SafetyMarginTokens)
	effective := resolveEffectiveInputBudget(window, reserve, safety)

	total := 0
	delta := 0
	prefix := 0
	asOfEventID := ""
	source := compact.UsageSourceEstimated
	if snapshot, ok := latestProviderTokenSnapshot(promptEvents); ok {
		total = snapshot.BaselineTokens
		delta = estimateTokensFromIndex(promptEvents, snapshot.DeltaStartIndex)
		total += delta
		asOfEventID = snapshot.EventID
		source = compact.UsageSourceProvider
	} else {
		prefix = cfg.EstimatedPromptPrefixTokens
		total = estimatePromptEventsTokens(promptEvents) + prefix
	}
	return compact.UsageSnapshot{
		TotalTokens:           total,
		ContextWindowTokens:   window,
		EffectiveInputBudget:  effective,
		EstimatedDeltaTokens:  delta,
		EstimatedPrefixTokens: prefix,
		Source:                source,
		AsOfEventID:           asOfEventID,
	}
}

func (c *codexStyleCompactor) generateCompactMarkdown(
	ctx context.Context,
	llm model.LLM,
	baseText string,
	events []*session.Event,
) (string, error) {
	if len(events) == 0 {
		return normalizeCompactMarkdown(baseText), nil
	}
	text, err := c.generateCompactMarkdownOnce(ctx, llm, baseText, events)
	if err == nil {
		return text, nil
	}
	if isCompactionOverflowError(err) {
		return c.generateCompactMarkdownSegmented(ctx, llm, baseText, events, 0)
	}
	return "", err
}

func (c *codexStyleCompactor) generateCompactMarkdownSegmented(
	ctx context.Context,
	llm model.LLM,
	baseText string,
	events []*session.Event,
	depth int,
) (string, error) {
	if len(events) == 0 {
		return normalizeCompactMarkdown(baseText), nil
	}
	if depth >= c.cfg.MaxSegmentDepth || len(events) <= 1 {
		return "", &model.ContextOverflowError{Cause: errors.New("compact segment still exceeds context budget")}
	}
	segments := splitEventsByTokenBudget(events, c.cfg.SegmentTokenBudget)
	if len(segments) <= 1 {
		mid := len(events) / 2
		if mid <= 0 || mid >= len(events) {
			return "", &model.ContextOverflowError{Cause: errors.New("unable to split compaction segment further")}
		}
		segments = [][]*session.Event{events[:mid], events[mid:]}
	}
	current := baseText
	for _, segment := range segments {
		if len(segment) == 0 {
			continue
		}
		update, err := c.generateCompactMarkdownOnce(ctx, llm, current, segment)
		if err != nil {
			if isCompactionOverflowError(err) {
				update, err = c.generateCompactMarkdownSegmented(ctx, llm, current, segment, depth+1)
			}
			if err != nil {
				return "", err
			}
		}
		current = update
	}
	return normalizeCompactMarkdown(current), nil
}

func (c *codexStyleCompactor) generateCompactMarkdownOnce(
	ctx context.Context,
	llm model.LLM,
	baseText string,
	events []*session.Event,
) (string, error) {
	var lastErr error
	for attempt := 0; attempt < c.cfg.MaxRetryAttempts; attempt++ {
		if attempt > 0 {
			delay := model.RetryDelayForAttempt(attempt-1, c.cfg.RetryBaseDelay, c.cfg.RetryMaxDelay)
			if err := sleepCompactionRetryDelay(ctx, delay); err != nil {
				return "", err
			}
		}
		text, err := modelCompactMarkdown(ctx, llm, baseText, events)
		if err == nil {
			return text, nil
		}
		if isCompactionOverflowError(err) {
			return "", err
		}
		lastErr = err
		if !model.IsRetryableLLMError(err) {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("compact generation failed")
	}
	return "", lastErr
}

func sleepCompactionRetryDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runtime) prepareInvocationContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	req agent.RunRequest,
	pendingInput *session.Event,
) ([]*session.Event, map[string]any, error) {
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return nil, nil, err
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return nil, nil, err
	}
	events = mainInvocationEvents(events)
	state, err := r.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	if state == nil {
		state = map[string]any{}
	}
	result, err := r.compactor.Prepare(ctx, compact.Request{
		Session:       activeSession,
		SessionRef:    ref,
		Events:        events,
		PendingEvents: pendingEventsForCompaction(pendingInput),
		Model:         req.AgentSpec.Model,
	})
	if err != nil {
		return nil, nil, err
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, activeSession, ref, result)
		if appendErr != nil {
			return nil, nil, appendErr
		}
		return compact.PromptEventsFromLatestCompact(append(events, persisted)), state, nil
	}
	return result.PromptEvents, state, nil
}

type CompactRequest struct {
	SessionRef session.SessionRef
	Model      model.LLM
	Trigger    string
}

type CompactResult struct {
	Session   session.Session
	Compacted bool
	Event     *session.Event
	Usage     compact.UsageSnapshot
}

func (r *Runtime) Compact(ctx context.Context, req CompactRequest) (CompactResult, error) {
	if r == nil {
		return CompactResult{}, errors.New("impl/agent/local: runtime is unavailable")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return CompactResult{}, err
	}
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return CompactResult{}, err
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return CompactResult{}, err
	}
	forceCompactor, ok := r.compactor.(compact.ForceEngine)
	if !ok {
		return CompactResult{}, errors.New("impl/agent/local: compactor does not support forced compaction")
	}
	result, err := forceCompactor.Force(ctx, compact.Request{
		Session:    activeSession,
		SessionRef: ref,
		Events:     events,
		Model:      req.Model,
	}, req.Trigger)
	if err != nil {
		return CompactResult{}, err
	}
	out := CompactResult{
		Session:   activeSession,
		Compacted: result.Compacted,
		Usage:     result.Usage,
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, activeSession, ref, result)
		if appendErr != nil {
			return CompactResult{}, appendErr
		}
		out.Event = persisted
	}
	return out, nil
}

func (r *Runtime) updateCompactionUsageFromBatch(_ context.Context, _ session.SessionRef, _ []*session.Event) error {
	return nil
}

func (r *Runtime) persistCompactionArtifacts(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	result compact.Result,
) (*session.Event, error) {
	if result.CompactEvent == nil {
		return nil, errors.New("impl/agent/local: compact event is required")
	}
	persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      normalizeEvent(activeSession, "", result.CompactEvent),
	})
	if err != nil {
		return nil, err
	}
	return persisted, nil
}

func modelCompactMarkdown(
	ctx context.Context,
	llm model.LLM,
	baseText string,
	events []*session.Event,
) (string, error) {
	input := renderCheckpointCompactionInput(baseText, events)
	if strings.TrimSpace(input) == "" {
		return "", errors.New("empty compaction input")
	}
	request := &model.Request{
		Instructions: []model.Part{model.NewTextPart(strings.TrimSpace(`
You are performing a CONTEXT CHECKPOINT COMPACTION for a coding agent.
Return only one structured Markdown handoff note. Do not return JSON. Do not use code fences.

Required shape:
CONTEXT CHECKPOINT

## Current Objective
- ...

## User Constraints And Corrections
- Preserve every durable user requirement, correction, approval, or rejection from the compacted range.
- Keep recent user wording verbatim when it changes what should happen next.

## Current Plan And Progress
- Preserve PLAN events as ordinary history, including item statuses when available.
- Distinguish completed work from work that still needs action.

## Key Files And Facts
- Include file paths plus useful symbols or line ranges when they were learned from READ/SEARCH/GLOB/PATCH output.

## Validation And Tool Results
- Keep relevant build/test/vet results, sandbox failures, and unread or incomplete tool outcomes.

## Open Questions Or Risks
- ...

## Next Actions
1. ...

Rules:
- Preserve the current objective, blocker, next action, user constraints, and execution progress with very high fidelity.
- If newer history changes the task, correction, approval state, blocker, or next action, the newer history wins over the old checkpoint.
- Treat the existing compact checkpoint as a reference, not as text that must be kept verbatim.
- Keep durable direction, blockers, file facts, handles, validation results, and execution progress. Drop stale, repetitive, or superseded detail.
- Do not turn the checkpoint into a schema dump. Use concise Markdown headings and bullets.
- Ignore acknowledgment-only turns such as "ack", "ok", or "done" unless they carry real progress or approve execution.
- Ignore reply-format scaffolding such as "reply exactly" or "answer with exactly" when extracting durable state.
`))},
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, input),
		},
		Stream: false,
	}
	final, err := collectCompactionResponse(ctx, llm, request)
	if err != nil {
		return "", err
	}
	text := normalizeCompactMarkdown(strings.TrimSpace(final.Message.TextContent()))
	if compactMarkdownLooksEmpty(text) {
		salvaged, salvageErr := salvageCompactMarkdown(ctx, llm, input, text)
		if salvageErr == nil && !compactMarkdownLooksEmpty(salvaged) {
			return salvaged, nil
		}
		return "", fmt.Errorf("impl/agent/local: insufficient compact checkpoint payload: %s", compactText(text, 320))
	}
	return text, nil
}

func compactMarkdownLooksEmpty(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	return len(text) < 24
}

func salvageCompactMarkdown(ctx context.Context, llm model.LLM, input string, prior string) (string, error) {
	request := &model.Request{
		Instructions: []model.Part{model.NewTextPart(strings.TrimSpace(`
You are repairing an empty or low-information context checkpoint for a coding agent.
Return only one structured Markdown handoff note. Do not return JSON.
Start with:
CONTEXT CHECKPOINT

## Current Objective
- ...

## User Constraints And Corrections
- ...

## Current Plan And Progress
- ...

## Next Actions
1. ...

Rules:
- Preserve exact wording for the current objective, blockers, and next actions when available.
- Do not leave Objective or Next action empty if the source contains them.
- Preserve durable user corrections and approvals from the compacted range.
- Ignore acknowledgment-only turns and reply-format scaffolding.
- Add only the minimum extra detail needed to continue the task.
`))},
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, strings.TrimSpace(input+"\n\nPrevious invalid compact output:\n"+prior)),
		},
		Stream: false,
	}
	final, err := collectCompactionResponse(ctx, llm, request)
	if err != nil {
		return "", err
	}
	return normalizeCompactMarkdown(strings.TrimSpace(final.Message.TextContent())), nil
}

func renderCheckpointCompactionInput(
	baseText string,
	events []*session.Event,
) string {
	var b strings.Builder
	if strings.TrimSpace(baseText) != "" {
		b.WriteString("# Existing Compact Checkpoint (reference only)\n")
		b.WriteString(strings.TrimSpace(baseText))
		b.WriteString("\n\n")
	}
	b.WriteString("# Event Replay Since Last Compact\n")
	for _, event := range events {
		line := renderCompactionEvent(event)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func collectCompactionResponse(ctx context.Context, llm model.LLM, req *model.Request) (*model.Response, error) {
	var final *model.Response
	for event, err := range llm.Generate(ctx, req) {
		if err != nil {
			return nil, err
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if final == nil {
		return nil, errors.New("impl/agent/local: model returned no compaction response")
	}
	return final, nil
}

func normalizeCompactMarkdown(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```markdown")
	text = strings.TrimPrefix(text, "```md")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToUpper(text), "CONTEXT CHECKPOINT") {
		text = "CONTEXT CHECKPOINT\n\n" + text
	}
	return strings.TrimSpace(text)
}

func renderCompactionEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	text := eventTextForCompaction(event)
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return renderCompactionBlock("User Message", compactText(text, 4000))
	case session.EventTypeAssistant:
		return renderCompactionBlock("Assistant Message", compactText(text, 5000))
	case session.EventTypePlan:
		return renderPlanEventForCompaction(event, text)
	case session.EventTypeToolCall:
		if update := session.ProtocolUpdateOf(event); update != nil {
			return renderToolEventForCompaction("Tool Call", event, update, update.RawInput, 2000)
		}
	case session.EventTypeToolResult:
		if update := session.ProtocolUpdateOf(event); update != nil {
			return renderToolEventForCompaction("Tool Result", event, update, update.RawOutput, 3500)
		}
		return renderCompactionBlock("Tool Result", compactText(text, 3500))
	case session.EventTypeParticipant:
		if event.Meta != nil {
			return renderCompactionBlock("Participant Update", compactText(renderCompactionValue(event.Meta, 1600), 1800))
		}
	}
	return renderCompactionBlock("Event", compactText(text, 1800))
}

func renderCompactionBlock(title string, body string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Event"
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "## " + title
	}
	return "## " + title + "\n" + body
}

func renderToolEventForCompaction(kind string, event *session.Event, update *session.ProtocolUpdate, payload map[string]any, limit int) string {
	toolName := toolNameForCompaction(event, update)
	lines := []string{}
	if toolName != "" {
		lines = append(lines, "- tool: "+toolName)
	}
	if update != nil {
		if title := strings.TrimSpace(update.Title); title != "" && !strings.EqualFold(title, toolName) {
			lines = append(lines, "- title: "+title)
		}
		if status := strings.TrimSpace(update.Status); status != "" {
			lines = append(lines, "- status: "+status)
		}
		if text := textFromProtocolContent(update.Content); text != "" {
			lines = append(lines, "- content: "+compactText(text, 1200))
		}
	}
	if len(payload) > 0 {
		if rendered := renderCompactionMap(payload, limit); rendered != "" {
			lines = append(lines, "", rendered)
		}
	} else if text := eventTextForCompaction(event); text != "" {
		lines = append(lines, "", compactText(text, limit))
	}
	return renderCompactionBlock(kind, strings.Join(lines, "\n"))
}

func renderCompactionValue(value any, limit int) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return compactText(typed, limit)
	case map[string]any:
		return renderCompactionMap(typed, limit)
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text := renderCompactionValue(item, max(limit/2, 200))
			if text == "" {
				continue
			}
			items = append(items, "- "+strings.ReplaceAll(text, "\n", "\n  "))
		}
		return compactText(strings.Join(items, "\n"), limit)
	default:
		return compactText(stringifyAny(value), limit)
	}
}

func renderCompactionMap(values map[string]any, limit int) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		text := renderCompactionValue(values[key], max(limit/len(keys), 240))
		if text == "" {
			continue
		}
		if strings.Contains(text, "\n") {
			lines = append(lines, key+":\n  "+strings.ReplaceAll(text, "\n", "\n  "))
		} else {
			lines = append(lines, key+": "+text)
		}
	}
	return compactText(strings.Join(lines, "\n"), limit)
}

func textFromProtocolContent(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return strings.TrimSpace(text)
		}
		if content, ok := typed["content"].(string); ok {
			return strings.TrimSpace(content)
		}
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := textFromProtocolContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case []session.ProtocolToolCallContent:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := textFromProtocolContent(item.Content); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

func toolNameForCompaction(event *session.Event, update *session.ProtocolUpdate) string {
	if event != nil {
		if runtimeMeta := nestedMap(event.Meta, "caelis", "runtime", "tool"); len(runtimeMeta) > 0 {
			if name := strings.TrimSpace(stringifyAny(runtimeMeta["name"])); name != "" {
				return name
			}
		}
		if event.Protocol != nil && event.Protocol.ToolCall != nil {
			if name := strings.TrimSpace(event.Protocol.ToolCall.Name); name != "" {
				return name
			}
		}
	}
	if update != nil {
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
		if kind := strings.TrimSpace(update.Kind); kind != "" {
			return kind
		}
	}
	return "tool"
}

func renderPlanEventForCompaction(event *session.Event, fallback string) string {
	lines := make([]string, 0, 8)
	if text := strings.TrimSpace(fallback); text != "" {
		lines = append(lines, compactText(text, 1000))
	}
	for _, entry := range planEntriesForCompaction(event) {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		status := strings.TrimSpace(entry.Status)
		if status == "" {
			status = "unknown"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", status, content))
	}
	if len(lines) == 0 {
		return renderCompactionBlock("Plan Update", "")
	}
	return renderCompactionBlock("Plan Update", strings.Join(lines, "\n"))
}

func planEntriesForCompaction(event *session.Event) []session.ProtocolPlanEntry {
	if event == nil || event.Protocol == nil {
		return nil
	}
	if event.Protocol.Plan != nil && len(event.Protocol.Plan.Entries) > 0 {
		return event.Protocol.Plan.Entries
	}
	if event.Protocol.Update != nil && len(event.Protocol.Update.Entries) > 0 {
		return event.Protocol.Update.Entries
	}
	return nil
}

func buildCompactEvent(activeSession session.Session, compactText string, data compact.CompactEventData) *session.Event {
	message := model.NewTextMessage(model.RoleUser, normalizeCompactMarkdown(compactText))
	scope := defaultScope(activeSession, "")
	return &session.Event{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Scope:      &scope,
		Message:    &message,
		Text:       message.TextContent(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodContextCheckpoint,
			Update: &session.ProtocolUpdate{
				SessionUpdate: "compact",
				Content:       session.ProtocolTextContent(message.TextContent()),
			},
		},
		Meta: map[string]any{
			compact.MetaKeyCompact: compact.CompactEventDataValue(data),
		},
	}
}

func compactTextFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(session.EventText(event))
}

func compactableEvents(events []*session.Event) []*session.Event {
	visible := compact.PromptEventsFromLatestCompact(events)
	if len(visible) == 0 {
		return nil
	}
	out := make([]*session.Event, 0, len(visible))
	for _, event := range visible {
		if event == nil || compact.IsCompactEvent(event) || event.Visibility == session.VisibilityOverlay {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func compactableEventCount(events []*session.Event) int {
	return len(compactableEvents(events))
}

func eventTextForCompaction(event *session.Event) string {
	if event == nil {
		return ""
	}
	if text := strings.TrimSpace(session.EventText(event)); text != "" {
		return text
	}
	return ""
}

func pendingEventsForCompaction(event *session.Event) []*session.Event {
	if event == nil || !session.IsMainInvocationVisibleEvent(event) {
		return nil
	}
	return []*session.Event{session.CloneEvent(event)}
}

func promptEventsWithPending(promptEvents []*session.Event, pendingEvents []*session.Event) []*session.Event {
	if len(pendingEvents) == 0 {
		return promptEvents
	}
	out := make([]*session.Event, 0, len(promptEvents)+len(pendingEvents))
	out = append(out, promptEvents...)
	for _, event := range pendingEvents {
		if event == nil || !session.IsMainInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func mainInvocationEvents(events []*session.Event) []*session.Event {
	if len(events) == 0 {
		return events
	}
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if !session.IsMainInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func (r *Runtime) compactAfterOverflow(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	req agent.RunRequest,
	cause error,
) (bool, error) {
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return false, err
	}
	result, err := r.compactor.CompactOnOverflow(ctx, compact.Request{
		Session:    activeSession,
		SessionRef: ref,
		Events:     events,
		Model:      req.AgentSpec.Model,
	}, cause)
	if err != nil {
		return false, err
	}
	if !result.Compacted || result.CompactEvent == nil {
		return false, nil
	}
	_, err = r.persistCompactionArtifacts(ctx, activeSession, ref, result)
	if err != nil {
		return false, err
	}
	return true, nil
}

func splitEventsByTokenBudget(events []*session.Event, budget int) [][]*session.Event {
	if budget <= 0 {
		budget = 24000
	}
	chunks := make([][]*session.Event, 0, 4)
	current := make([]*session.Event, 0, 8)
	currentTokens := 0
	for _, ev := range events {
		if ev == nil {
			continue
		}
		tokens := estimatePromptEventTokens(ev)
		if len(current) > 0 && currentTokens+tokens > budget {
			chunks = append(chunks, current)
			current = make([]*session.Event, 0, 8)
			currentTokens = 0
		}
		current = append(current, session.CloneEvent(ev))
		currentTokens += tokens
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

type providerTokenSnapshot struct {
	BaselineTokens  int
	DeltaStartIndex int
	EventID         string
}

func latestProviderTokenSnapshot(events []*session.Event) (providerTokenSnapshot, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event == nil || event.Meta == nil {
			continue
		}
		baseline, includeSnapshotGroup, ok := providerPromptBaselineTokens(event.Meta)
		if !ok || baseline <= 0 {
			continue
		}
		start := providerSnapshotGroupStart(events, i)
		deltaStart := start
		if !includeSnapshotGroup {
			deltaStart = i + 1
		}
		if id := strings.TrimSpace(events[start].ID); id != "" {
			return providerTokenSnapshot{
				BaselineTokens:  baseline,
				DeltaStartIndex: deltaStart,
				EventID:         id,
			}, true
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			return providerTokenSnapshot{
				BaselineTokens:  baseline,
				DeltaStartIndex: deltaStart,
				EventID:         id,
			}, true
		}
	}
	return providerTokenSnapshot{}, false
}

func providerPromptBaselineTokens(meta map[string]any) (int, bool, bool) {
	if len(meta) == 0 {
		return 0, false, false
	}
	if usage := nestedMap(meta, "caelis", "sdk", "usage"); len(usage) > 0 {
		if value, ok := intFromAny(usage["prompt_tokens"]); ok && value > 0 {
			return value, true, true
		}
		total, totalOK := intFromAny(usage["total_tokens"])
		completion, completionOK := intFromAny(usage["completion_tokens"])
		if totalOK && completionOK && total > 0 {
			return max(total-completion, 0), true, true
		}
		if totalOK && total > 0 {
			return total, false, true
		}
	}
	if value, ok := intFromAny(meta["prompt_tokens"]); ok && value > 0 {
		return value, true, true
	}
	total, totalOK := intFromAny(meta["total_tokens"])
	completion, completionOK := intFromAny(meta["completion_tokens"])
	if totalOK && completionOK && total > 0 {
		return max(total-completion, 0), true, true
	}
	if totalOK && total > 0 {
		return total, false, true
	}
	return 0, false, false
}

func providerSnapshotGroupStart(events []*session.Event, end int) int {
	if end < 0 || end >= len(events) {
		return end
	}
	target := providerSnapshotSignature(events[end])
	if target == "" {
		return end
	}
	start := end
	for start > 0 {
		prev := events[start-1]
		if prev == nil || providerSnapshotSignature(prev) != target {
			break
		}
		start--
	}
	return start
}

func providerSnapshotSignature(event *session.Event) string {
	if event == nil || len(event.Meta) == 0 {
		return ""
	}
	prompt, _ := intFromAny(event.Meta["prompt_tokens"])
	completion, _ := intFromAny(event.Meta["completion_tokens"])
	total, _ := intFromAny(event.Meta["total_tokens"])
	provider := strings.TrimSpace(stringifyAny(event.Meta["provider"]))
	model := strings.TrimSpace(stringifyAny(event.Meta["model"]))
	if sdkMeta := nestedMap(event.Meta, "caelis", "sdk"); len(sdkMeta) > 0 {
		provider = firstNonEmpty(provider, strings.TrimSpace(stringifyAny(sdkMeta["provider"])))
		model = firstNonEmpty(model, strings.TrimSpace(stringifyAny(sdkMeta["model"])))
		if usage := nestedMap(event.Meta, "caelis", "sdk", "usage"); len(usage) > 0 {
			if value, ok := intFromAny(usage["prompt_tokens"]); ok {
				prompt = value
			}
			if value, ok := intFromAny(usage["completion_tokens"]); ok {
				completion = value
			}
			if value, ok := intFromAny(usage["total_tokens"]); ok {
				total = value
			}
		}
	}
	if prompt <= 0 && completion <= 0 && total <= 0 && provider == "" && model == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s|%d|%d|%d", provider, model, prompt, completion, total)
}

func nestedMap(values map[string]any, path ...string) map[string]any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	out, _ := current.(map[string]any)
	return out
}

func estimateTokensFromIndex(events []*session.Event, index int) int {
	if index <= 0 {
		return estimatePromptEventsTokens(events)
	}
	total := 0
	for _, event := range events[index:] {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventsTokens(events []*session.Event) int {
	total := 0
	for _, event := range events {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventTokens(event *session.Event) int {
	if event == nil {
		return 0
	}
	if event.Message != nil {
		return estimateMessageTokens(*event.Message)
	}
	if text := strings.TrimSpace(session.EventText(event)); text != "" {
		return estimateTextTokens(text)
	}
	return 0
}

func estimateMessageTokens(message model.Message) int {
	total := 0
	if text := strings.TrimSpace(message.TextContent()); text != "" {
		total += estimateTextTokens(text)
	}
	for _, call := range message.ToolCalls() {
		total += estimateTextTokens(call.Name) + estimateTextTokens(call.Args)
	}
	for _, result := range message.ToolResults() {
		total += estimateTextTokens(result.Name)
		response := model.Message{
			Role: model.RoleTool,
			Parts: []model.Part{{
				Kind:       model.PartKindToolResult,
				ToolResult: &result,
			}},
		}.ToolResponse()
		if response != nil {
			payload := stringifyAny(response.Result)
			estimated := estimateTextTokens(payload)
			total += max(estimated+32, int(float64(estimated)*1.25))
		}
	}
	return max(total, 1)
}

func resolveContextWindowTokens(llm model.LLM, fallback int) int {
	if provider, ok := llm.(compact.ContextWindowProvider); ok {
		if tokens := provider.ContextWindowTokens(); tokens > 0 {
			return tokens
		}
	}
	return fallback
}

func resolveReserveOutputTokens(window int, configured int) int {
	if configured <= 0 {
		configured = 5000
	}
	if window <= 0 {
		return configured
	}
	maxReserve := max(window/4, 256)
	if configured > maxReserve {
		return maxReserve
	}
	return configured
}

func resolveSafetyMarginTokens(window int, configured int) int {
	if configured <= 0 {
		configured = 2048
	}
	if window <= 0 {
		return configured
	}
	maxSafety := max(window/8, 256)
	if configured > maxSafety {
		return maxSafety
	}
	return configured
}

func resolveEffectiveInputBudget(window, reserve, safety int) int {
	if window <= 0 {
		return 1
	}
	effective := window - reserve - safety
	if effective <= 0 {
		effective = window - reserve
	}
	if effective <= 0 {
		effective = window / 2
	}
	return max(min(effective, window), 1)
}

func dynamicWatermarks(window int, configuredSoft, configuredForce float64) (float64, float64) {
	if configuredSoft > 0 && configuredForce > 0 {
		if configuredForce < configuredSoft {
			configuredForce = configuredSoft
		}
		return configuredSoft, configuredForce
	}
	switch {
	case window >= 200000:
		return 0.95, 0.985
	case window >= 128000:
		return 0.93, 0.975
	case window >= 64000:
		return 0.90, 0.96
	case window >= 32000:
		return 0.85, 0.93
	default:
		return 0.78, 0.88
	}
}

func lastEventID(events []*session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if id := strings.TrimSpace(events[i].ID); id != "" {
			return id
		}
	}
	return ""
}

func compactText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 12 {
		return text[:limit]
	}
	head := limit / 2
	tail := limit - head - 3
	if tail < 0 {
		tail = 0
	}
	return strings.TrimSpace(text[:head]) + "..." + strings.TrimSpace(text[len(text)-tail:])
}

func stringifyAny(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 4
	if len([]rune(text))%4 != 0 {
		tokens++
	}
	return max(tokens, 1)
}

func intFromAny(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		v, err := typed.Int64()
		if err == nil {
			return int(v), true
		}
	}
	return 0, false
}

func isCompactionOverflowError(err error) bool {
	if err == nil {
		return false
	}
	if model.IsContextOverflow(err) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, keyword := range []string{
		"context length",
		"context window",
		"prompt is too long",
		"too many tokens",
		"maximum context",
		"input is too long",
		"token limit",
		"max context",
		"context overflow",
	} {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}
