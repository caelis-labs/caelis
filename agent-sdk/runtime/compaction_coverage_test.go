package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type coverageModel struct {
	calls    int
	response *model.Response
	err      error
}

func (*coverageModel) Name() string         { return "coverage" }
func (*coverageModel) ProviderName() string { return "fixture" }
func (m *coverageModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if m.calls == 1 && m.err != nil {
			yield(nil, m.err)
			return
		}
		response := m.response
		if response == nil || m.calls > 1 {
			response = &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "CONTEXT CHECKPOINT\n\nPreserve the user's objective and authorization constraints. Keep unknown outcomes explicit and continue the remaining implementation and validation."), TurnComplete: true}
		}
		yield(model.StreamEventFromResponse(response), nil)
	}
}

func coverageFingerprint(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func coverageMessage(role model.Role, text string) *model.Message {
	message := model.NewTextMessage(role, text)
	return &message
}

func coverageRequest(t *testing.T, llm *coverageModel) compact.Request {
	t.Helper()
	req := compact.Request{
		Model:  llm,
		Events: []*session.Event{{ID: "user", Seq: 1, Type: session.EventTypeUser, Message: coverageMessage(model.RoleUser, "preserve the objective")}, {ID: "assistant", Seq: 2, Type: session.EventTypeAssistant, Message: coverageMessage(model.RoleAssistant, "continue validation")}},
		InContextRequest: &model.Request{Instructions: []model.Part{model.NewTextPart("preserve all constraints")}, Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "preserve the objective"),
			model.NewTextMessage(model.RoleAssistant, "continue validation"),
		}},
		RequireInContext: true,
	}
	bindCoverage(t, &req)
	return req
}

func bindCoverage(t *testing.T, req *compact.Request) {
	t.Helper()
	delta := compactableEvents(req.Events)
	req.Coverage = &compact.Coverage{
		SourceThroughSeq: session.LastEventSeq(req.Events), SummarizedThroughSeq: session.LastEventSeq(delta),
		SummarizedThroughID: lastEventID(delta), SourceEventCount: len(delta),
		SourcePrefixHash: coverageFingerprint(t, req.Events), FrozenRequestHash: coverageFingerprint(t, req.InContextRequest),
	}
}

func TestCompactionCoverageRejectsInvalidBeforeGeneration(t *testing.T) {
	for _, path := range []string{"force", "compact"} {
		for _, tt := range []struct {
			name   string
			mutate func(*compact.Request)
		}{
			{"missing_request", func(r *compact.Request) { r.InContextRequest = nil }},
			{"missing_coverage", func(r *compact.Request) { r.Coverage = nil }},
			{"empty_missing_coverage", func(r *compact.Request) { r.Events = nil; r.Coverage = nil }},
			{"empty_missing_request", func(r *compact.Request) { r.Events = nil; bindCoverage(t, r); r.InContextRequest = nil }},
			{"source_hash", func(r *compact.Request) { r.Events[0].Message = coverageMessage(model.RoleUser, "mutated source") }},
			{"frozen_hash", func(r *compact.Request) {
				r.InContextRequest.Instructions = []model.Part{model.NewTextPart("mutated instructions")}
			}},
			{"source_seq", func(r *compact.Request) { r.Coverage.SourceThroughSeq++ }},
			{"count", func(r *compact.Request) { r.Coverage.SourceEventCount++ }},
			{"id", func(r *compact.Request) { r.Coverage.SummarizedThroughID = "wrong" }},
			{"seq", func(r *compact.Request) { r.Coverage.SummarizedThroughSeq-- }},
			{"summarized_after_source", func(r *compact.Request) { r.Coverage.SummarizedThroughSeq = r.Coverage.SourceThroughSeq + 1 }},
			{"coverage_requires_request", func(r *compact.Request) {
				r.RequireInContext = false
				r.Coverage.RequireInContext = true
				r.InContextRequest = nil
			}},
		} {
			t.Run(path+"/"+tt.name, func(t *testing.T) {
				llm := &coverageModel{}
				req := coverageRequest(t, llm)
				tt.mutate(&req)
				compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{})}
				var result compact.Result
				var err error
				if path == "force" {
					result, err = compactor.Force(t.Context(), req, "test")
				} else {
					result, err = compactor.compact(t.Context(), req, "test")
				}
				var coverageErr *compact.CoverageError
				if !errors.As(err, &coverageErr) {
					t.Fatalf("error = %v, want CoverageError", err)
				}
				if coverageErr.Reason == "" || coverageErr.Retryable() {
					t.Fatalf("invalid coverage error: %#v", coverageErr)
				}
				if llm.calls != 0 || result.Compacted || result.CompactEvent != nil {
					t.Fatalf("calls=%d result=%+v", llm.calls, result)
				}
			})
		}
	}
}

