package runtime

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type stepWatermarkModel struct {
	t                    *testing.T
	normalCalls          int
	compactionCalls      int
	sawCheckpointOnRetry bool
	compactionErr        error
}

func (m *stepWatermarkModel) Name() string { return "step-watermark" }
func (m *stepWatermarkModel) ProviderName() string {
	return "test-provider"
}

func (m *stepWatermarkModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if isRuntimeCompactionRequest(req) {
		m.compactionCalls++
		if m.compactionErr != nil {
			return func(yield func(*model.StreamEvent, error) bool) {
				yield(nil, m.compactionErr)
			}
		}
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
				Provider:     "test-provider",
				Model:        "step-watermark",
				Usage: model.Usage{
					PromptTokens:     190,
					CompletionTokens: 6,
					TotalTokens:      196,
				},
			}), nil)
		case 2:
			requestText := strings.Join(requestMessageTexts(req), "\n")
			hasToolContinuity := strings.Contains(requestText, "ECHO tool result completed") || strings.Contains(requestText, "value: pong")
			if !strings.Contains(requestText, "CONTEXT CHECKPOINT") || !hasToolContinuity {
				m.t.Fatalf("post-compact request missing checkpoint continuity: %s", requestText)
			}
			m.sawCheckpointOnRetry = true
			yield(model.StreamEventFromResponse(&model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "recovered after step compact"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
				Provider:     "test-provider",
				Model:        "step-watermark",
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

type identifiedCompactionModel struct {
	staticModel
	providerName string
	modelName    string
}

func (m identifiedCompactionModel) Name() string         { return m.modelName }
func (m identifiedCompactionModel) ProviderName() string { return m.providerName }

type attachmentUsageModel struct {
	t               *testing.T
	normalCalls     int
	compactionCalls int
}

func (m *attachmentUsageModel) Name() string             { return "gpt-5.6-sol" }
func (m *attachmentUsageModel) ProviderName() string     { return "openai-codex" }
func (m *attachmentUsageModel) ContextWindowTokens() int { return 258400 }
func (m *attachmentUsageModel) Capabilities() model.Capabilities {
	capabilities := runtimeTestModelCapabilities()
	capabilities.ImageInput = true
	return capabilities
}

func (m *attachmentUsageModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if isRuntimeCompactionRequest(req) {
		m.compactionCalls++
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(model.StreamEventFromResponse(&model.Response{
				Message: model.NewTextMessage(model.RoleAssistant, `CONTEXT CHECKPOINT

## Current Objective
- Finish the attachment-assisted tool turn.

## Next Actions
1. Continue the turn from the compacted context.`),
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
					ID:   "call-attachment-usage",
					Name: "ECHO",
					Args: `{"value":"pong"}`,
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonToolCalls,
				Provider:     "openai-codex",
				Model:        "gpt-5.6-sol",
				Usage: model.Usage{
					PromptTokens:     92576,
					CompletionTokens: 6,
					TotalTokens:      92582,
				},
			}), nil)
		case 2:
			if !requestHasToolResult(req, "ECHO") {
				m.t.Fatalf("post-tool request missing ECHO result: %+v", req.Messages)
			}
			yield(model.StreamEventFromResponse(&model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "completed without false compact"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
				Provider:     "openai-codex",
				Model:        "gpt-5.6-sol",
				Usage: model.Usage{
					PromptTokens:     92620,
					CompletionTokens: 5,
					TotalTokens:      92625,
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

type repeatedWatermarkModel struct {
	t                    *testing.T
	toolCallsBeforeFinal int
	normalCalls          int
	compactionCalls      int
	checkpointRequests   int
}

func (m *repeatedWatermarkModel) Name() string { return "repeated-watermark" }
func (m *repeatedWatermarkModel) ProviderName() string {
	return "test-provider"
}

func (m *repeatedWatermarkModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if isRuntimeCompactionRequest(req) {
		m.compactionCalls++
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(model.StreamEventFromResponse(&model.Response{
				Message: model.NewTextMessage(model.RoleAssistant, `CONTEXT CHECKPOINT

## Current Objective
- Continue a long tool-assisted turn that may require repeated compaction.

## Progress
- Prior ECHO tool progress has been summarized.

## Next Actions
1. Continue the turn from this checkpoint.`),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			}), nil)
		}
	}

	m.normalCalls++
	callIndex := m.normalCalls
	if callIndex > 1 {
		requestText := strings.Join(requestMessageTexts(req), "\n")
		if !strings.Contains(requestText, "CONTEXT CHECKPOINT") {
			m.t.Fatalf("request %d missing compact checkpoint: %s", callIndex, requestText)
		}
		m.checkpointRequests++
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex <= m.toolCallsBeforeFinal {
			yield(model.StreamEventFromResponse(&model.Response{
				Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
					ID:   fmt.Sprintf("call-repeated-%d", callIndex),
					Name: "ECHO",
					Args: fmt.Sprintf(`{"step":%d}`, callIndex),
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonToolCalls,
				Provider:     "test-provider",
				Model:        "repeated-watermark",
				Usage: model.Usage{
					PromptTokens:     190,
					CompletionTokens: 5,
					TotalTokens:      195,
				},
			}), nil)
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{
			Message:      model.NewTextMessage(model.RoleAssistant, "finished after repeated compactions"),
			TurnComplete: true,
			StepComplete: true,
			Status:       model.ResponseStatusCompleted,
			FinishReason: model.FinishReasonStop,
			Provider:     "test-provider",
			Model:        "repeated-watermark",
			Usage: model.Usage{
				PromptTokens:     80,
				CompletionTokens: 5,
				TotalTokens:      85,
			},
		}), nil)
	}
}

func (m *retryExhaustedHighWaterModel) Name() string { return "retry-exhausted-high-water" }
func (m *retryExhaustedHighWaterModel) ProviderName() string {
	return "test-provider"
}

func (m *retryExhaustedHighWaterModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if isRuntimeCompactionRequest(req) {
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
				Provider:     "test-provider",
				Model:        "retry-exhausted-high-water",
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
