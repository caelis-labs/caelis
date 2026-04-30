package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdkcompact "github.com/OnslaughtSnail/caelis/sdk/compact"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

// CompactionConfig controls codex-style replacement-history compaction.
type CompactionConfig struct {
	Enabled                    bool
	WatermarkRatio             float64
	ForceWatermarkRatio        float64
	DefaultContextWindowTokens int
	ReserveOutputTokens        int
	SafetyMarginTokens         int
	RetainedUserTokenLimit     int
	SegmentTokenBudget         int
	MaxSegmentDepth            int
	MaxRetryAttempts           int
	RetryBaseDelay             time.Duration
	RetryMaxDelay              time.Duration
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
	if cfg.RetainedUserTokenLimit <= 0 {
		cfg.RetainedUserTokenLimit = 20000
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
	return cfg
}

type codexStyleCompactor struct {
	cfg CompactionConfig
}

func newCodexStyleCompactor(cfg CompactionConfig) sdkcompact.Engine {
	return &codexStyleCompactor{cfg: normalizeCompactionConfig(cfg)}
}

func (c *codexStyleCompactor) Prepare(ctx context.Context, req sdkcompact.Request) (sdkcompact.Result, error) {
	promptEvents := sdkcompact.PromptEventsFromLatestCompact(req.Events)
	usagePromptEvents := promptEventsWithPending(promptEvents, req.PendingEvents)
	result := sdkcompact.Result{
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

func (c *codexStyleCompactor) Force(ctx context.Context, req sdkcompact.Request, trigger string) (sdkcompact.Result, error) {
	promptEvents := sdkcompact.PromptEventsFromLatestCompact(req.Events)
	result := sdkcompact.Result{
		PromptEvents: promptEvents,
		Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
	}
	if compactableEventCount(req.Events) == 0 {
		return result, nil
	}
	if req.Model == nil {
		return sdkcompact.Result{}, errors.New("sdk/runtime/local: compact model is required")
	}
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		trigger = "manual"
	}
	return c.compact(ctx, req, trigger)
}

func (c *codexStyleCompactor) CompactOnOverflow(ctx context.Context, req sdkcompact.Request, cause error) (sdkcompact.Result, error) {
	if !c.cfg.Enabled || req.Model == nil {
		promptEvents := sdkcompact.PromptEventsFromLatestCompact(req.Events)
		return sdkcompact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, cause
	}
	if !isCompactionOverflowError(cause) {
		return sdkcompact.Result{}, cause
	}
	return c.compact(ctx, req, "overflow_recovery")
}

func (c *codexStyleCompactor) decide(_ context.Context, usage sdkcompact.UsageSnapshot, req sdkcompact.Request) (sdkcompact.TriggerDecision, error) {
	if usage.EffectiveInputBudget <= 0 || req.Model == nil {
		return sdkcompact.TriggerDecision{}, nil
	}
	if compactableEventCount(req.Events) == 0 {
		return sdkcompact.TriggerDecision{}, nil
	}
	softRatio, forceRatio := dynamicWatermarks(usage.ContextWindowTokens, c.cfg.WatermarkRatio, c.cfg.ForceWatermarkRatio)
	ratio := float64(usage.TotalTokens) / float64(usage.EffectiveInputBudget)
	switch {
	case ratio >= forceRatio:
		return sdkcompact.TriggerDecision{ShouldCompact: true, Reason: "context_limit"}, nil
	case ratio >= softRatio:
		return sdkcompact.TriggerDecision{ShouldCompact: true, Reason: "context_watermark"}, nil
	default:
		return sdkcompact.TriggerDecision{}, nil
	}
}

func (c *codexStyleCompactor) compact(ctx context.Context, req sdkcompact.Request, trigger string) (sdkcompact.Result, error) {
	baseEvent, baseData, _ := sdkcompact.LatestCompactEvent(req.Events)
	baseText := compactTextFromEvent(baseEvent)
	delta := compactableEvents(req.Events)
	if len(delta) == 0 {
		promptEvents := sdkcompact.PromptEventsFromLatestCompact(req.Events)
		return sdkcompact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, nil
	}
	retainedUsers, retainedUserIndexes := selectRetainedUserInputs(delta, c.cfg.RetainedUserTokenLimit)
	summaryEvents := dropSelectedEvents(delta, retainedUserIndexes)
	if len(summaryEvents) == 0 {
		promptEvents := sdkcompact.PromptEventsFromLatestCompact(req.Events)
		return sdkcompact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, nil
	}

	compactText, err := c.generateCompactMarkdown(ctx, req.Model, baseText, summaryEvents, preferredCompactionAnchors(baseText, delta))
	if err != nil {
		return sdkcompact.Result{}, err
	}
	retainedUsers, replacementHistory, _ := c.fitReplacementHistoryToBudget(req, compactText, retainedUsers)
	data := sdkcompact.CompactEventData{
		Revision:            baseData.Revision + 1,
		SummarizedThroughID: lastEventID(delta),
		Generator:           "model_markdown",
		Trigger:             strings.TrimSpace(trigger),
		RetainedUserInputs:  retainedUsers,
		ReplacementHistory:  replacementHistory,
	}
	compactEvent := buildCompactEvent(req.Session, compactText, data)
	promptEvents := sdkcompact.PromptEventsFromLatestCompact([]*sdksession.Event{compactEvent})
	usage := c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents))
	data.TotalTokens = usage.TotalTokens
	data.ContextWindowTokens = usage.ContextWindowTokens
	if compactEvent.Meta == nil {
		compactEvent.Meta = map[string]any{}
	}
	compactEvent.Meta[sdkcompact.MetaKeyCompact] = sdkcompact.CompactEventDataValue(data)
	return sdkcompact.Result{
		Compacted:    true,
		CompactText:  compactText,
		CompactEvent: compactEvent,
		PromptEvents: promptEvents,
		Usage:        usage,
	}, nil
}

