package kernel

import (
	"context"
	"fmt"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type approvalJudgmentResolverStub struct {
	staticResolver
	evaluator judgment.Evaluator
	err       error
}

func (r approvalJudgmentResolverStub) ResolveApprovalJudgment(context.Context, session.SessionRef) (judgment.Evaluator, error) {
	return r.evaluator, r.err
}

type configuredScreenResolver struct {
	approvalJudgmentResolverStub
	calls *int
	fail  bool
}

func (r configuredScreenResolver) ResolveApprovalModel(context.Context, session.SessionRef) (model.LLM, error) {
	*r.calls++
	if r.fail {
		return nil, fmt.Errorf("Agent provider unavailable")
	}
	return fakeLLM{name: "configured-guardian"}, nil
}

type judgmentReviewFunc func(context.Context, ApprovalReviewRequest) (ApprovalReviewResult, error)

func (f judgmentReviewFunc) ReviewApproval(ctx context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
	return f(ctx, req)
}

type unusedJudgment struct{}

func (unusedJudgment) Name() string { return "classifier" }
func (unusedJudgment) Evaluate(context.Context, judgment.Request) (judgment.Response, error) {
	return judgment.Response{}, fmt.Errorf("not invoked by recording approver")
}

func TestApprovalScreeningPreservesModelSelectionAndExecutionGate(t *testing.T) {
	for _, tc := range []struct {
		name                                                      string
		configured, screened, needsAgent, screenError, modelError bool
	}{
		{name: "complete screen does not resolve unavailable Agent", configured: true, screened: true, modelError: true},
		{name: "screen falls back to configured Agent", configured: true, screened: true, needsAgent: true},
		{name: "screen falls back to main model", screened: true, needsAgent: true},
		{name: "classifier construction error uses Agent", configured: true, screened: true, needsAgent: true, screenError: true},
		{name: "no screen uses configured Agent", configured: true, needsAgent: true},
		{name: "no bindings uses main model", needsAgent: true},
		{name: "Agent resolution failure never executes", configured: true, screened: true, needsAgent: true, modelError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active := session.Session{SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws"}}
			runtime := &approvalRuntime{session: active}
			base := approvalJudgmentResolverStub{staticResolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{AgentSpec: agent.AgentSpec{Model: fakeLLM{name: "main"}}}}}}
			if tc.screened {
				base.evaluator = unusedJudgment{}
			}
			if tc.screenError {
				base.err = fmt.Errorf("classifier unavailable")
			}
			var resolver TurnResolver = base
			calls := 0
			if tc.configured {
				resolver = configuredScreenResolver{approvalJudgmentResolverStub: base, calls: &calls, fail: tc.modelError}
			}
			reviewer := judgmentReviewFunc(func(ctx context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
				if (req.Judgment != nil) != (tc.screened && !tc.screenError) {
					t.Error("invalid screening selection")
				}
				if tc.needsAgent {
					resolved := req.Model
					if req.ResolveModel != nil {
						if calls != 0 {
							t.Error("Agent resolved before classifier deferred")
						}
						var err error
						resolved, err = req.ResolveModel(ctx)
						if err != nil {
							return ApprovalReviewResult{}, err
						}
					}
					want := "main"
					if tc.configured {
						want = "configured-guardian"
					}
					if resolved == nil || resolved.Name() != want {
						t.Fatalf("Agent model=%v want %s", resolved, want)
					}
				} else if req.ResolveModel == nil || req.Model != nil {
					t.Error("screening did not defer Agent resolution")
				}
				return ApprovalReviewResult{Approved: true}, nil
			})
			gateway, err := New(Config{Sessions: staticSessionService{session: active}, Runtime: runtime, Resolver: resolver, ApprovalReviewer: reviewer})
			if err != nil {
				t.Fatal(err)
			}
			result, err := gateway.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: active.SessionRef, Input: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			_ = collectHandleEvents(t, result.Handle)
			wantCalls, wantExecutions := 0, 1
			if tc.configured && tc.needsAgent {
				wantCalls = 1
			}
			if tc.modelError && tc.needsAgent {
				wantExecutions = 0
			}
			if calls != wantCalls || runtime.executionCount() != wantExecutions {
				t.Fatalf("model calls=%d executions=%d", calls, runtime.executionCount())
			}
		})
	}
}
