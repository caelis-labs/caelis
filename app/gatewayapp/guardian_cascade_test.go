package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestGuardianScreeningCascadesToAgentWithinOriginalReview(t *testing.T) {
	for _, tc := range []struct {
		name, decision, reason string
		confidence             float64
		failure                error
		agentResponse          string
		wantAgent, wantAllow   bool
	}{
		{name: "allow without resolving Agent", decision: "0", reason: "none", confidence: 1, wantAllow: true},
		{name: "deny without resolving Agent", decision: "1", reason: "constraint", confidence: 1},
		{name: "contradictory allow defers to Agent allow", decision: "0", reason: "constraint", confidence: 1, wantAgent: true, wantAllow: true},
		{name: "contradictory allow defers to Agent deny", decision: "0", reason: "constraint", confidence: 1, agentResponse: `{"option_id":"reject_once","rationale":"The action contradicts the user's explicit constraint."}`, wantAgent: true},
		{name: "uncertain", decision: "0", confidence: .5, wantAgent: true, wantAllow: true},
		{name: "missing evidence", decision: "unavailable", confidence: 1, wantAgent: true, wantAllow: true},
		{name: "invalid answer", decision: "unknown", confidence: 1, wantAgent: true, wantAllow: true},
		{name: "unexplained denial", decision: "1", reason: "none", confidence: 1, wantAgent: true, wantAllow: true},
		{name: "provider failure", failure: errors.New("screen-only-provider-error"), wantAgent: true, wantAllow: true},
		{name: "screening timeout", failure: context.DeadlineExceeded, wantAgent: true, wantAllow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, active := newApprovalReviewerTestSession(t, t.Context())
			appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Inspect the project. Do not delete files.")
			reviewer := newGuardianApprovalApprover(service)
			defer reviewer.Close()
			llm := &approvalReviewerFakeModel{}
			if tc.agentResponse != "" {
				llm.responses = []string{tc.agentResponse}
			}
			req := approvalReviewerTestRequest(active, nil, "inspect", map[string]any{"cmd": "rg TODO ."})
			parent, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			deadline, _ := parent.Deadline()
			screened, resolved := 0, 0
			req.Judgment = judgmentFunc(func(ctx context.Context, _ judgment.Request) (judgment.Response, error) {
				screened++
				if limit, ok := ctx.Deadline(); !ok || time.Until(limit) > 10*time.Second {
					t.Error("screening omitted its bounded deadline")
				}
				return judgment.Response{Model: "screen-only-provider", Answers: map[string]judgment.Answer{
					"decision": choiceAnswer(tc.decision, tc.confidence), "reason": choiceAnswer(tc.reason, 1), "source": choiceAnswer("0", 1),
				}}, tc.failure
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
			if tc.wantAgent {
				raw, _ := json.Marshal(llm.Requests())
				if strings.Contains(string(raw), "screen-only") || !strings.Contains(string(raw), "Do not delete files") || !strings.Contains(string(raw), "rg TODO") {
					t.Fatal("Agent prompt lost canonical evidence or included classifier output")
				}
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
		return judgment.Response{Model: "screen", Answers: map[string]judgment.Answer{"decision": choiceAnswer("unavailable", 1)}, Usage: judgment.Usage{InputTokens: 100}}, nil
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