func (c *codexStyleCompactor) snapshotUsage(req sdkcompact.Request, promptEvents []*sdksession.Event) sdkcompact.UsageSnapshot {
	window := resolveContextWindowTokens(req.Model, c.cfg.DefaultContextWindowTokens)
	return snapshotUsageWithResolvedWindow(promptEvents, window, c.cfg)
}

// ComputeUsageSnapshot applies the same provider-aware usage snapshot logic
// used by compaction, but without mutating session history.
func ComputeUsageSnapshot(events []*sdksession.Event, pendingEvents []*sdksession.Event, contextWindow int, cfg CompactionConfig) sdkcompact.UsageSnapshot {
	promptEvents := sdkcompact.PromptEventsFromLatestCompact(events)
	return snapshotUsageWithResolvedWindow(promptEventsWithPending(promptEvents, pendingEvents), contextWindow, cfg)
}

func snapshotUsageWithResolvedWindow(promptEvents []*sdksession.Event, window int, cfg CompactionConfig) sdkcompact.UsageSnapshot {
	cfg = normalizeCompactionConfig(cfg)
	if window <= 0 {
		window = cfg.DefaultContextWindowTokens
	}
	reserve := resolveReserveOutputTokens(window, cfg.ReserveOutputTokens)
	safety := resolveSafetyMarginTokens(window, cfg.SafetyMarginTokens)
	effective := resolveEffectiveInputBudget(window, reserve, safety)

	total := 0
	delta := 0
	asOfEventID := ""
	source := sdkcompact.UsageSourceEstimated
	if snapshot, ok := latestProviderTokenSnapshot(promptEvents); ok {
		total = snapshot.BaselineTokens
		delta = estimateTokensFromIndex(promptEvents, snapshot.DeltaStartIndex)
		total += delta
		asOfEventID = snapshot.EventID
		source = sdkcompact.UsageSourceProvider
	} else {
		total = estimatePromptEventsTokens(promptEvents)
	}
	return sdkcompact.UsageSnapshot{
		TotalTokens:          total,
		ContextWindowTokens:  window,
		EffectiveInputBudget: effective,
		EstimatedDeltaTokens: delta,
		Source:               source,
		AsOfEventID:          asOfEventID,
	}
}

func (c *codexStyleCompactor) generateCompactMarkdown(
	ctx context.Context,
	model sdkmodel.LLM,
	baseText string,
	events []*sdksession.Event,
	anchors compactionAnchors,
) (string, error) {
	if len(events) == 0 {
		return forceContinuityLead(normalizeCompactMarkdown(baseText), anchors), nil
	}
	text, err := c.generateCompactMarkdownOnce(ctx, model, baseText, events, anchors)
	if err == nil {
		return text, nil
	}
	if isCompactionOverflowError(err) {
		return c.generateCompactMarkdownSegmented(ctx, model, baseText, events, anchors, 0)
	}
	return "", err
}

