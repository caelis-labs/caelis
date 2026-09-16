package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

type commandApprovalModel struct {
	generate func(context.Context, *model.Request) model.Message
}

func (*commandApprovalModel) Name() string                     { return "command-approval" }
func (*commandApprovalModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (m *commandApprovalModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		message := m.generate(ctx, req)
		reason := model.FinishReasonStop
		if len(message.ToolCalls()) != 0 {
			reason = model.FinishReasonToolCalls
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Message: message, TurnComplete: true, StepComplete: true,
			Status: model.ResponseStatusCompleted, FinishReason: reason,
		}}, nil)
	}
}

func TestCommandApprovalYieldsWithinRunAndPreservesCanonicalHistory(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{Sessions: sessions, TaskStore: sessionfile.NewTaskStore(sessions), AgentFactory: chat.Factory{}, DefaultPolicyMode: presets.ModeAutoReview})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(active.CWD, "approved.txt")
	approvalEntered := make(chan agent.ApprovalRequest, 1)
	approve := make(chan struct{})
	continued := make(chan *model.Request, 1)
	continueModel := make(chan struct{})
	var approvalCount atomic.Int32
	requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approvalCount.Add(1)
		if req.OnAdmission == nil || req.OperationTaskID == "" {
			return agent.ApprovalResponse{}, context.Canceled
		}
		budget, end := context.WithTimeout(ctx, 90*time.Second)
		defer end()
		if err := req.OnAdmission(budget); err != nil {
			return agent.ApprovalResponse{}, err
		}
		approvalEntered <- req
		select {
		case <-approve:
			return agent.ApprovalResponse{Approved: true, Outcome: "selected", OptionID: "allow_once", ReviewText: "approved"}, nil
		case <-ctx.Done():
			return agent.ApprovalResponse{}, ctx.Err()
		}
	})
	step := 0
	handle := ""
	m := &commandApprovalModel{generate: func(ctx context.Context, req *model.Request) model.Message {
		step++
		if step == 1 {
			return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "pending-command", Name: shell.RunCommandToolName, Args: string(mustJSONRaw(map[string]any{
				"command": shellWriteFileForTest(target, "approved"), "workdir": active.CWD,
				"yield_time_ms": 0, "sandbox_permissions": "require_escalated", "justification": "Write the requested file",
			}))}}, "")
		}
		if step == 2 {
			handle = mustFindTaskHandle(t, req)
			continued <- req
			select {
			case <-continueModel:
			case <-ctx.Done():
			}
		} else if mustFindTaskState(t, req, handle) == taskapi.StateCompleted {
			return model.NewTextMessage(model.RoleAssistant, "done")
		}
		return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "wait-" + time.Now().Format("150405.000000000"), Name: tasktool.ToolName, Args: string(mustJSONRaw(map[string]any{"action": "wait", "handle": handle}))}}, "")
	}}
	run, err := r.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "Write approved.txt", ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{Name: "chat", Model: m, Tools: []tool.Tool{mustRuntimeRunCommandTool(t, hostRuntimeForTest(t, active.CWD)), tasktool.New()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Handle.Cancel()
	var approval agent.ApprovalRequest
	select {
	case approval = <-approvalEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var pendingRequest *model.Request
	select {
	case pendingRequest = <-continued:
	case <-ctx.Done():
		t.Fatal("model did not continue while approval was pending")
	}
	if state := mustFindTaskState(t, pendingRequest, mustFindTaskHandle(t, pendingRequest)); state != taskapi.StateWaitingApproval {
		t.Fatalf("state = %s", state)
	}
	state, err := r.RunState(ctx, active.SessionRef)
	if err != nil || state.Status != agent.RunLifecycleStatusRunning || state.WaitingApproval {
		t.Fatalf("Run state = %+v, %v", state, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("command started before approval: %v", err)
	}
	entry, err := r.tasks.store.Get(ctx, approval.OperationTaskID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := commandExecutionFromEntry(entry)
	if err != nil || spec.Approval.TokenID != approval.PauseTokenID || spec.Approval.Deadline.IsZero() || entry.Handle != mustFindTaskHandle(t, pendingRequest) {
		t.Fatalf("durable intent = %#v, %#v, %v", entry, spec, err)
	}
	close(approve)
	close(continueModel)
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		events, _ := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
		for _, event := range events {
			if event.Tool != nil {
				t.Logf("tool: %+v", event.Tool)
			}
		}
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "approved" {
		t.Fatalf("effect = %q, %v", data, err)
	}
	if approvalCount.Load() != 1 {
		t.Fatalf("approvals = %d", approvalCount.Load())
	}
	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	results, terminalReceipts, reviewed := 0, 0, 0
	for _, event := range events {
		if token := session.ResolvedApprovalReview(event); token != nil {
			reviewed++
			if !session.IsClientReplayEvent(event) || session.IsCanonicalHistoryEvent(event) || token.ToolCallID != "pending-command" {
				t.Fatalf("review visibility or identity changed: %#v", event)
			}
		}
		if event.Tool != nil && event.Tool.ID == "pending-command" && session.EventTypeOf(event) == session.EventTypeToolResult {
			results++
		}
		if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.Key.ToolCallID == "pending-command" && event.Journal.ToolExecution.Status == session.ToolExecutionSucceeded {
			terminalReceipts++
		}
	}
	if results != 1 || terminalReceipts != 1 || reviewed != 1 {
		t.Fatalf("canonical results/terminal receipts/reviews = %d/%d/%d", results, terminalReceipts, reviewed)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	reviewed = 0
	var after uint64
	for {
		page, err := reopened.EventsPage(ctx, session.EventPageRequest{SessionRef: active.SessionRef, AfterSeq: after, Visibility: session.EventPageClientReplay})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Events {
			if token := session.ResolvedApprovalReview(event); token != nil {
				reviewed++
				if !token.Approved || token.ToolCallID != "pending-command" {
					t.Fatalf("reopened decision = %#v", token)
				}
			}
		}
		if !page.HasMore {
			break
		}
		after = page.NextSeq
	}
	if reviewed != 1 {
		t.Fatalf("reopened client replay reviews = %d", reviewed)
	}
	assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
}

func TestCommandApprovalUsesOneInlineBudget(t *testing.T) {
	for _, delay := range []time.Duration{3 * time.Second, 25 * time.Second, 60 * time.Second, 89 * time.Second, 91 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, active, r := newRuntimeRunCommandToolTestHarness(t)
				r.tasks.store = newSagaTaskStore()
				running := false
				backend := newCommandStartProbe(&yieldProbeSandboxSession{statusRunning: &running}, nil)
				target := runtimeCommandTool{base: mustRuntimeRunCommandTool(t, backend), session: active, sessionRef: active.SessionRef, tasks: r.tasks}
				scope := newTaskContinuationScope()
				owner := context.WithValue(t.Context(), taskScopeKey{}, scope)
				call := tool.Call{ID: "budget", Name: shell.RunCommandToolName, Input: mustJSONRaw(map[string]any{"command": "echo ok", "workdir": active.CWD})}
				start := time.Now()
				result, err := target.submitApproval(owner, call, taskApproval{owner: owner, started: start, request: agent.ApprovalRequest{RunID: "run", TurnID: "turn"}, resolve: func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					budget, cancel := context.WithTimeout(ctx, 90*time.Second)
					defer cancel()
					if err := req.OnAdmission(budget); err != nil {
						return agent.ApprovalResponse{}, err
					}
					time.Sleep(delay)
					return agent.ApprovalResponse{Approved: true}, nil
				}}, target.Call)
				if err != nil {
					t.Fatal(err)
				}
				if elapsed := time.Since(start); elapsed != min(delay, 10*time.Second) {
					t.Fatalf("inline time = %s, approval = %s", elapsed, delay)
				}
				var payload map[string]any
				if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
					t.Fatal(err)
				}
				if delay > 10*time.Second && payload["state"] != string(taskapi.StateWaitingApproval) {
					t.Fatalf("payload = %#v", payload)
				}
				if err := scope.join(); err != nil {
					t.Fatal(err)
				}
				wantStarts := 1
				if delay >= 90*time.Second {
					wantStarts = 0
				}
				if backend.startCalls() != wantStarts {
					t.Fatalf("starts = %d", backend.startCalls())
				}
				entry, err := r.tasks.store.Get(owner, mustCommandTaskID(t, active.SessionRef, call.ID))
				if err != nil {
					t.Fatal(err)
				}
				spec, err := commandExecutionFromEntry(entry)
				if err != nil || !spec.Approval.Deadline.Equal(start.Add(90*time.Second)) {
					t.Fatalf("deadline = %#v, %v", spec, err)
				}
				if wantStarts == 0 && (entry.State != taskapi.StateFailed || entry.Result["error_code"] != "approval_expired") {
					t.Fatalf("expired approval = %#v", entry)
				}
			})
		})
	}
}

