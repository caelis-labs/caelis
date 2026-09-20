package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestGuardianScreeningCascadesToAgentWithinOriginalReview(t *testing.T) {
	for _, tc := range []struct {
		name, decision       string
		confidence           float64
		unknown, violation   float64
		failure              error
		agentResponse        string
		wantAgent, wantAllow bool
	}{
		{name: "allow without resolving Agent", decision: "allow_once", confidence: 1, wantAllow: true},
		{name: "deny without resolving Agent", decision: "reject_once", confidence: 1, violation: 1},
		{name: "visible denial despite unknown effects", decision: "reject_once", confidence: 1, unknown: 1, violation: 1},
		{name: "uncertain deny uses Agent explanation", decision: "reject_once", confidence: .6, violation: 1, agentResponse: `{"option_id":"reject_once","rationale":"The action contradicts the user's explicit constraint."}`, wantAgent: true},
		{name: "uncertain", decision: "allow_once", confidence: .5, wantAgent: true, wantAllow: true},
		{name: "material unknown defers to Agent", decision: "allow_once", confidence: 1, unknown: 1, wantAgent: true, wantAllow: true},
		{name: "visible violation conflict defers to Agent", decision: "allow_once", confidence: 1, violation: 1, wantAgent: true, wantAllow: true},
		{name: "gray evidence defers to Agent", decision: "allow_once", confidence: 1, unknown: .5, violation: .5, wantAgent: true, wantAllow: true},
		{name: "invalid answer", decision: "unknown", confidence: 1, wantAgent: true, wantAllow: true},
		{name: "provider failure", failure: errors.New("screen-only-provider-error"), wantAgent: true, wantAllow: true},
		{name: "screening timeout", failure: context.DeadlineExceeded, wantAgent: true, wantAllow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, active := newApprovalReviewerTestSession(t, t.Context())
			appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Inspect the project. Do not delete files.")
			observed := guardianSource(0, session.EventTypeToolResult, "")
			observed.ID, observed.SessionID = "", active.SessionID
			observed.Tool.Name = "Read"
			observed.Tool.Input = map[string]any{"path": "README.md"}
			observed.Tool.Output = map[string]any{"content": "INDEPENDENT_TOOL_EVIDENCE"}
			if _, err := service.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: observed}); err != nil {
				t.Fatal(err)
			}
			reviewer := newGuardianApprovalApprover(service)
			defer reviewer.Close()
			llm := &approvalReviewerFakeModel{}
			if tc.agentResponse != "" {
				llm.responses = []string{tc.agentResponse}
			}
			command := "rg TODO ."
			if !tc.wantAllow {
				command = "rm README.md"
			}
			req := approvalReviewerTestRequest(active, nil, "inspect", map[string]any{"cmd": command})
			parent, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			deadline, _ := parent.Deadline()
			screened, resolved := 0, 0
			req.Judgment = judgmentFunc(func(ctx context.Context, request judgment.Request) (judgment.Response, error) {
				screened++
				raw, _ := json.Marshal(request)
				if strings.Contains(string(raw), "INDEPENDENT_TOOL_EVIDENCE") {
					t.Error("classifier received tool history")
				}
				if limit, ok := ctx.Deadline(); !ok || time.Until(limit) > 10*time.Second {
					t.Error("screening omitted its bounded deadline")
				}
				return judgment.Response{Model: "screen-only-provider", Answers: guardianScreenAnswers(tc.decision, tc.confidence, tc.unknown, tc.violation)}, tc.failure
			})
			req.ResolveModel = func(ctx context.Context) (model.LLM, error) {
				resolved++
				if screened != 1 || !tc.wantAgent {
					t.Error("Agent model resolved before screening required it")
				}
				if got, _ := ctx.Deadline(); !got.Equal(deadline) {
					t.Error("fallback replaced the original approval deadline")
				}
				return llm, nil
			}
			result, err := reviewer.Decide(parent, req)
			if err != nil || result.Approved != tc.wantAllow || (resolved == 1) != tc.wantAgent || (len(llm.Requests()) == 1) != tc.wantAgent {
				t.Fatalf("result=%+v err=%v resolved=%d calls=%d", result, err, resolved, len(llm.Requests()))
			}
			if !tc.wantAllow && ((tc.wantAgent && result.Rationale == "") || (!tc.wantAgent && (result.Rationale != "" || result.DisplayText != "denied"))) {
				t.Fatalf("unexpected denial explanation: %+v", result)
			}
			if tc.wantAgent {
				raw, _ := json.Marshal(llm.Requests())
				if strings.Contains(string(raw), "screen-only") || !strings.Contains(string(raw), "Do not delete files") || !strings.Contains(string(raw), command) || !strings.Contains(string(raw), "INDEPENDENT_TOOL_EVIDENCE") {
					t.Fatal("Agent prompt lost canonical evidence or included classifier output")
				}
			}
			if !tc.wantAgent {
				for _, resident := range reviewer.residents {
					for range len(resident.lanes) {
						lane := <-resident.lanes
						if lane != nil && lane.runtime != nil {
							t.Fatal("direct screening initialized a query sandbox")
						}
						resident.lanes <- lane
					}
				}
			}
		})
	}
}

func TestGuardianScreeningOptionEligibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []approval.Option
		wantErr bool
	}{
		{"legal aliases skip classifier", []approval.Option{{ID: "allow_once", Kind: "allow"}, {ID: "reject_once", Kind: "deny"}}, false},
		{"one outcome skips classifier", []approval.Option{{ID: "allow_once", Kind: "allow_once"}}, false},
		{"missing ID fails protocol", []approval.Option{{Kind: "allow_once"}, {ID: "reject_once", Kind: "reject_once"}}, true},
		{"duplicate ID fails protocol", []approval.Option{{ID: "allow_once", Kind: "allow_once"}, {ID: "allow_once", Kind: "reject_once"}}, true},
		{"custom kind fails protocol", []approval.Option{{ID: "allow_once", Name: "Approve", Kind: "custom"}, {ID: "reject_once", Kind: "reject_once"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, active := newApprovalReviewerTestSession(t, t.Context())
			reviewer := newGuardianApprovalApprover(service)
			defer reviewer.Close()
			llm := &approvalReviewerFakeModel{}
			req := approvalReviewerTestRequest(active, llm, "inspect", map[string]any{"cmd": "rg TODO ."})
			req.Approval.Options = tc.options
			req.Judgment = judgmentFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
				t.Error("ineligible options sent to classifier")
				return judgment.Response{}, nil
			})
			result, err := reviewer.Decide(t.Context(), req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.wantErr {
				if len(llm.Requests()) != 0 {
					t.Fatal("protocol-invalid request reached Agent")
				}
			} else if !result.Approved || len(llm.Requests()) != 1 {
				t.Fatalf("valid options did not settle through Agent: %+v", result)
			}
		})
	}
}

func TestGuardianScreeningCancellationNeverStartsAgent(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	reviewer := newGuardianApprovalApprover(service)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := approvalReviewerTestRequest(active, nil, "inspect", map[string]any{"cmd": "rg TODO ."})
	req.Judgment = judgmentFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
		cancel()
		return judgment.Response{}, context.Canceled
	})
	resolved := false
	req.ResolveModel = func(context.Context) (model.LLM, error) {
		resolved = true
		return &approvalReviewerFakeModel{}, nil
	}
	_, err := reviewer.Decide(ctx, req)
	if closeErr := reviewer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !errors.Is(err, context.Canceled) || resolved {
		t.Fatalf("cancelled review started Agent: resolved=%v err=%v", resolved, err)
	}
}

func TestGuardianScreeningOversizedEvidenceUsesAgentBudget(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Inspect these identifiers: "+strings.Repeat("identifier ", 2400))
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	llm := &approvalReviewerFakeModel{contextWindowTokens: 128000}
	req := approvalReviewerTestRequest(active, llm, "inspect", map[string]any{"cmd": "rg TODO ."})
	req.Judgment = judgmentFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
		t.Error("oversized evidence sent to classifier")
		return judgment.Response{}, nil
	})
	result, err := reviewer.Decide(t.Context(), req)
	if err != nil || !result.Approved || len(llm.Requests()) != 1 {
		t.Fatalf("Agent fallback result=%+v err=%v calls=%d", result, err, len(llm.Requests()))
	}
}

func TestGuardianScreeningAndAgentReceiptsSurviveReopen(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{}}
	reviewer := newGuardianApprovalApprover(store)
	defer reviewer.Close()
	req := approvalReviewerTestRequest(active, llm, "inspect", map[string]any{"cmd": "echo approved"})
	req.Judgment = judgmentFunc(func(context.Context, judgment.Request) (judgment.Response, error) {
		return judgment.Response{Model: "screen", Answers: guardianScreenAnswers("allow_once", .5, 0, 0), Usage: judgment.Usage{InputTokens: 100}}, nil
	})
	result, err := reviewer.Decide(t.Context(), req)
	if err != nil || !result.Approved {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	count, total := 0, 0
	for _, event := range events {
		if !session.IsModelInvocationReceipt(event) {
			continue
		}
		count++
		usage := session.UsageSnapshotFromSessionEvent(event)
		if usage == nil || event.Meta["review_id"] != req.ReviewID || event.Scope.Executor.ID != "guardian" {
			t.Fatalf("invalid cascade receipt: %+v", event)
		}
		total += usage.TotalTokens
	}
	if count != 2 || total != 112 {
		t.Fatalf("receipts=%d tokens=%d", count, total)
	}
	if usage, _, err := reviewer.ApprovalReviewAccounting(t.Context(), req, result); err != nil || usage != nil {
		t.Fatalf("duplicate legacy accounting: %+v %v", usage, err)
	}
}