func (c *codexStyleCompactor) generateCompactMarkdownSegmented(
	ctx context.Context,
	model sdkmodel.LLM,
	baseText string,
	events []*sdksession.Event,
	anchors compactionAnchors,
	depth int,
) (string, error) {
	if len(events) == 0 {
		return forceContinuityLead(normalizeCompactMarkdown(baseText), anchors), nil
	}
	if depth >= c.cfg.MaxSegmentDepth || len(events) <= 1 {
		return "", &sdkmodel.ContextOverflowError{Cause: errors.New("compact segment still exceeds context budget")}
	}
	segments := splitEventsByTokenBudget(events, c.cfg.SegmentTokenBudget)
	if len(segments) <= 1 {
		mid := len(events) / 2
		if mid <= 0 || mid >= len(events) {
			return "", &sdkmodel.ContextOverflowError{Cause: errors.New("unable to split compaction segment further")}
		}
		segments = [][]*sdksession.Event{events[:mid], events[mid:]}
	}
	current := baseText
	for _, segment := range segments {
		if len(segment) == 0 {
			continue
		}
		update, err := c.generateCompactMarkdownOnce(ctx, model, current, segment, anchors)
		if err != nil {
			if isCompactionOverflowError(err) {
				update, err = c.generateCompactMarkdownSegmented(ctx, model, current, segment, anchors, depth+1)
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
	model sdkmodel.LLM,
	baseText string,
	events []*sdksession.Event,
	anchors compactionAnchors,
) (string, error) {
	var lastErr error
	for attempt := 0; attempt < c.cfg.MaxRetryAttempts; attempt++ {
		if attempt > 0 {
			delay := retryDelayForAttemptWithBounds(attempt-1, c.cfg.RetryBaseDelay, c.cfg.RetryMaxDelay)
			if err := sleepContext(ctx, delay); err != nil {
				return "", err
			}
		}
		text, err := modelCompactMarkdown(ctx, model, baseText, events, anchors)
		if err == nil {
			return text, nil
		}
		if isCompactionOverflowError(err) {
			return "", err
		}
		lastErr = err
		if !shouldRetry(err) {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("compact generation failed")
	}
	return "", lastErr
}

func (r *Runtime) prepareInvocationContext(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	req sdkruntime.RunRequest,
	pendingInput *sdksession.Event,
) ([]*sdksession.Event, map[string]any, error) {
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return nil, nil, err
	}
	events, err := r.sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref})
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
	result, err := r.compactor.Prepare(ctx, sdkcompact.Request{
		Session:       session,
		SessionRef:    ref,
		Events:        events,
		PendingEvents: pendingEventsForCompaction(pendingInput),
		Model:         req.AgentSpec.Model,
	})
	if err != nil {
		return nil, nil, err
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, session, ref, result)
		if appendErr != nil {
			return nil, nil, appendErr
		}
		return sdkcompact.PromptEventsFromLatestCompact(append(events, persisted)), state, nil
	}
	return result.PromptEvents, state, nil
}

type CompactRequest struct {
	SessionRef sdksession.SessionRef
	Model      sdkmodel.LLM
	Trigger    string
}

type CompactResult struct {
	Session   sdksession.Session
	Compacted bool
	Event     *sdksession.Event
	Usage     sdkcompact.UsageSnapshot
}

func (r *Runtime) Compact(ctx context.Context, req CompactRequest) (CompactResult, error) {
	if r == nil {
		return CompactResult{}, errors.New("sdk/runtime/local: runtime is unavailable")
	}
	ref := sdksession.NormalizeSessionRef(req.SessionRef)
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return CompactResult{}, err
	}
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return CompactResult{}, err
	}
	events, err := r.sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref})
	if err != nil {
		return CompactResult{}, err
	}
	forceCompactor, ok := r.compactor.(sdkcompact.ForceEngine)
	if !ok {
		return CompactResult{}, errors.New("sdk/runtime/local: compactor does not support forced compaction")
	}
	result, err := forceCompactor.Force(ctx, sdkcompact.Request{
		Session:    session,
		SessionRef: ref,
		Events:     events,
		Model:      req.Model,
	}, req.Trigger)
	if err != nil {
		return CompactResult{}, err
	}
	out := CompactResult{
		Session:   session,
		Compacted: result.Compacted,
		Usage:     result.Usage,
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, session, ref, result)
		if appendErr != nil {
			return CompactResult{}, appendErr
		}
		out.Event = persisted
	}
	return out, nil
}

func (r *Runtime) updateCompactionUsageFromBatch(_ context.Context, _ sdksession.SessionRef, _ []*sdksession.Event) error {
	return nil
}

func (r *Runtime) persistCompactionArtifacts(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	result sdkcompact.Result,
) (*sdksession.Event, error) {
	if result.CompactEvent == nil {
		return nil, errors.New("sdk/runtime/local: compact event is required")
	}
	persisted, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: ref,
		Event:      normalizeEvent(session, "", result.CompactEvent),
	})
	if err != nil {
		return nil, err
	}
	return persisted, nil
}

