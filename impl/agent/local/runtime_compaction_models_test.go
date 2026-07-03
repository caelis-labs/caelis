package local

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/ports/model"
)

type stepWatermarkModel struct {
	t                    *testing.T
	normalCalls          int
	compactionCalls      int
	sawCheckpointOnRetry bool
}

func (m *stepWatermarkModel) Name() string { return "step-watermark" }

func (m *stepWatermarkModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if strings.Contains(requestInstructionsText(req), "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(model.StreamEventFromResponse(&model.Response{
				Message: model.NewTextMessage(model.RoleAssistant, `CONTEXT CHECKPOINT

## Current Objective
- Finish the tool-assisted turn after compacting before the next model request.

## Validation And Tool Results
- ECHO tool result completed with value pong.
- Preserve the tool result continuity.

## Next Actions
1. Continue the turn and provide the final answer.`),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			}), nil)
		}
	}
	m.normalCalls++
	callIndex := m.normalCalls
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(model.StreamEventFromResponse(&model.Response{
				Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
					ID:   "call-step-compact",
					Name: "ECHO",
					Args: `{"value":"pong"}`,
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonToolCalls,
				Usage: model.Usage{
					PromptTokens:     190,
					CompletionTokens: 6,
					TotalTokens:      196,
				},
			}), nil)
		case 2:
			requestText := strings.Join(requestMessageTexts(req), "\n")
			if !strings.Contains(requestText, "CONTEXT CHECKPOINT") || !strings.Contains(requestText, "ECHO tool result completed") {
				m.t.Fatalf("post-compact request missing checkpoint continuity: %s", requestText)
			}
			m.sawCheckpointOnRetry = true
			yield(model.StreamEventFromResponse(&model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "recovered after step compact"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
				Usage: model.Usage{
					PromptTokens:     80,
					CompletionTokens: 6,
					TotalTokens:      86,
				},
			}), nil)
		default:
			m.t.Fatalf("unexpected normal model call %d", callIndex)
		}
	}
}

type retryExhaustedHighWaterModel struct {
	t                       *testing.T
	normalCalls             int
	compactionCalls         int
	sawPostToolRetryRequest bool
	sawCheckpointOnRetry    bool
}

func (m *retryExhaustedHighWaterModel) Name() string { return "retry-exhausted-high-water" }

func (m *retryExhaustedHighWaterModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if strings.Contains(requestInstructionsText(req), "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(model.StreamEventFromResponse(&model.Response{
				Message: model.NewTextMessage(model.RoleAssistant, `CONTEXT CHECKPOINT

## Current Objective
- Finish the turn after compacting a retry-exhausted high-water request.

## Validation And Tool Results
- ECHO tool result completed with retry exhausted high-water tool result.
- Preserve the tool result continuity.

## Next Actions
1. Retry the model request with this checkpoint.`),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			}), nil)
		}
	}
	m.normalCalls++
	callIndex := m.normalCalls
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(model.StreamEventFromResponse(&model.Response{
				Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
					ID:   "call-retry-exhausted-compact",
					Name: "ECHO",
					Args: `{"value":"pong"}`,
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonToolCalls,
				Usage: model.Usage{
					PromptTokens:     245,
					CompletionTokens: 1,
					TotalTokens:      246,
				},
			}), nil)
		case 2:
			m.sawPostToolRetryRequest = true
			yield(nil, &model.RetryExhaustedError{
				MaxRetries: 5,
				Cause:      errors.New("model: http status 500 body=Internal Server Error"),
			})
		case 3:
			requestText := strings.Join(requestMessageTexts(req), "\n")
			if !strings.Contains(requestText, "CONTEXT CHECKPOINT") || !strings.Contains(requestText, "retry exhausted high-water tool result") {
				m.t.Fatalf("retry request missing checkpoint continuity: %s", requestText)
			}
			m.sawCheckpointOnRetry = true
			yield(model.StreamEventFromResponse(&model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "recovered after retry exhausted compact"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			}), nil)
		default:
			m.t.Fatalf("unexpected normal model call %d", callIndex)
		}
	}
}
