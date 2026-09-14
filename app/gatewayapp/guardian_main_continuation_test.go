package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type guardianMainRecoveryModel struct {
	approvalReviewerFakeModel
	step int
}

func (*guardianMainRecoveryModel) Name() string { return "main-recovery-fixture" }
func (m *guardianMainRecoveryModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.step++
		if m.step == 2 {
			found := false
			for _, message := range req.Messages {
				for _, part := range message.Parts {
					if part.ToolResult != nil && part.ToolResult.ToolUseID == "first" && part.ToolResult.IsError {
						found = true
					}
				}
			}
			if !found {
				yield(nil, fmt.Errorf("main model did not receive unavailable action result"))
				return
			}
		}
		message := model.NewTextMessage(model.RoleAssistant, "Continued independent work after unavailable approval.")
		switch m.step {
		case 1:
			message = model.NewMessage(model.RoleAssistant, model.NewToolUsePart("first", "ReviewedAction", json.RawMessage(`{}`)))
		case 2:
			message = model.NewMessage(model.RoleAssistant, model.NewToolUsePart("second", "IndependentWork", json.RawMessage(`{}`)))
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
	}
}

func TestGuardianUnavailableActionDoesNotStopMainSDKTurn(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	failing := &approvalReviewerFakeModel{responses: []string{"invalid", "invalid"}}
	mainModel := &guardianMainRecoveryModel{}
	executed, continued := false, false
	tools := []tool.Tool{
		tool.NamedTool{Def: tool.Definition{Name: "ReviewedAction"}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
			decision, err := reviewer.Decide(ctx, approvalReviewerTestRequest(active, failing, "review fixture", nil))
			if err != nil {
				return tool.Result{}, err
			}
			executed = decision.Approved
			return tool.Result{}, nil
		}},
		tool.NamedTool{Def: tool.Definition{Name: "IndependentWork"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			continued = true
			return tool.Result{Content: []model.Part{model.NewTextPart("independent work completed")}}, nil
		}},
	}
	runtime, err := sdkruntime.New(sdkruntime.Config{Sessions: service, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "Do the task; continue independent work if one action is unavailable.", AgentSpec: agent.AgentSpec{Name: "main", Model: mainModel, Tools: tools}})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Handle.Close()
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if executed || !continued || mainModel.step != 3 {
		t.Fatalf("executed=%v continued=%v steps=%d", executed, continued, mainModel.step)
	}
	if len(failing.Requests()) != 2 {
		t.Fatal("did not exhaust real Guardian format repair")
	}
}