func modelCompactMarkdown(
	ctx context.Context,
	model sdkmodel.LLM,
	baseText string,
	events []*sdksession.Event,
	anchors compactionAnchors,
) (string, error) {
	input := renderCheckpointCompactionInput(baseText, events)
	if strings.TrimSpace(input) == "" {
		return "", errors.New("empty compaction input")
	}
	anchors = mergeCompactionAnchors(extractCompactionAnchorsFromEvents(events), anchors)
	request := &sdkmodel.Request{
		Instructions: []sdkmodel.Part{sdkmodel.NewTextPart(strings.TrimSpace(`
You are performing a CONTEXT CHECKPOINT COMPACTION for a coding agent.
Return only a plain-text handoff note. Do not return JSON. Do not use code fences.

Rules:
- Start with these three lines when the source provides them:
  Objective: ...
  Blocker: ...
  Next action: ...
- Preserve the current objective, blocker, next action, and execution progress with very high fidelity.
- If newer history explicitly states an objective, blocker, or next action, use the newer wording instead of the older checkpoint wording.
- Treat the existing compact checkpoint as a reference, not as text that must be kept verbatim.
- Keep durable direction, blockers, and execution progress. Drop stale, repetitive, or superseded details.
- Ignore acknowledgment-only turns such as "ack", "ok", or "done" unless they carry real progress.
- Ignore reply-format scaffolding such as "reply exactly" or "answer with exactly" when extracting durable state.
- After the first lines, continue with a concise plain-text handoff note covering only the details needed to continue the task.
- Prefer short bullets or short paragraphs. Keep task, participant, and blocker summaries short but concrete.
`))},
		Messages: []sdkmodel.Message{
			sdkmodel.NewTextMessage(sdkmodel.RoleUser, input),
		},
		Stream: false,
	}
	final, err := collectCompactionResponse(ctx, model, request)
	if err != nil {
		return "", err
	}
	text := normalizeCompactMarkdown(strings.TrimSpace(final.Message.TextContent()))
	text = forceContinuityLead(text, anchors)
	if compactMarkdownLooksEmpty(text) || compactMarkdownMissingRequiredAnchors(text, anchors) {
		salvaged, salvageErr := salvageCompactMarkdown(ctx, model, input, text, anchors)
		if salvageErr == nil {
			salvaged = forceContinuityLead(salvaged, anchors)
		}
		if salvageErr == nil && !compactMarkdownLooksEmpty(salvaged) {
			return salvaged, nil
		}
		return "", fmt.Errorf("sdk/runtime/local: insufficient compact checkpoint payload: %s", compactText(text, 320))
	}
	return text, nil
}

func preferredCompactionAnchors(baseText string, events []*sdksession.Event) compactionAnchors {
	return mergeCompactionAnchors(extractCompactionAnchorsFromText(baseText), extractCompactionAnchorsFromEvents(events))
}

func mergeCompactionAnchors(base compactionAnchors, override compactionAnchors) compactionAnchors {
	if strings.TrimSpace(override.Objective) != "" {
		base.Objective = strings.TrimSpace(override.Objective)
	}
	if strings.TrimSpace(override.Blocker) != "" {
		base.Blocker = strings.TrimSpace(override.Blocker)
	}
	if strings.TrimSpace(override.NextAction) != "" {
		base.NextAction = strings.TrimSpace(override.NextAction)
	}
	base.Objective = strings.TrimSpace(base.Objective)
	base.Blocker = strings.TrimSpace(base.Blocker)
	base.NextAction = strings.TrimSpace(base.NextAction)
	return base
}

func compactMarkdownLooksEmpty(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	return len(text) < 24
}

func salvageCompactMarkdown(ctx context.Context, model sdkmodel.LLM, input string, prior string, anchors compactionAnchors) (string, error) {
	anchorBlock := renderCompactionAnchors(anchors)
	request := &sdkmodel.Request{
		Instructions: []sdkmodel.Part{sdkmodel.NewTextPart(strings.TrimSpace(`
You are repairing an empty or low-information context checkpoint for a coding agent.
Return only a plain-text handoff note starting with:
Objective: ...
Blocker: ...
Next action: ...

Rules:
- Preserve exact wording for the current objective, blockers, and next actions when available.
- Do not leave Objective or Next action empty if the source contains them.
- Ignore acknowledgment-only turns and reply-format scaffolding.
- After the first lines, add only the minimum extra detail needed to continue the task.
`))},
		Messages: []sdkmodel.Message{
			sdkmodel.NewTextMessage(sdkmodel.RoleUser, strings.TrimSpace(input+"\n\nRequired anchors to preserve:\n"+anchorBlock+"\n\nPrevious invalid compact output:\n"+prior)),
		},
		Stream: false,
	}
	final, err := collectCompactionResponse(ctx, model, request)
	if err != nil {
		return "", err
	}
	return normalizeCompactMarkdown(strings.TrimSpace(final.Message.TextContent())), nil
}

type compactionAnchors struct {
	Objective  string
	Blocker    string
	NextAction string
}

func extractCompactionAnchorsFromEvents(events []*sdksession.Event) compactionAnchors {
	anchors := compactionAnchors{}
	for _, preferUser := range []bool{true, false} {
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			if event == nil {
				continue
			}
			if preferUser && sdksession.EventTypeOf(event) != sdksession.EventTypeUser {
				continue
			}
			text := eventTextForCompaction(event)
			if text == "" {
				continue
			}
			if isSummaryHistoryMessage(text) {
				continue
			}
			if anchors.Objective == "" {
				anchors.Objective = extractLabeledClause(text, []string{"objective:", "session objective is:", "project objective:", "objective is:"})
			}
			if anchors.Blocker == "" {
				anchors.Blocker = extractLabeledClause(text, []string{"blocker:", "current blocker is:", "current blocker:", "blocker is:"})
			}
			if anchors.NextAction == "" {
				anchors.NextAction = extractLabeledClause(text, []string{"next action is:", "next action:"})
			}
			if anchors.Objective != "" && anchors.Blocker != "" && anchors.NextAction != "" {
				return anchors
			}
		}
	}
	return anchors
}