func mustCommandTaskID(t *testing.T, ref session.SessionRef, callID string) string {
	t.Helper()
	id, err := commandTaskID(ref, callID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCommandExecutionSpecRoundTrip(t *testing.T) {
	req := taskapi.CommandStartRequest{Timeout: 123 * time.Second, Constraints: sandbox.Constraints{Route: sandbox.RouteHost, Backend: sandbox.BackendHost, Permission: sandbox.PermissionFullAccess}}
	spec := commandExecutionForRequest(req)
	spec.Approval = &commandApprovalRef{RunID: "r", TurnID: "t", TokenID: "p", Deadline: time.Now().UTC().Truncate(time.Second)}
	raw, err := json.Marshal(&taskapi.Entry{Spec: map[string]any{"execution": spec.value()}})
	if err != nil {
		t.Fatal(err)
	}
	var entry taskapi.Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	got, err := commandExecutionFromEntry(&entry)
	if err != nil || !reflect.DeepEqual(got, spec) {
		t.Fatalf("round trip = %#v, %v; want %#v", got, err, spec)
	}
}

func TestCommandApprovalRevocationBeforeEffectClaim(t *testing.T) {
	for _, action := range []string{"cancel", "steer", "task_cancel"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, active, r := newRuntimeRunCommandToolTestHarness(t)
				r.tasks.store = newSagaTaskStore()
				backend := newCommandStartProbe(newYieldProbeSandboxSession(), nil)
				target := runtimeCommandTool{base: mustRuntimeRunCommandTool(t, backend), session: active, sessionRef: active.SessionRef, tasks: r.tasks}
				scope := newTaskContinuationScope()
				owner := context.WithValue(t.Context(), taskScopeKey{}, scope)
				observer, detach := context.WithCancel(owner)
				call := tool.Call{ID: "revoked", Name: shell.RunCommandToolName, Input: mustJSONRaw(map[string]any{"command": "echo ok", "yield_time_ms": 0})}
				approved := make(chan struct{})
				start := make(chan struct{})
				defer func() {
					scope.revoke(context.Canceled, true)
					select {
					case <-start:
					default:
						close(start)
					}
					_ = scope.join()
				}()
				result, err := target.submitApproval(observer, call, taskApproval{owner: owner, request: agent.ApprovalRequest{RunID: "run", TurnID: "turn"}, resolve: func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					if err := req.OnAdmission(ctx); err != nil {
						return agent.ApprovalResponse{}, err
					}
					return agent.ApprovalResponse{Approved: true}, nil
				}}, func(ctx context.Context, call tool.Call) (tool.Result, error) {
					close(approved)
					<-start
					return target.Call(ctx, call)
				})
				if err != nil || result.IsError {
					t.Fatalf("submit = %#v, %v", result, err)
				}
				<-approved
				detach()
				synctest.Wait()
				id := mustCommandTaskID(t, active.SessionRef, call.ID)
				pending, err := r.tasks.Read(owner, active.SessionRef, taskapi.ControlRequest{TaskID: id, Principal: session.ActorKindTool})
				if err != nil || pending.State != taskapi.StateWaitingApproval || pending.SupportsInput {
					t.Fatalf("pending read = %#v, %v", pending, err)
				}
				terminalSnapshot, err := r.terminals.Read(owner, terminal.Ref{SessionID: active.SessionID, TaskID: id})
				if err != nil || terminalSnapshot.State != "waiting_approval" || !terminalSnapshot.Running || !terminalSnapshot.StartedAt.IsZero() {
					t.Fatalf("pending terminal fallback = %#v, %v", terminalSnapshot, err)
				}
				if _, err := r.tasks.Write(owner, active.SessionRef, taskapi.ControlRequest{TaskID: id, Principal: session.ActorKindTool, Input: "premature"}); err == nil {
					t.Fatal("pending command accepted stdin")
				}
				duplicate, err := target.submitApproval(owner, call, taskApproval{owner: owner}, nil)
				if err != nil || duplicate.IsError {
					t.Fatalf("duplicate submission = %#v, %v", duplicate, err)
				}
				work := scope.active[id]
				if work.ctx.Err() != nil {
					t.Fatal("tool observer cancellation revoked the owner")
				}
				switch action {
				case "cancel":
					scope.revoke(context.Canceled, true)
				case "steer":
					scope.revoke(errTaskAuthorizationChanged, false)
				case "task_cancel":
					task, err := r.tasks.lookupCommandCanonical(owner, active.SessionRef, id)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := r.tasks.cancelCommandClaimed(owner, task); err != nil {
						t.Fatal(err)
					}
				}
				joined := make(chan error, 1)
				go func() { joined <- scope.join() }()
				synctest.Wait()
				select {
				case err := <-joined:
					t.Fatalf("owner exited before continuation: %v", err)
				default:
				}
				close(start)
				if err := <-joined; err != nil {
					t.Fatal(err)
				}
				if backend.startCalls() != 0 {
					t.Fatal("revoked command was started")
				}
				entry, err := r.tasks.store.Get(owner, id)
				if err != nil || entry.State != taskapi.StateCancelled || entry.Running {
					t.Fatalf("settled = %#v, %v", entry, err)
				}
			})
		})
	}
}

