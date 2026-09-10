package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// responsesStreamConfig separates wire decoding from endpoint authentication
// and request policy. The same decoder serves API-key and subscription clients.
type responsesStreamConfig struct {
	name                string
	provider            string
	model               string
	replayProvider      string
	contextWindowTokens int
	firstEventTimeout   time.Duration
	idleTimeout         time.Duration
	plainReasoning      bool
	terminalErrorCodes  map[string]responsesErrorClassification
}

func readResponsesStream(ctx context.Context, body io.ReadCloser, req *model.Request, cfg responsesStreamConfig, yield func(*model.StreamEvent, error) bool) {
	accumulator := newOpenAIResponsesAccumulator(cfg.replayProvider)
	accumulator.plainReasoning = cfg.plainReasoning
	terminalSeen := false
	stopped := false
	err := readSSEWithActivityTimeout(body, cfg.firstEventTimeout, cfg.idleTimeout, responsesSSEHasSemanticActivity, func(data []byte) error {
		var event openAICodexStreamWire
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("%s: decode stream event: %w", cfg.name, err)
		}
		if event.Response != nil && event.Response.Usage != nil {
			model.RecordInvocationUsage(ctx, event.Response.Usage.toKernelUsage())
		}
		switch event.Type {
		case "response.output_item.added", "response.output_item.done":
			if event.Item != nil {
				accumulator.applyItem(*event.Item, event.OutputIndex)
			}
		case "response.output_text.delta", "response.refusal.delta":
			if event.Delta == "" {
				return nil
			}
			accumulator.appendText(event)
			if req.Stream && !yield(&model.StreamEvent{
				Type:      model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{Index: event.OutputIndex, Kind: model.PartKindText, TextDelta: event.Delta},
			}, nil) {
				stopped = true
				return errStopSSE
			}
		case "response.reasoning_text.delta", "response.reasoning_summary.delta", "response.reasoning_summary_text.delta":
			if event.Delta == "" {
				return nil
			}
			delta := accumulator.appendReasoning(event)
			if req.Stream && !yield(&model.StreamEvent{
				Type:      model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{Index: event.OutputIndex, Kind: model.PartKindReasoning, TextDelta: delta},
			}, nil) {
				stopped = true
				return errStopSSE
			}
		case "response.function_call_arguments.delta":
			if event.Delta == "" {
				return nil
			}
			accumulator.appendArguments(event)
			if req.Stream && !yield(&model.StreamEvent{
				Type:      model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{Index: event.OutputIndex, Kind: model.PartKindToolUse, InputDelta: event.Delta},
			}, nil) {
				stopped = true
				return errStopSSE
			}
		case "response.completed", "response.incomplete":
			if event.Response == nil {
				return errorcode.New(errorcode.Internal, cfg.name+": terminal response is empty")
			}
			response, err := cfg.response(event.Response, accumulator)
			if err != nil {
				return err
			}
			terminalSeen = true
			if !yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil) {
				stopped = true
			}
			return errStopSSE
		case "response.failed", "error":
			return responsesStreamError(cfg.name, event, cfg.terminalErrorCodes)
		}
		return nil
	})
	if stopped {
		return
	}
	if err != nil {
		yield(nil, err)
		return
	}
	if !terminalSeen {
		yield(nil, fmt.Errorf("%s: stream ended before a terminal response", cfg.name))
	}
}

func (cfg responsesStreamConfig) response(wire *openAICodexResponseWire, accumulator *openAICodexAccumulator) (*model.Response, error) {
	for index, item := range wire.Output {
		accumulator.applyItem(item, index)
	}
	message, err := accumulator.message()
	if err != nil {
		return nil, err
	}
	finishReason, rawFinishReason := openAICodexFinishReason(wire, accumulator.hasToolCall)
	usage := model.Usage{}
	if wire.Usage != nil {
		usage = wire.Usage.toKernelUsage()
	}
	return &model.Response{
		Message:             message,
		StepComplete:        true,
		TurnComplete:        true,
		Status:              model.ResponseStatusCompleted,
		FinishReason:        finishReason,
		RawFinishReason:     rawFinishReason,
		Usage:               usage,
		Model:               firstNonEmptyString(wire.Model, cfg.model),
		Provider:            cfg.provider,
		ContextWindowTokens: cfg.contextWindowTokens,
	}, nil
}