func isSummaryHistoryMessage(text string) bool {
	text = strings.TrimSpace(strings.ToUpper(text))
	return strings.HasPrefix(text, "CONTEXT CHECKPOINT")
}

func extractCompactionAnchorsFromText(text string) compactionAnchors {
	text = strings.TrimSpace(text)
	if text == "" {
		return compactionAnchors{}
	}
	return compactionAnchors{
		Objective:  extractLabeledClause(text, []string{"objective:", "session objective is:", "project objective:", "objective is:"}),
		Blocker:    extractLabeledClause(text, []string{"blocker:", "current blocker is:", "current blocker:", "blocker is:"}),
		NextAction: extractLabeledClause(text, []string{"next action:", "next action is:"}),
	}
}

func compactMarkdownMissingRequiredAnchors(text string, anchors compactionAnchors) bool {
	if strings.TrimSpace(text) == "" {
		return true
	}
	lower := strings.ToLower(text)
	if anchorMissing(lower, anchors.Objective) {
		return true
	}
	if anchorMissing(lower, anchors.Blocker) {
		return true
	}
	if anchorMissing(lower, anchors.NextAction) {
		return true
	}
	return false
}

func anchorMissing(compactLower, anchor string) bool {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return false
	}
	return !strings.Contains(compactLower, strings.ToLower(anchor))
}