func TestCommandApprovalRecoveryNeverRestarts(t *testing.T) {
	t.Parallel()
	_, active, r := newRuntimeRunCommandToolTestHarness(t)
	r.tasks.store = newFileTaskStoreForTest(t)
	req := taskapi.CommandStartRequest{Command: "echo recovered", Workdir: active.CWD, ParentCall: "lost-owner"}
	id := mustCommandTaskID(t, active.SessionRef, req.ParentCall)
	digest, err := commandRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	spec := commandExecutionForRequest(req)
	spec.Approval = &commandApprovalRef{RunID: "run-lost", TurnID: "turn-lost", TokenID: "pause-lost"}
	prepared, _, err := r.tasks.prepareCommandClaimed(t.Context(), active, active.SessionRef, id, digest, req, spec)
	if err != nil {
		t.Fatal(err)
	}
	r.tasks = newTaskRuntime(r, r.tasks.store)
	if err := r.recoverRuntimeState(t.Context(), active.SessionRef); err != nil {
		t.Fatal(err)
	}
	entry, err := r.tasks.store.Get(t.Context(), id)
	if err != nil || entry.Running || entry.State != taskapi.StateInterrupted || entry.Handle != prepared.handle {
		t.Fatalf("recovery = %#v, %v", entry, err)
	}
	backend := newCommandStartProbe(newYieldProbeSandboxSession(), nil)
	result, err := r.tasks.StartCommand(t.Context(), active, active.SessionRef, backend, req)
	if err != nil || result.State != taskapi.StateInterrupted || backend.startCalls() != 0 {
		t.Fatalf("retry = %#v, %v, starts=%d", result, err, backend.startCalls())
	}
	if err := r.recoverRuntimeState(t.Context(), active.SessionRef); err != nil {
		t.Fatal(err)
	}
	latest, _ := r.tasks.store.Get(t.Context(), id)
	if latest.Revision != entry.Revision {
		t.Fatal("recovery repeated a terminal mutation")
	}
	stale := &session.Event{Type: session.EventTypeToolResult, Time: time.Now().Add(time.Minute), Tool: &session.EventTool{ID: req.ParentCall, Name: shell.RunCommandToolName, Output: map[string]any{"handle": entry.Handle, "state": "waiting_approval"}}, Meta: trustedTaskResultMeta(taskToolMeta(prepared.snapshotWithoutSession(time.Now())))}
	if err := r.tasks.syncCanonicalToolResult(t.Context(), active.SessionRef, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sessions.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: stale}); err != nil {
		t.Fatal(err)
	}
	backfilled, err := r.tasks.backfillCanonicalTaskEntry(t.Context(), active.SessionRef, latest)
	if err != nil || backfilled.State != taskapi.StateInterrupted || backfilled.Revision != latest.Revision {
		t.Fatalf("delayed pending result regressed recovery: %#v, %v", backfilled, err)
	}
}

