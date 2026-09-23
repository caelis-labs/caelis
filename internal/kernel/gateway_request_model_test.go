package kernel

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestBeginTurnApprovalUsesRequestModelAndPreservesGuardianOverride(t *testing.T) {
	for _, test := range []struct {
		name     string
		pinned   model.LLM
		guardian model.LLM
		want     string
	}{
		{name: "static fallback", want: "bootstrap"},
		{name: "request snapshot", pinned: fakeLLM{name: "request-model"}, want: "request-model"},
		{name: "configured guardian", pinned: fakeLLM{name: "request-model"}, guardian: fakeLLM{name: "guardian"}, want: "guardian"},
	} {
		t.Run(test.name, func(t *testing.T) {
			active := session.Session{SessionRef: session.SessionRef{AppName: "caelis", UserID: "user", SessionID: "session", WorkspaceKey: "workspace"}}
			base := staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{AgentSpec: agent.AgentSpec{Model: fakeLLM{name: "bootstrap"}}}}}
			var resolver TurnResolver = base
			if test.guardian != nil {
				resolver = approvalModelResolverStub{staticResolver: base, model: test.guardian}
			}
			reviewer := &recordingApprovalReviewer{result: ApprovalReviewResult{Approved: true}}
			runtime := &approvalRuntime{session: active, model: test.pinned}
			gateway, err := New(Config{Sessions: staticSessionService{session: active}, Runtime: runtime, Resolver: resolver, ApprovalReviewer: reviewer})
			if err != nil {
				t.Fatal(err)
			}
			run, err := gateway.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: active.SessionRef, Input: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			_ = collectHandleEvents(t, run.Handle)
			if reviewer.req.Model == nil || reviewer.req.Model.Name() != test.want || runtime.executionCount() != 1 {
				t.Fatalf("review model=%v executions=%d want %q", reviewer.req.Model, runtime.executionCount(), test.want)
			}
		})
	}
}