func renderCompactionAnchors(anchors compactionAnchors) string {
	lines := []string{}
	if anchors.Objective != "" {
		lines = append(lines, "- Objective: "+anchors.Objective)
	}
	if anchors.Blocker != "" {
		lines = append(lines, "- Blocker: "+anchors.Blocker)
	}
	if anchors.NextAction != "" {
		lines = append(lines, "- Next action: "+anchors.NextAction)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func extractLabeledClause(input string, labels []string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	lower := strings.ToLower(input)
	start := -1
	labelLen := 0
	for _, label := range labels {
		if idx := strings.Index(lower, label); idx >= 0 && (start < 0 || idx < start) {
			start = idx
			labelLen = len(label)
		}
	}
	if start < 0 {
		return ""
	}
	bodyStart := start + labelLen
	bodyLower := lower[bodyStart:]
	candidates := []string{
		"session objective is:",
		"project objective:",
		"objective is:",
		"current blocker is:",
		"current blocker:",
		"blocker is:",
		"next action is:",
		"next action:",
	}
	end := len(input)
	for _, candidate := range candidates {
		if idx := strings.Index(bodyLower, candidate); idx >= 0 {
			next := bodyStart + idx
			if next > bodyStart && next < end {
				end = next
			}
		}
	}
	if idx := strings.IndexAny(input[bodyStart:end], "\n\r"); idx >= 0 {
		end = min(end, bodyStart+idx)
	}
	if idx := firstSentenceBoundary(input[bodyStart:end]); idx >= 0 {
		end = min(end, bodyStart+idx)
	}
	text := strings.TrimSpace(input[bodyStart:end])
	text = strings.Trim(text, " .;,-")
	return text
}

func firstSentenceBoundary(text string) int {
	for i := 0; i < len(text)-1; i++ {
		switch text[i] {
		case '.', '!', '?':
			if text[i+1] == ' ' || text[i+1] == '\n' || text[i+1] == '\r' {
				return i + 1
			}
		}
	}
	return -1
}

func forceContinuityLead(text string, anchors compactionAnchors) string {
	text = normalizeCompactMarkdown(text)
	if text == "" {
		return text
	}
	lines := []string{"CONTEXT CHECKPOINT"}
	if anchors.Objective != "" {
		lines = append(lines, "Objective: "+anchors.Objective)
	}
	if anchors.Blocker != "" {
		lines = append(lines, "Blocker: "+anchors.Blocker)
	}
	if anchors.NextAction != "" {
		lines = append(lines, "Next action: "+anchors.NextAction)
	}
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "CONTEXT CHECKPOINT"))
	if body != "" {
		lines = append(lines, "", body)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func renderCheckpointCompactionInput(
	baseText string,
	events []*sdksession.Event,
) string {
	var b strings.Builder
	if strings.TrimSpace(baseText) != "" {
		b.WriteString("Existing compact checkpoint (reference only):\n")
		b.WriteString(strings.TrimSpace(baseText))
		b.WriteString("\n\n")
	}
	b.WriteString("New history since the last compact checkpoint:\n")
	for _, event := range events {
		line := renderCompactionEvent(event)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func collectCompactionResponse(ctx context.Context, model sdkmodel.LLM, req *sdkmodel.Request) (*sdkmodel.Response, error) {
	var final *sdkmodel.Response
	for event, err := range model.Generate(ctx, req) {
		if err != nil {
			return nil, err
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if final == nil {
		return nil, errors.New("sdk/runtime/local: model returned no compaction response")
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

func renderCompactionEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	text := eventTextForCompaction(event)
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser:
		return "USER: " + compactText(text, 260)
	case sdksession.EventTypeAssistant:
		return "ASSISTANT: " + compactText(text, 260)
	case sdksession.EventTypePlan:
		return renderPlanEventForCompaction(event, text)
	case sdksession.EventTypeToolCall:
		if update := sdksession.ProtocolUpdateOf(event); update != nil {
			return fmt.Sprintf("TOOL_CALL %s: %s",
				toolNameForCompaction(event, update),
				compactText(stringifyAny(update.RawInput), 220),
			)
		}
	case sdksession.EventTypeToolResult:
		if update := sdksession.ProtocolUpdateOf(event); update != nil {
			return fmt.Sprintf("TOOL_RESULT %s: %s",
				toolNameForCompaction(event, update),
				compactText(stringifyAny(update.RawOutput), 260),
			)
		}
		return "TOOL_RESULT: " + compactText(text, 260)
	case sdksession.EventTypeParticipant:
		if event.Meta != nil {
			return "PARTICIPANT: " + compactText(stringifyAny(event.Meta), 220)
		}
	}
	return compactText(text, 220)
}

func toolNameForCompaction(event *sdksession.Event, update *sdksession.ProtocolUpdate) string {
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

func renderPlanEventForCompaction(event *sdksession.Event, fallback string) string {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(fallback); text != "" {
		parts = append(parts, compactText(text, 140))
	}
	if event != nil && event.Protocol != nil && event.Protocol.Plan != nil {
		rendered := make([]string, 0, len(event.Protocol.Plan.Entries))
		for _, entry := range event.Protocol.Plan.Entries {
			content := strings.TrimSpace(entry.Content)
			status := strings.TrimSpace(entry.Status)
			if content == "" {
				continue
			}
			if status != "" {
				rendered = append(rendered, fmt.Sprintf("%s [%s]", content, status))
			} else {
				rendered = append(rendered, content)
			}
			if len(rendered) >= 4 {
				break
			}
		}
		if len(rendered) > 0 {
			parts = append(parts, "entries: "+strings.Join(rendered, "; "))
		}
	}
	if len(parts) == 0 {
		return "PLAN"
	}
	return "PLAN: " + compactText(strings.Join(parts, " | "), 260)
}

func buildCompactEvent(session sdksession.Session, compactText string, data sdkcompact.CompactEventData) *sdksession.Event {
	message := sdkmodel.NewTextMessage(sdkmodel.RoleUser, normalizeCompactMarkdown(compactText))
	scope := defaultScope(session, "")
	return &sdksession.Event{
		Type:       sdksession.EventTypeCompact,
		Visibility: sdksession.VisibilityCanonical,
		Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindSystem, Name: "runtime"},
		Scope:      &scope,
		Message:    &message,
		Text:       message.TextContent(),
		Protocol: &sdksession.EventProtocol{
			Method: sdksession.ProtocolMethodContextCheckpoint,
			Update: &sdksession.ProtocolUpdate{
				SessionUpdate: "compact",
				Content:       sdksession.ProtocolTextContent(message.TextContent()),
			},
		},
		Meta: map[string]any{
			sdkcompact.MetaKeyCompact: sdkcompact.CompactEventDataValue(data),
		},
	}
}

func (c *codexStyleCompactor) fitReplacementHistoryToBudget(
	req sdkcompact.Request,
	compactText string,
	retainedUsers []string,
) ([]string, []*sdksession.Event, sdkcompact.UsageSnapshot) {
	type summaryLimits struct {
		maxChars int
		maxLines int
	}
	limits := []summaryLimits{
		{maxChars: 480, maxLines: 12},
		{maxChars: 240, maxLines: 6},
		{maxChars: 0, maxLines: 0},
	}
	bestRetained := append([]string(nil), retainedUsers...)
	bestHistory := buildReplacementHistoryWithLimits(compactText, bestRetained, limits[0].maxChars, limits[0].maxLines)
	bestUsage := c.snapshotUsage(req, promptEventsWithPending(bestHistory, req.PendingEvents))
	for _, limit := range limits {
		for drop := 0; drop <= len(retainedUsers); drop++ {
			candidateRetained := append([]string(nil), retainedUsers[drop:]...)
			history := buildReplacementHistoryWithLimits(compactText, candidateRetained, limit.maxChars, limit.maxLines)
			usage := c.snapshotUsage(req, promptEventsWithPending(history, req.PendingEvents))
			if bestUsage.TotalTokens <= 0 || usage.TotalTokens < bestUsage.TotalTokens {
				bestRetained = candidateRetained
				bestHistory = history
				bestUsage = usage
			}
			if usage.TotalTokens <= usage.EffectiveInputBudget {
				return candidateRetained, history, usage
			}
		}
	}
	return bestRetained, bestHistory, bestUsage
}

func compactTextFromEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(sdksession.EventText(event))
}

func buildReplacementHistoryWithLimits(compactText string, retainedUsers []string, maxChars int, maxLines int) []*sdksession.Event {
	out := make([]*sdksession.Event, 0, len(retainedUsers)+1)
	for _, text := range retainedUsers {
		msg := sdkmodel.NewTextMessage(sdkmodel.RoleUser, text)
		out = append(out, &sdksession.Event{
			Type:       sdksession.EventTypeUser,
			Visibility: sdksession.VisibilityOverlay,
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"},
			Message:    &msg,
			Text:       msg.TextContent(),
			Protocol: &sdksession.EventProtocol{
				Update: &sdksession.ProtocolUpdate{
					SessionUpdate: string(sdksession.ProtocolUpdateTypeUserMessage),
					Content:       sdksession.ProtocolTextContent(msg.TextContent()),
				},
			},
		})
	}
	if replacementSummary := replacementHistorySummaryTextWithLimits(compactText, maxChars, maxLines); replacementSummary != "" {
		msg := sdkmodel.NewTextMessage(sdkmodel.RoleUser, replacementSummary)
		out = append(out, &sdksession.Event{
			Type:       sdksession.EventTypeUser,
			Visibility: sdksession.VisibilityOverlay,
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"},
			Message:    &msg,
			Text:       msg.TextContent(),
		})
	}
	return out
}

func replacementHistorySummaryTextWithLimits(summaryText string, maxChars int, maxLines int) string {
	text := normalizeCompactMarkdown(summaryText)
	if text == "" {
		return ""
	}
	anchors := extractCompactionAnchorsFromText(text)
	lines := []string{"CONTEXT CHECKPOINT"}
	if anchors.Objective != "" {
		lines = append(lines, "Objective: "+anchors.Objective)
	}
	if anchors.Blocker != "" {
		lines = append(lines, "Blocker: "+anchors.Blocker)
	}
	if anchors.NextAction != "" {
		lines = append(lines, "Next action: "+anchors.NextAction)
	}
	bodyLines := []string{}
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.EqualFold(line, "CONTEXT CHECKPOINT") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "objective:") || strings.HasPrefix(lower, "blocker:") || strings.HasPrefix(lower, "next action:") {
			continue
		}
		bodyLines = append(bodyLines, line)
	}
	body := strings.TrimSpace(strings.Join(bodyLines, "\n"))
	if body != "" && maxChars > 0 && maxLines > 0 {
		lines = append(lines, "", trimReplacementSummaryBody(bodyLines, maxChars, maxLines))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func trimReplacementSummaryBody(lines []string, maxChars int, maxLines int) string {
	if maxChars <= 0 {
		maxChars = 480
	}
	if maxLines <= 0 {
		maxLines = 12
	}
	selected := make([]string, 0, min(len(lines), maxLines))
	used := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nextCost := len(line)
		if len(selected) > 0 {
			nextCost++
		}
		if len(selected) >= maxLines || used+nextCost > maxChars {
			if len(selected) == 0 {
				line = strings.TrimSpace(line)
				runes := []rune(line)
				if len(runes) > maxChars {
					line = strings.TrimSpace(string(runes[:maxChars]))
				}
				selected = append(selected, line)
			}
			selected = append(selected, "...")
			break
		}
		selected = append(selected, line)
		used += nextCost
	}
	return strings.TrimSpace(strings.Join(selected, "\n"))
}

func compactableEvents(events []*sdksession.Event) []*sdksession.Event {
	visible := sdkcompact.PromptEventsFromLatestCompact(events)
	if len(visible) == 0 {
		return nil
	}
	out := make([]*sdksession.Event, 0, len(visible))
	for _, event := range visible {
		if event == nil || sdkcompact.IsCompactEvent(event) || event.Visibility == sdksession.VisibilityOverlay {
			continue
		}
		out = append(out, sdksession.CloneEvent(event))
	}
	return out
}

func compactableEventCount(events []*sdksession.Event) int {
	return len(compactableEvents(events))
}

func selectRetainedUserInputs(events []*sdksession.Event, tokenBudget int) ([]string, map[int]struct{}) {
	if tokenBudget <= 0 {
		return nil, nil
	}
	type retainedUser struct {
		index int
		text  string
	}
	selected := make([]retainedUser, 0, 4)
	remaining := tokenBudget
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event == nil || sdksession.EventTypeOf(event) != sdksession.EventTypeUser {
			continue
		}
		text := eventTextForCompaction(event)
		if text == "" || isSummaryHistoryMessage(text) {
			continue
		}
		cost := estimateTextTokens(text)
		if cost <= remaining {
			selected = append(selected, retainedUser{
				index: i,
				text:  text,
			})
			remaining -= cost
			if remaining <= 0 {
				break
			}
			continue
		}
		truncated := truncateRetainedUserInput(text, remaining)
		if truncated == "" {
			break
		}
		selected = append(selected, retainedUser{
			index: i,
			text:  truncated,
		})
		break
	}
	if len(selected) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(selected))
	indexes := map[int]struct{}{}
	for i := len(selected) - 1; i >= 0; i-- {
		out = append(out, selected[i].text)
		indexes[selected[i].index] = struct{}{}
	}
	return out, indexes
}

func eventTextForCompaction(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	if text := strings.TrimSpace(sdksession.EventText(event)); text != "" {
		return text
	}
	return ""
}

func pendingEventsForCompaction(event *sdksession.Event) []*sdksession.Event {
	if event == nil || !sdksession.IsMainInvocationVisibleEvent(event) {
		return nil
	}
	return []*sdksession.Event{sdksession.CloneEvent(event)}
}

func promptEventsWithPending(promptEvents []*sdksession.Event, pendingEvents []*sdksession.Event) []*sdksession.Event {
	if len(pendingEvents) == 0 {
		return promptEvents
	}
	out := make([]*sdksession.Event, 0, len(promptEvents)+len(pendingEvents))
	out = append(out, promptEvents...)
	for _, event := range pendingEvents {
		if event == nil || !sdksession.IsMainInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func mainInvocationEvents(events []*sdksession.Event) []*sdksession.Event {
	if len(events) == 0 {
		return events
	}
	out := make([]*sdksession.Event, 0, len(events))
	for _, event := range events {
		if !sdksession.IsMainInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func truncateRetainedUserInput(text string, tokenBudget int) string {
	text = strings.TrimSpace(text)
	if text == "" || tokenBudget <= 0 {
		return ""
	}
	runes := []rune(text)
	limit := tokenBudget * 4
	if len(runes) <= limit {
		return text
	}
	if limit <= 0 {
		return ""
	}
	if limit <= 12 {
		return strings.TrimSpace(string(runes[:limit]))
	}
	head := limit / 2
	tail := limit - head - 3
	if tail < 0 {
		tail = 0
	}
	return strings.TrimSpace(string(runes[:head])) + "..." + strings.TrimSpace(string(runes[len(runes)-tail:]))
}

func (r *Runtime) compactAfterOverflow(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	req sdkruntime.RunRequest,
	cause error,
) (bool, error) {
	events, err := r.sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref})
	if err != nil {
		return false, err
	}
	result, err := r.compactor.CompactOnOverflow(ctx, sdkcompact.Request{
		Session:    session,
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
	_, err = r.persistCompactionArtifacts(ctx, session, ref, result)
	if err != nil {
		return false, err
	}
	return true, nil
}

func dropSelectedEvents(events []*sdksession.Event, selected map[int]struct{}) []*sdksession.Event {
	if len(selected) == 0 {
		return sdksession.CloneEvents(events)
	}
	out := make([]*sdksession.Event, 0, len(events))
	for i, event := range events {
		if event == nil {
			continue
		}
		if _, ok := selected[i]; ok {
			continue
		}
		out = append(out, sdksession.CloneEvent(event))
	}
	return out
}

func splitEventsByTokenBudget(events []*sdksession.Event, budget int) [][]*sdksession.Event {
	if budget <= 0 {
		budget = 24000
	}
	chunks := make([][]*sdksession.Event, 0, 4)
	current := make([]*sdksession.Event, 0, 8)
	currentTokens := 0
	for _, ev := range events {
		if ev == nil {
			continue
		}
		tokens := estimatePromptEventTokens(ev)
		if len(current) > 0 && currentTokens+tokens > budget {
			chunks = append(chunks, current)
			current = make([]*sdksession.Event, 0, 8)
			currentTokens = 0
		}
		current = append(current, sdksession.CloneEvent(ev))
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

func latestProviderTokenSnapshot(events []*sdksession.Event) (providerTokenSnapshot, bool) {
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

func providerSnapshotGroupStart(events []*sdksession.Event, end int) int {
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

func providerSnapshotSignature(event *sdksession.Event) string {
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

func estimateTokensFromIndex(events []*sdksession.Event, index int) int {
	if index <= 0 {
		return estimatePromptEventsTokens(events)
	}
	total := 0
	for _, event := range events[index:] {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventsTokens(events []*sdksession.Event) int {
	total := 0
	for _, event := range events {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventTokens(event *sdksession.Event) int {
	if event == nil {
		return 0
	}
	if event.Message != nil {
		return estimateMessageTokens(*event.Message)
	}
	if text := strings.TrimSpace(sdksession.EventText(event)); text != "" {
		return estimateTextTokens(text)
	}
	return 0
}

func estimateMessageTokens(message sdkmodel.Message) int {
	total := 0
	if text := strings.TrimSpace(message.TextContent()); text != "" {
		total += estimateTextTokens(text)
	}
	for _, call := range message.ToolCalls() {
		total += estimateTextTokens(call.Name) + estimateTextTokens(call.Args)
	}
	for _, result := range message.ToolResults() {
		total += estimateTextTokens(result.Name)
		response := sdkmodel.Message{
			Role: sdkmodel.RoleTool,
			Parts: []sdkmodel.Part{{
				Kind:       sdkmodel.PartKindToolResult,
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

func resolveContextWindowTokens(model sdkmodel.LLM, fallback int) int {
	if provider, ok := model.(sdkcompact.ContextWindowProvider); ok {
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

func lastEventID(events []*sdksession.Event) string {
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
	if sdkmodel.IsContextOverflow(err) {
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