func TestTaskContinuationScopeBoundsAdmissionAndSerializesClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scope := newTaskContinuationScope()
		for i := range maxTaskContinuations {
			if _, err := scope.register(t.Context(), string(rune('a'+i)), 0); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := scope.register(t.Context(), "overflow", 0); err == nil {
			t.Fatal("unbounded admission")
		}
		work := scope.active["a"]
		committing := make(chan struct{})
		commit := make(chan struct{})
		claimed := make(chan error, 1)
		go func() { claimed <- work.claim(func() error { close(committing); <-commit; return nil }) }()
		<-committing
		revoked := make(chan struct{})
		go func() { scope.revoke(context.Canceled, true); close(revoked) }()
		if work.mu.TryLock() {
			work.mu.Unlock()
			t.Fatal("effect commit did not retain its revocation lock")
		}
		select {
		case <-revoked:
			t.Fatal("revocation raced past the durable claim")
		default:
		}
		close(commit)
		if err := <-claimed; err != nil {
			t.Fatal(err)
		}
		<-revoked
		if work.ctx.Err() != nil {
			t.Fatal("claimed producer lost its execution context")
		}
		for id, active := range scope.active {
			scope.complete(id, active, nil)
		}
		if err := scope.join(); err != nil {
			t.Fatal(err)
		}
		if _, err := scope.register(t.Context(), "closed", 0); err == nil {
			t.Fatal("closed owner admitted work")
		}
		if err := work.claim(func() error { return nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("completed claim = %v", err)
		}
	})
}

