package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type guardianFaultRuntime struct {
	sandbox.Runtime
	fault string
}

func (r guardianFaultRuntime) Run(ctx context.Context, _ sandbox.CommandRequest) (sandbox.CommandResult, error) {
	switch r.fault {
	case "timeout":
		return sandbox.CommandResult{Stdout: "partial evidence", ExitCode: -1}, context.DeadlineExceeded
	case "large":
		return sandbox.CommandResult{Stdout: strings.Repeat("bounded evidence\n", 65536), ExitCode: 0}, nil
	default:
		return sandbox.CommandResult{Stdout: "partial evidence", ExitCode: 1}, errors.New("evidence execution unavailable")
	}
}

type guardianFaultDecisionModel struct {
	approvalReviewerFakeModel
	fault string
	allow bool
	calls int
	seen  []*model.Request
}

func (m *guardianFaultDecisionModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	m.seen = append(m.seen, model.CloneRequest(req))
	step := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if step%2 == 1 {
			message := model.NewMessage(model.RoleAssistant, model.NewToolUsePart(fmt.Sprintf("evidence-%d", step), "RunCommand", json.RawMessage(`{"command":"inspect fixture"}`)))
			yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, StepComplete: true}), nil)
			return
		}
		found := false
		for _, message := range req.Messages {
			for _, part := range message.Parts {
				if part.ToolResult == nil || part.ToolResult.ToolUseID != fmt.Sprintf("evidence-%d", step-1) {
					continue
				}
				found = true
				raw, _ := json.Marshal(part.ToolResult)
				if len(raw) > 40*1024 {
					yield(nil, fmt.Errorf("unbounded evidence reached model: %d", len(raw)))
					return
				}
				if m.fault != "large" && !part.ToolResult.IsError {
					yield(nil, errors.New("failed evidence lost error status"))
					return
				}
				if m.fault == "large" && !strings.Contains(string(raw), "truncated") {
					yield(nil, errors.New("large output lost truncation fact"))
					return
				}
			}
		}
		if !found {
			yield(nil, errors.New("final model call did not receive evidence result"))
			return
		}
		text := `{"option_id":"reject_once","rationale":"The action contradicts the explicit task constraint."}`
		if m.allow {
			text = `{"option_id":"allow_once"}`
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, text), TurnComplete: true, StepComplete: true}), nil)
	}
}

// Exercise the actual SDK tool loop, continuation, resident history and final
// option validation with failing execution, rather than mocking the reviewer.
func TestGuardianResidentDecidesAfterEvidenceFailures(t *testing.T) {
	for _, fault := range []string{"timeout", "execution", "large"} {
		for _, allow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allow=%v", fault, allow), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					service, active := newApprovalReviewerTestSession(t, t.Context())
					reviewer := newGuardianApprovalApprover(service)
					defer reviewer.Close()
					_, q, release, err := reviewer.acquireResident(t.Context(), active.SessionRef)
					if err != nil {
						t.Fatal(err)
					}
					native, err := host.New(host.Config{CWD: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					q.runtime = guardianFaultRuntime{Runtime: native, fault: fault}
					q.scratch = t.TempDir()
					release()
					llm := &guardianFaultDecisionModel{fault: fault, allow: allow}
					var metrics []guardianReviewMetrics
					reviewer.observeReview = func(m guardianReviewMetrics) { metrics = append(metrics, m) }
					for i := 0; i < 2; i++ {
						req := approvalReviewerTestRequest(active, llm, "inspect", nil)
						req.ReviewID = fmt.Sprintf("review-%d", i)
						result, err := reviewer.Decide(t.Context(), req)
						if err != nil || result.Approved != allow {
							t.Fatalf("review=%d result=%+v error=%v", i, result, err)
						}
					}
					if llm.calls != 4 || len(metrics) != 2 {
						t.Fatalf("calls=%d metrics=%+v", llm.calls, metrics)
					}
					if !metrics[1].RuntimeReused || !metrics[1].SandboxReused {
						t.Fatalf("resident resources not reused: %+v", metrics)
					}
					for _, m := range metrics {
						if m.ModelCalls != 2 || m.ToolCalls != 1 || m.TotalMS > kernel.AutoReviewTimeout.Milliseconds() {
							t.Fatalf("review budget/counts: %+v", m)
						}
						if fault != "large" && m.ToolErrors != 1 {
							t.Fatalf("missing tool error metric: %+v", m)
						}
					}
				})
			})
		}
	}
}

func TestGuardianResidentCloseCancelsQueuedAndActiveReview(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	reviewer := newGuardianApprovalApprover(service)
	resident, _, release1, err := reviewer.acquireResident(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	_, _, release2, err := reviewer.acquireResident(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	var others []func()
	for i := 2; i < guardianResidentLaneCount; i++ {
		_, _, release, err := reviewer.acquireResident(t.Context(), active.SessionRef)
		if err != nil {
			t.Fatal(err)
		}
		others = append(others, release)
	}
	queued := make(chan error, 1)
	go func() {
		_, _, release, err := reviewer.acquireResident(t.Context(), active.SessionRef)
		if release != nil {
			release()
		}
		queued <- err
	}()
	closed := make(chan error, 1)
	go func() { closed <- reviewer.Close() }()
	select {
	case <-resident.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel active leases")
	}
	release1()
	release2()
	for _, release := range others {
		release()
	}
	if err := <-queued; err == nil {
		t.Fatal("closed Guardian admitted queued work")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reviewer.acquireResident(t.Context(), active.SessionRef); err == nil {
		t.Fatal("closed reviewer reopened")
	}
}

func TestGuardianProjectionIncludesLateResultAndIgnoresPendingAction(t *testing.T) {
	call := guardianSource(1, session.EventTypeToolCall, "run verification")
	result := guardianSource(2, session.EventTypeToolResult, "")
	result.Tool.ID = call.Tool.ID
	result.Tool.Output = map[string]any{"exit_code": 1, "stderr": "Sandbox denied the operation", "stdout": strings.Repeat("large result", 32768)}
	one := guardianProjectEvent(result)
	if text := session.EventText(one); !strings.Contains(text, "Sandbox denied") || !strings.Contains(text, "folded") || len(text) > 16*1024 {
		t.Fatalf("result evidence=%s", text)
	}
	first := guardianWindowRequest(t, "first")
	a, items, err := guardianWindow(guardianConversationSnapshot{ParentEvents: []*session.Event{call}}, first, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.ReviewID = "second"
	second.RuntimeRequest.Call.ID = call.Tool.ID
	b, _, err := guardianWindow(guardianConversationSnapshot{Events: a, ParentCursor: items.ParentCursor, ParentEvents: []*session.Event{call, result}}, second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 2 || session.EventText(b[0]) != session.EventText(a[0]) || session.EventText(b[1]) != session.EventText(one) {
		t.Fatal("late result rewrote call or depended on pending approval")
	}
}
