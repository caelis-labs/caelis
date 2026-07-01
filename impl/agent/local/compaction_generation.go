package local

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

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
		if ctx.Err() != nil {
			return "", lastErr
		}
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