func TestCommandApprovalFinalSafePointJoinsAndAcceptsSteering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sessions, active, r := newRuntimeRunCommandToolTestHarness(t)
		r.tasks.store = newSagaTaskStore()
		backend := newCommandStartProbe(newYieldProbeSandboxSession(), nil)
		entered := make(chan context.Context, 1)
		release := make(chan struct{})
		final := make(chan struct{}, 1)
		calls := 0
		m := &commandApprovalModel{generate: func(_ context.Context, _ *model.Request) model.Message {
			calls++
			if calls == 1 {
				return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "final-pending", Name: shell.RunCommandToolName, Args: string(mustJSONRaw(map[string]any{"command": "echo ok", "yield_time_ms": 0, "sandbox_permissions": "require_escalated", "justification": "Requested command"}))}}, "")
			}
			if calls == 2 {
				final <- struct{}{}
			}
			return model.NewTextMessage(model.RoleAssistant, "done")
		}}
		requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			if err := req.OnAdmission(ctx); err != nil {
				return agent.ApprovalResponse{}, err
			}
			entered <- ctx
			<-release // A resolver may need cleanup after its cancellation signal.
			return agent.ApprovalResponse{Approved: true}, nil
		})
		run, err := r.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "run command", ApprovalRequester: requester, AgentSpec: agent.AgentSpec{Name: "chat", Model: m, Tools: []tool.Tool{mustRuntimeRunCommandTool(t, backend)}}})
		if err != nil {
			t.Fatal(err)
		}
		approvalCtx := <-entered
		<-final
		synctest.Wait()
		handle := run.Handle.(*runner)
		select {
		case <-handle.done:
			t.Fatal("Run ended with a live approval continuation")
		default:
		}
		if err := run.Handle.Submit(agent.Submission{Kind: agent.SubmissionKindConversation, Text: "Cancel that command and finish"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !errors.Is(context.Cause(approvalCtx), errTaskAuthorizationChanged) {
			t.Fatalf("approval cancellation = %v", context.Cause(approvalCtx))
		}
		select {
		case <-handle.done:
			t.Fatal("Run released ownership before resolver cleanup")
		default:
		}
		close(release)
		if _, err := drainRunnerEvents(t, run.Handle); err != nil {
			t.Fatal(err)
		}
		if backend.startCalls() != 0 || calls < 3 {
			t.Fatalf("starts=%d model calls=%d", backend.startCalls(), calls)
		}
		events, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Journal != nil && event.Journal.ToolExecution != nil {
				t.Fatal("unstarted command wrote an execution approval receipt")
			}
		}
	})
}

func TestCommandApprovalSteeringInvalidatesInFlightAdmission(t *testing.T) {
	t.Parallel()
	_, active, r := newRuntimeRunCommandToolTestHarness(t)
	r.tasks.store = newSagaTaskStore()
	scope := newTaskContinuationScope()
	owner := context.WithValue(t.Context(), taskScopeKey{}, scope)
	backend := newCommandStartProbe(newYieldProbeSandboxSession(), nil)
	command := runtimeCommandTool{base: mustRuntimeRunCommandTool(t, backend), session: active, sessionRef: active.SessionRef, tasks: r.tasks}
	var approvals int
	target := policyWrappedTool{tool: command, mode: "ask", session: active, sessionRef: active.SessionRef,
		policy: policy.NamedMode{ID: "ask", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			// Steering arrives after the call began but before Task registration.
			scope.revoke(errTaskAuthorizationChanged, false)
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: input.Call.ID, Name: input.Tool.Name}}}, nil
		}},
		approval: approvalContext{ctx: owner, runtime: r, session: active, sessionRef: active.SessionRef, runID: "run", turnID: "turn", requester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			approvals++
			return agent.ApprovalResponse{Approved: true}, nil
		})},
	}
	result, err := target.Call(owner, tool.Call{ID: "late-admission", Name: shell.RunCommandToolName, Input: mustJSONRaw(map[string]any{"command": "echo old request", "yield_time_ms": 0})})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["state"] != "cancelled" || approvals != 0 || backend.startCalls() != 0 {
		t.Fatalf("late admission = %#v, approvals=%d starts=%d", payload, approvals, backend.startCalls())
	}
}
