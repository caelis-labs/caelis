package gatewayapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type judgmentFunc func(context.Context, judgment.Request) (judgment.Response, error)

func (judgmentFunc) Name() string { return "fixture-classifier" }
func (f judgmentFunc) Evaluate(ctx context.Context, r judgment.Request) (judgment.Response, error) {
	return f(ctx, r)
}
func choiceAnswer(choice string, confidence float64) judgment.Answer {
	answer := judgment.Answer{Type: judgment.Choice, Choice: choice, Confidence: &confidence}
	if choice == "0" || choice == "1" || choice == "unavailable" {
		answer.Probabilities = map[string]float64{"0": 0, "1": 0, "unavailable": 0}
		answer.Probabilities[choice] = confidence
		if choice == "unavailable" {
			answer.Probabilities["0"] = 1 - confidence
		} else {
			answer.Probabilities["unavailable"] = 1 - confidence
		}
	}
	return answer
}

func TestGuardianJudgmentUsesCanonicalSourcesAndStrictSettlement(t *testing.T) {
	for _, tc := range []struct {
		name, decision string
		confidence     float64
		wantErr, allow bool
	}{
		{"allow", "0", 1, false, true},
		{"deny without explanation", "1", 1, false, false},
		{"uncertain allow", "0", .5, true, false},
		{"uncertain deny", "1", .6, true, false},
		{"missing evidence", "unavailable", 1, true, false},
		{"unknown option", "injected", 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, active := newApprovalReviewerTestSession(t, t.Context())
			appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Inspect the project. Do not delete files.")
			appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeAssistant, model.RoleAssistant, "UNTRUSTED_ASSISTANT_PERMISSION")
			reviewer := newGuardianApprovalApprover(service)
			defer reviewer.Close()
			command := "rm README.md"
			if tc.allow {
				command = "rg TODO ."
			}
			req := approvalReviewerTestRequest(active, nil, "inspect", map[string]any{"cmd": command})
			req.Judgment = judgmentFunc(func(_ context.Context, r judgment.Request) (judgment.Response, error) {
				raw, _ := json.Marshal(r)
				if len(r.Questions) != 2 {
					t.Error("classifier must ask only decision and consequences")
				}
				if strings.Contains(string(raw), "UNTRUSTED_ASSISTANT_PERMISSION") || !strings.Contains(string(raw), "Do not delete files") || !strings.Contains(string(raw), command) {
					t.Errorf("invalid canonical projection")
				}
				return judgment.Response{Model: "fixture-classifier", Answers: map[string]judgment.Answer{"decision": choiceAnswer(tc.decision, tc.confidence)}, Usage: judgment.Usage{InputTokens: 100}}, nil
			})
			result, err := reviewer.runGuardianJudgment(t.Context(), req, &guardianInvocationCollector{})
			if (err != nil) != tc.wantErr || result.Approved != tc.allow {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !tc.wantErr && !tc.allow && (result.Rationale != "" || result.DisplayText != "denied") {
				t.Fatalf("classifier invented denial explanation: %s", result.Rationale)
			}
			if tc.allow && result.Rationale != "" {
				t.Fatal("allow added rationale")
			}
			for _, resident := range reviewer.residents {
				for range len(resident.lanes) {
					lane := <-resident.lanes
					if lane != nil && lane.runtime != nil {
						t.Fatal("classifier initialized query sandbox")
					}
					resident.lanes <- lane
				}
			}
		})
	}
}
