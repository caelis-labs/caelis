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
	if choice == "allow_once" || choice == "reject_once" {
		answer.Probabilities = map[string]float64{"allow_once": 1 - confidence, "reject_once": 1 - confidence}
		answer.Probabilities[choice] = confidence
	}
	return answer
}

func noulAnswer(value float64) judgment.Answer {
	return judgment.Answer{Type: judgment.Noul, Noul: &value}
}

// guardianScreenAnswers builds the three answers a screening request expects:
// the original-option distribution plus the independent material_unknown and
// visible_violation evidence gates.
func guardianScreenAnswers(choice string, confidence, unknown, violation float64) map[string]judgment.Answer {
	return guardianScreenAnswersWith(choiceAnswer(choice, confidence), unknown, violation)
}

// guardianScreenAnswersWith pairs an explicit decision answer with valid evidence
// gates for option sets whose IDs are not allow_once/reject_once.
func guardianScreenAnswersWith(decision judgment.Answer, unknown, violation float64) map[string]judgment.Answer {
	return map[string]judgment.Answer{
		"decision":          decision,
		"material_unknown":  noulAnswer(unknown),
		"visible_violation": noulAnswer(violation),
	}
}

func TestGuardianJudgmentUsesCanonicalSourcesAndStrictSettlement(t *testing.T) {
	for _, tc := range []struct {
		name, decision     string
		confidence         float64
		unknown, violation float64
		wantErr, allow     bool
	}{
		{"allow", "allow_once", 1, 0, 0, false, true},
		{"deny without explanation", "reject_once", 1, 0, 1, false, false},
		{"uncertain allow", "allow_once", .5, 0, 0, true, false},
		{"uncertain deny", "reject_once", .6, 0, 1, true, false},
		{"unknown option", "injected", 1, 0, 0, true, false},
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
				if len(r.Questions) != 3 || r.Questions["decision"].Type != judgment.Choice || r.Questions["material_unknown"].Type != judgment.Noul || r.Questions["visible_violation"].Type != judgment.Noul {
					t.Error("classifier must ask the original-option decision and two Noul evidence gates")
				}
				for _, id := range []string{"material_unknown", "visible_violation"} {
					instructions, _ := r.Questions[id].Instructions.(string)
					if !strings.Contains(instructions, "no historical tool observations or trusted sandbox guarantees") {
						t.Errorf("%s question must exclude trusted sandbox metadata", id)
					}
				}
				if strings.Contains(string(raw), "UNTRUSTED_ASSISTANT_PERMISSION") || !strings.Contains(string(raw), "Do not delete files") || !strings.Contains(string(raw), command) {
					t.Errorf("invalid canonical projection")
				}
				return judgment.Response{Model: "fixture-classifier", Answers: guardianScreenAnswers(tc.decision, tc.confidence, tc.unknown, tc.violation), Usage: judgment.Usage{InputTokens: 100}}, nil
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
