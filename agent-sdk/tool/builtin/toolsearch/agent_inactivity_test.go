package toolsearch

import (
	"context"
	"errors"
	"iter"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type activitySelectionModel struct {
	interval  time.Duration
	activity  bool
	toolDelta bool
	finalAt   time.Duration
}

func (*activitySelectionModel) Name() string { return "activity-selector" }
func (*activitySelectionModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (m *activitySelectionModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		model.RecordInvocationUsage(ctx, model.Usage{Reported: true, PromptTokens: 7})
		elapsed := time.Duration(0)
		for elapsed < m.finalAt {
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			case <-time.After(m.interval):
			}
			elapsed += m.interval
			if elapsed >= m.finalAt {
				break
			}
			event := &model.StreamEvent{Type: model.StreamEventPartDelta, PartDelta: &model.PartDelta{Kind: model.PartKindReasoning}}
			if m.activity {
				event.PartDelta.TextDelta = "thinking"
			}
			if m.toolDelta {
				event.PartDelta.Kind = model.PartKindToolUse
				event.PartDelta.InputDelta = `{"name":`
			}
			if !yield(event, nil) {
				return
			}
		}
		yield(model.StreamEventFromResponse(&model.Response{
			Message:      model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__lookup"]}`),
			TurnComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop,
		}), nil)
	}
}

func TestAgentSearchInactivityAllowsLongReasoningButNotEmptyHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		model      activitySelectionModel
		wantTime   time.Duration
		wantErr    error
		wantResult string
	}{
		{"reasoning beyond old total budget", activitySelectionModel{interval: 20 * time.Second, activity: true, finalAt: 100 * time.Second}, 100 * time.Second, nil, "docs__lookup"},
		{"tool arguments beyond old total budget", activitySelectionModel{interval: 20 * time.Second, toolDelta: true, finalAt: 100 * time.Second}, 100 * time.Second, nil, "docs__lookup"},
		{"empty protocol heartbeat", activitySelectionModel{interval: 5 * time.Second, finalAt: 100 * time.Second}, 30 * time.Second, errSelectorInactive, ""},
		{"silent model", activitySelectionModel{interval: 100 * time.Second, finalAt: 100 * time.Second}, 30 * time.Second, errSelectorInactive, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var receipts []model.Invocation
				ctx := WithInvocationAccounting(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) }, nil)
				candidate := tool.Definition{Name: "docs__lookup", Description: "Find a synthetic record"}
				started := time.Now()
				got, err := (agentRanker{inactivityTimeout: 30 * time.Second}).Rank(ctx, "record", []tool.Definition{candidate}, 1, SearchModel{
					Model: &tc.model, ReadSchema: func(context.Context, string) (tool.Definition, error) {
						t.Fatal("schema read after incomplete model stream")
						return tool.Definition{}, nil
					},
				})
				if time.Since(started) != tc.wantTime || !errors.Is(err, tc.wantErr) {
					t.Fatalf("elapsed=%s error=%v, want %s and %v", time.Since(started), err, tc.wantTime, tc.wantErr)
				}
				if tc.wantResult == "" && len(got) != 0 || tc.wantResult != "" && (len(got) != 1 || got[0] != tc.wantResult) {
					t.Fatalf("selection=%v, want %q", got, tc.wantResult)
				}
				if len(receipts) != 1 || receipts[0].Usage.PromptTokens != 7 || receipts[0].Outcome != map[bool]string{true: "completed", false: "cancelled"}[tc.wantErr == nil] {
					t.Fatalf("invocation receipts=%+v", receipts)
				}
			})
		})
	}
}