func TestCompactionCoverageAllowsCheckpointAfterRawTail(t *testing.T) {
	llm := &coverageModel{}
	req := coverageRequest(t, llm)
	checkpoint := &session.Event{ID: "checkpoint", Seq: 3, Type: session.EventTypeCompact, Text: "older summary", Meta: map[string]any{compact.MetaKeyCompact: compact.CompactEventDataValue(compact.CompactEventData{ContractVersion: compact.CompactContractVersion, SummarizedThroughSeq: 1, SummarizedThroughID: "user"})}}
	req.Events = append(req.Events, checkpoint)
	req.InContextRequest.Messages = []model.Message{
		model.NewTextMessage(model.RoleUser, "older summary"),
		model.NewTextMessage(model.RoleAssistant, "continue validation"),
	}
	bindCoverage(t, &req)
	if req.Coverage.SourceThroughSeq != 3 || req.Coverage.SummarizedThroughSeq != 2 {
		t.Fatalf("coverage = %+v", req.Coverage)
	}
	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{})}
	result, err := compactor.Force(t.Context(), req, "test")
	if err != nil || !result.Compacted || llm.calls != 1 {
		t.Fatalf("calls=%d result=%+v error=%v", llm.calls, result, err)
	}
	data, ok := compact.CompactEventDataFromEvent(result.CompactEvent)
	if !ok || data.SummarizedThroughSeq != 2 || data.SourceEventCount != 1 {
		t.Fatalf("checkpoint coverage = %+v", data)
	}
}

func TestCompactionRequireInContextNeverFallsBack(t *testing.T) {
	for _, requirement := range []string{"request", "coverage"} {
		for _, failure := range []string{"empty", "tools", "length", "error"} {
			t.Run(requirement+"/"+failure, func(t *testing.T) {
				llm := &coverageModel{response: &model.Response{TurnComplete: true}}
				switch failure {
				case "tools":
					llm.response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call", Name: "tool"}}, "")
				case "length":
					llm.response.FinishReason = model.FinishReasonLength
					llm.response.FinishReasonReported = true
				case "error":
					llm.err = errors.New("generation failed")
				}
				req := coverageRequest(t, llm)
				if requirement == "coverage" {
					req.RequireInContext = false
					req.Coverage.RequireInContext = true
				}
				compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{MaxRetryAttempts: 3})}
				result, err := compactor.Force(t.Context(), req, "test")
				if err == nil || llm.calls != 1 || result.Compacted || result.CompactEvent != nil {
					t.Fatalf("calls=%d result=%+v error=%v", llm.calls, result, err)
				}
			})
		}
	}
}

func TestCompactionRequireInContextOptionalRetainsFallback(t *testing.T) {
	llm := &coverageModel{err: errors.New("in-context generation failed")}
	req := coverageRequest(t, llm)
	req.RequireInContext, req.Coverage = false, nil
	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{})}
	result, err := compactor.Force(t.Context(), req, "test")
	if err != nil || !result.Compacted || llm.calls != 2 {
		t.Fatalf("calls=%d result=%+v error=%v", llm.calls, result, err)
	}
}
