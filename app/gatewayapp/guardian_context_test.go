package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
)

type guardianCommandResultRuntime struct {
	sandbox.Runtime
	result sandbox.CommandResult
}

func (r guardianCommandResultRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return r.result, nil
}

func TestGuardianDialogueSuppliesProposalBeforeReplyWithoutToolOrReasoning(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "修好这个问题。")
	proposal := "请允许推送当前分支并更新 PR。"
	message := model.NewMessage(model.RoleAssistant,
		model.NewTextPart(proposal),
		model.NewReasoningMessage(model.RoleAssistant, "hidden reasoning", model.ReasoningVisibilityVisible).Parts[0],
		model.NewToolUsePart("call-1", "RunCommand", json.RawMessage(`{"command":"private tool argument"}`)),
	)
	if _, err := service.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type: session.EventTypeToolCall, Visibility: session.VisibilityCanonical, Message: &message,
		Tool: &session.EventTool{ID: "call-1", Name: "RunCommand", Input: map[string]any{"command": "private tool argument"}},
	}}); err != nil {
		t.Fatal(err)
	}
	appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "照这个办。")
	llm := &approvalReviewerFakeModel{}
	req := approvalReviewerTestRequest(active, llm, "Update PR", map[string]any{"command": "git push origin HEAD"})
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	if _, err := reviewer.Decide(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	requests := llm.Requests()
	if len(requests) != 1 || len(requests[0].Messages) != 4 || !strings.Contains(requests[0].Messages[1].TextContent(), proposal) ||
		!strings.Contains(requests[0].Messages[2].TextContent(), "照这个办。") {
		t.Fatalf("proposal and user reply did not reach Agent review in source order: %v", requests)
	}
	for _, part := range requests[0].Messages[1].Parts {
		if part.Text == nil || strings.Contains(part.Text.Text, "hidden reasoning") || strings.Contains(part.Text.Text, "private tool argument") {
			t.Fatal("proposal imported non-dialogue content")
		}
	}
	var projection guardianProjection
	events, _, err := projection.read(t.Context(), service, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	screen, err := guardianScreenState(req, events)
	if err != nil {
		t.Fatal(err)
	}
	dialogue := screen["conversation"].([]guardianDialogueMessage)
	if len(dialogue) != 3 || dialogue[1].Role != model.RoleAssistant || dialogue[1].Text != requests[0].Messages[1].TextContent() ||
		dialogue[2].Role != model.RoleUser || dialogue[2].Text != requests[0].Messages[2].TextContent() {
		t.Fatalf("Jev did not see the same ordered proposal and reply: %+v", dialogue)
	}
}

func TestGuardianHarnessOnlyProjectsUserAuthorityWithoutQueries(t *testing.T) {
	for _, fileBacked := range []bool{false, true} {
		t.Run(fmt.Sprintf("file=%v", fileBacked), func(t *testing.T) {
			service, active := newApprovalReviewerTestSession(t, t.Context())
			if fileBacked {
				service = sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
				var err error
				active, err = service.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
				if err != nil {
					t.Fatal(err)
				}
			}
			appendEvent := func(e *session.Event) {
				t.Helper()
				e.ID, e.Seq = "", 0
				if _, err := service.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: e}); err != nil {
					t.Fatal(err)
				}
			}
			appendEvent(guardianSource(0, session.EventTypeUser, "Inspect the workspace; preserve the original ledger."))
			command := "inspect-workspace | head -10"
			call := guardianSource(0, session.EventTypeToolCall, command)
			appendEvent(call)
			llm := &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`, `{"option_id":"allow_once"}`}}
			reviewer := newGuardianApprovalApprover(service)
			defer reviewer.Close()
			req := approvalReviewerTestRequest(active, llm, "inspect", map[string]any{"command": "inspect-current"})
			if _, err := reviewer.Decide(t.Context(), req); err != nil {
				t.Fatal(err)
			}

			// Use the actual shell tool's canonical result shape. The shell's
			// final pipeline status can be zero despite an upstream diagnostic.
			base, err := host.New(host.Config{CWD: active.CWD})
			if err != nil {
				t.Fatal(err)
			}
			defer base.Close()
			runner, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: guardianCommandResultRuntime{Runtime: base, result: sandbox.CommandResult{Stdout: "partial workspace listing\n", Stderr: "upstream inspection: Permission denied\n", ExitCode: 0}}})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"command": command})
			result, err := runner.Call(t.Context(), tool.Call{ID: call.Tool.ID, Name: "RunCommand", Input: raw})
			if err != nil || result.IsError {
				t.Fatalf("wrapper result=%+v err=%v", result, err)
			}
			var payload map[string]any
			if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
				t.Fatal(err)
			}
			late := guardianSource(0, session.EventTypeToolResult, "")
			late.Tool.ID, late.Tool.Status, late.Tool.Output = call.Tool.ID, "completed", payload
			appendEvent(late)
			appendEvent(guardianSource(0, session.EventTypeUser, "Use only generated files for subsequent changes."))
			req.ReviewID = "next-review"
			if _, err := reviewer.Decide(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			requests := llm.Requests()
			if len(requests) != 2 || len(requests[1].Messages) != 5 ||
				!reflect.DeepEqual(requests[0].Messages, requests[1].Messages[:len(requests[0].Messages)]) ||
				!strings.Contains(requests[1].Messages[3].TextContent(), "Use only generated files") {
				t.Fatal("ordinary approvals queried or changed the retained user and ticket")
			}
			var text strings.Builder
			for _, m := range requests[1].Messages {
				text.WriteString(m.TextContent())
				text.WriteByte('\n')
			}
			for _, want := range []string{"inspect-current", "preserve the original ledger", "Use only generated files"} {
				if !strings.Contains(text.String(), want) {
					t.Fatalf("Harness omitted %q: %s", want, text.String())
				}
			}
			for _, excluded := range []string{command, "partial workspace listing", "upstream inspection: Permission denied"} {
				if strings.Contains(text.String(), excluded) {
					t.Fatalf("prior operation %q entered the prompt", excluded)
				}
			}
			var names []string
			for _, spec := range requests[1].Tools {
				names = append(names, spec.Function.Name)
			}
			if !reflect.DeepEqual(names, []string{"Read", "Grep", "RunCommand"}) {
				t.Fatalf("unexpected tools: %v", names)
			}
			_, q, release, err := reviewer.acquireResident(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			release()
			if q.runtime != nil || q.calls != 0 {
				t.Fatal("ordinary approval initialized evidence sandbox")
			}
		})
	}
}

type guardianOverflowModel struct {
	approvalReviewerFakeModel
	path string
	step int
}

func (m *guardianOverflowModel) Generate(_ context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	step := m.step
	m.step++
	return func(yield func(*model.StreamEvent, error) bool) {
		if step == 0 {
			raw, _ := json.Marshal(map[string]any{"path": m.path})
			message := model.NewMessage(model.RoleAssistant, model.NewToolUsePart("inspect-before-overflow", "Read", raw))
			yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
			return
		}
		yield(nil, &model.ContextOverflowError{Cause: fmt.Errorf("provider context estimate correction")})
	}
}

func TestGuardianActiveOverflowIsUnavailableWithoutRepeatingTools(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Inspect the workspace; preserve protected.txt.")
	path := filepath.Join(active.CWD, "evidence.txt")
	if err := os.WriteFile(path, []byte("bounded observation"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	_, q, release, err := reviewer.acquireResident(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	q.runtime, err = host.New(host.Config{CWD: active.CWD})
	q.scratch = t.TempDir()
	release()
	if err != nil {
		t.Fatal(err)
	}
	llm := &guardianOverflowModel{path: path}
	result, err := reviewer.Decide(t.Context(), approvalReviewerTestRequest(active, llm, "inspect", nil))
	if err == nil || result.Approved || llm.step != 2 || q.calls != 1 {
		t.Fatalf("overflow result=%+v err=%v model calls=%d tool calls=%d", result, err, llm.step, q.calls)
	}
	snapshot, err := reviewer.conversations.snapshot(active.SessionID)
	if err != nil || len(snapshot.Events) != 0 {
		t.Fatalf("failed review entered reusable context: %+v %v", snapshot, err)
	}
}
