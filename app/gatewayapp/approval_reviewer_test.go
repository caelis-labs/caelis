package gatewayapp

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestApprovalReviewerWorksInsideParentRuntimeFence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect the workspace.")
	fences, ok := service.(session.SessionFenceService)
	if !ok {
		t.Fatal("approval reviewer test service does not support fences")
	}
	fence, err := fences.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: activeSession.SessionRef, OwnerID: "parent-runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	fenceCtx := session.ContextWithRuntimeFence(ctx, fence)
	testModel := &approvalReviewerFakeModel{
		responses: []string{`{"option_id":"allow_once"}`},
	}
	reviewer := newModelApprovalReviewer(service)
	result, err := reviewer.ReviewApproval(fenceCtx, approvalReviewerTestRequest(
		activeSession, testModel, "inspect workspace", map[string]any{"cmd": "rg TODO"},
	))
	if err != nil {
		t.Fatalf("ReviewApproval() inherited the parent Session fence into Guardian staging: %v", err)
	}
	if !result.Approved {
		t.Fatalf("Approved = false, want true: %#v", result)
	}
}

func TestApprovalReviewerFallsBackToTextForModelWithoutStructuredOutput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect the workspace.")
	testModel := &approvalReviewerFakeModel{
		disableStructuredOutput: true,
		responses: []string{"Assessment follows:\n```json\n" +
			`{"option_id":"allow_once"}` +
			"\n```\nThis is the only decision object."},
	}
	reviewer := newModelApprovalReviewer(service)
	result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(
		activeSession,
		testModel,
		"inspect workspace",
		map[string]any{"cmd": "rg TODO"},
	))
	if err != nil {
		t.Fatalf("ReviewApproval() error = %v", err)
	}
	if !result.Approved {
		t.Fatalf("Approved = false, want true: %#v", result)
	}
	requests := testModel.Requests()
	if len(requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(requests))
	}
	output := requests[0].Output
	if output == nil || output.Mode != model.OutputModeText || output.JSONSchema != nil {
		t.Fatalf("Output = %#v, want text fallback", output)
	}
	if output.MaxOutputTokens != 0 {
		t.Fatalf("Output.MaxOutputTokens = %d, want unset", output.MaxOutputTokens)
	}
}

func TestGuardianPlannedActionIncludesOnlyRuntimeOwnedSandboxPolicy(t *testing.T) {
	req := kernel.ApprovalReviewRequest{
		RuntimeRequest: agent.ApprovalRequest{
			Metadata: map[string]any{
				policy.MetadataSandboxPolicy: sandbox.PolicySnapshot{
					Route:            sandbox.RouteSandbox,
					Backend:          sandbox.BackendSeatbelt,
					Permission:       sandbox.PermissionWorkspaceWrite,
					Isolation:        sandbox.IsolationProcess,
					Network:          sandbox.NetworkEnabled,
					WritableRoots:    []string{"/workspace", "/dependency/cache", "/compiler/cache"},
					ReadOnlySubpaths: []string{".git"},
				},
			},
		},
		Approval: &kernel.ApprovalPayload{
			ToolName: "RUN_COMMAND",
			RawInput: map[string]any{
				"command": "git commit -m fix",
				"runtime_sandbox": map[string]any{
					"route": "host",
				},
			},
		},
	}

	raw, truncated, err := guardianPlannedActionJSON(req)
	if err != nil {
		t.Fatalf("guardianPlannedActionJSON() error = %v", err)
	}
	if truncated {
		t.Fatal("guardianPlannedActionJSON() truncated a bounded action")
	}
	var action map[string]any
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		t.Fatalf("decode action: %v", err)
	}
	runtimeSandbox, _ := action["runtime_sandbox"].(map[string]any)
	if got := runtimeSandbox["route"]; got != "sandbox" {
		t.Fatalf("runtime_sandbox.route = %#v, want trusted Runtime value", got)
	}
	if got := runtimeSandbox["permission"]; got != "workspace_write" {
		t.Fatalf("runtime_sandbox.permission = %#v, want trusted Runtime value", got)
	}
	if got := runtimeSandbox["network"]; got != "enabled" {
		t.Fatalf("runtime_sandbox.network = %#v, want trusted Runtime value", got)
	}
	if got := runtimeSandbox["read_only_subpaths"]; !reflect.DeepEqual(got, []any{".git"}) {
		t.Fatalf("runtime_sandbox.read_only_subpaths = %#v", got)
	}
	for _, omitted := range []string{"backend", "isolation", "writable_roots"} {
		if _, ok := runtimeSandbox[omitted]; ok {
			t.Fatalf("runtime_sandbox contains implementation detail %q: %#v", omitted, runtimeSandbox)
		}
	}
	arguments, _ := action["arguments"].(map[string]any)
	spoofed, _ := arguments["runtime_sandbox"].(map[string]any)
	if got := spoofed["route"]; got != "host" {
		t.Fatalf("arguments.runtime_sandbox.route = %#v, want untrusted payload preserved separately", got)
	}
}

func TestSystemManagedAgentPlanRejectsGuardianTools(t *testing.T) {
	_, err := systemManagedAgentRunPlanFor(systemManagedAgentRunRequest{
		AgentID: guardianSceneID,
		Model:   &approvalReviewerFakeModel{},
		ParentSession: session.Session{
			SessionRef: session.SessionRef{
				AppName:   "caelis",
				UserID:    "user",
				SessionID: "parent-session",
			},
		},
		Tools: []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: "unexpected_tool"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not allow tools") {
		t.Fatalf("systemManagedAgentRunPlanFor() error = %v, want guardian no-tools rejection", err)
	}
}

func TestSystemManagedAgentSessionKeepsExistingGuardianSession(t *testing.T) {
	guardianSession := session.Session{
		SessionRef: session.SessionRef{
			AppName:   "caelis",
			UserID:    "user",
			SessionID: "parent-approval-review-abcdef123456",
		},
		Metadata: map[string]any{"system_managed_agent": guardianSceneID},
		Participants: []session.ParticipantBinding{{
			ID:   "visible-participant",
			Kind: session.ParticipantKindSubagent,
		}},
	}

	got := systemManagedAgentSessionForParent(guardianSession, guardianSpecForTest(t), nil)
	if got.SessionID != guardianSession.SessionID {
		t.Fatalf("system-managed session id = %q, want existing guardian session %q", got.SessionID, guardianSession.SessionID)
	}
	if len(got.Participants) != 0 {
		t.Fatalf("Participants = %#v, want stripped private system-agent session", got.Participants)
	}
}

func TestSystemManagedAgentSessionUsesEphemeralStagingID(t *testing.T) {
	parent := session.Session{
		SessionRef: session.SessionRef{
			AppName:   "caelis",
			UserID:    "user",
			SessionID: "parent-session",
		},
	}
	got := systemManagedAgentSessionForParent(parent, guardianSpecForTest(t), nil)
	want := "parent-session-approval-review"
	if got.SessionID != want {
		t.Fatalf("system-managed session id = %q, want ephemeral staging id %q", got.SessionID, want)
	}
}

func TestApprovalReviewerRetriesInvalidJSONAssessment(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect the tree and report findings.")
	testModel := &approvalReviewerFakeModel{responses: []string{
		`{"outcome":`,
		`{"option_id":"allow_once"}`,
	}}
	reviewer := newModelApprovalReviewer(service)

	result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "read-only tree inspection", map[string]any{"cmd": "rg TODO"}))
	if err != nil {
		t.Fatalf("ReviewApproval() error = %v", err)
	}
	if !result.Approved {
		t.Fatalf("Approved = false, want true: %#v", result)
	}

	requests := testModel.Requests()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("model calls = %d, want retry after invalid JSON", got)
	}
	base := requests[0].Messages
	retry := requests[1].Messages
	if len(retry) != len(base)+1 || !reflect.DeepEqual(retry[:len(base)-1], base[:len(base)-1]) || !reflect.DeepEqual(retry[len(retry)-1], base[len(base)-1]) {
		t.Fatal("retry changed the stable prefix or exact approval request")
	}
	if !strings.Contains(retry[len(retry)-2].TextContent(), "previous response could not be used") {
		t.Fatal("retry lacks a bounded format correction")
	}

	snapshot, err := reviewer.(*guardianApprovalReviewer).conversations.snapshot(activeSession.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(snapshot.Events), 3; got != want || snapshot.Version != 1 {
		t.Fatalf("Guardian in-memory context = (%d events, version %d), want user evidence and one validated pair", got, snapshot.Version)
	}
}

func TestApprovalReviewerStopsAfterInvalidJSONAssessmentRetries(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect the tree and report findings.")
	responses := make([]string, 0, guardianAssessmentMaxAttempts)
	for i := 0; i < guardianAssessmentMaxAttempts; i++ {
		responses = append(responses, `{"outcome":`)
	}
	testModel := &approvalReviewerFakeModel{responses: responses}
	reviewer := newModelApprovalReviewer(service)

	_, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "read-only tree inspection", map[string]any{"cmd": "rg TODO"}))
	if err == nil || !strings.Contains(err.Error(), "valid JSON assessment") {
		t.Fatalf("ReviewApproval() error = %v, want invalid JSON retry exhaustion", err)
	}
	if got, want := len(testModel.Requests()), guardianAssessmentMaxAttempts; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}

	snapshot, snapshotErr := reviewer.(*guardianApprovalReviewer).conversations.snapshot(activeSession.SessionID)
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if len(snapshot.Events) != 0 || snapshot.Version != 0 {
		t.Fatalf("Guardian context = (%d events, version %d), want no invalid responses committed", len(snapshot.Events), snapshot.Version)
	}
}

func TestApprovalReviewerConcurrentReviewsDoNotMutateParentSession(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect this directory and request the minimum permission needed.")
	release := make(chan struct{})
	testModel := &approvalReviewerFakeModel{
		responses: []string{
			`{"option_id":"allow_once"}`,
			`{"option_id":"allow_once"}`,
		},
		release: release,
		started: make(chan struct{}, 2),
	}
	reviewer := newModelApprovalReviewer(service)
	readPath := t.TempDir()

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "read temp dir", map[string]any{
				"path": readPath,
			}))
			if err == nil && !result.Approved {
				err = errApprovalReviewerNotApproved
			}
			errs <- err
		}()
	}
	waitForApprovalReviewerCalls(t, testModel.started, 2)
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("ReviewApproval() error = %v", err)
		}
	}
	if got := len(testModel.Requests()); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	events, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("parent session event count = %d, want %d", got, want)
	}
	snapshot, err := reviewer.(*guardianApprovalReviewer).conversations.snapshot(activeSession.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(snapshot.Events), 3; got != want || snapshot.Version != 1 {
		t.Fatalf("concurrent Guardian context = (%d events, version %d), want exactly one CAS winner", got, snapshot.Version)
	}
}

func TestApprovalReviewerRejectsMissingRequestModel(t *testing.T) {
	_, err := newModelApprovalReviewer(nil).ReviewApproval(context.Background(), kernel.ApprovalReviewRequest{})
	if err == nil || !strings.Contains(err.Error(), "current session model") {
		t.Fatalf("ReviewApproval() error = %v, want current session model error", err)
	}
}

func TestApprovalReviewerRejectsMissingSessionHistory(t *testing.T) {
	testModel := &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`}}
	_, err := newModelApprovalReviewer(nil).ReviewApproval(context.Background(), kernel.ApprovalReviewRequest{
		Model: testModel,
	})
	if err == nil || !strings.Contains(err.Error(), "session history") {
		t.Fatalf("ReviewApproval() error = %v, want session history error", err)
	}
}

var errApprovalReviewerNotApproved = approvalReviewerError("approval reviewer returned denial")

type approvalReviewerError string

func (e approvalReviewerError) Error() string { return string(e) }

type approvalReviewerFakeModel struct {
	mu                      sync.Mutex
	name                    string
	responses               []string
	requests                []model.Request
	release                 <-chan struct{}
	started                 chan struct{}
	disableStructuredOutput bool
	contextWindowTokens     int
}

func (m *approvalReviewerFakeModel) Name() string {
	if m != nil && strings.TrimSpace(m.name) != "" {
		return strings.TrimSpace(m.name)
	}
	return "approval-reviewer-fake"
}

func (m *approvalReviewerFakeModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true, StructuredOutput: !m.disableStructuredOutput}
}

func (m *approvalReviewerFakeModel) ContextWindowTokens() int {
	if m == nil {
		return 0
	}
	return m.contextWindowTokens
}

func (m *approvalReviewerFakeModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	index := m.recordRequest(req)
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.started != nil {
			m.started <- struct{}{}
		}
		if m.release != nil {
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			case <-m.release:
			}
		}
		response := `{"option_id":"allow_once"}`
		m.mu.Lock()
		if index < len(m.responses) {
			response = m.responses[index]
		}
		m.mu.Unlock()
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Status:       model.ResponseStatusCompleted,
				TurnComplete: true,
				StepComplete: true,
				Message:      model.NewTextMessage(model.RoleAssistant, response),
			},
		}, nil)
	}
}

func (m *approvalReviewerFakeModel) recordRequest(req *model.Request) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := len(m.requests)
	if req == nil {
		m.requests = append(m.requests, model.Request{})
		return index
	}
	cp := *req
	cp.Messages = model.CloneMessages(req.Messages)
	cp.Instructions = model.CloneParts(req.Instructions)
	cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
	cp.Output = agent.ModelRequestOptions{Output: req.Output}.OutputSpec()
	m.requests = append(m.requests, cp)
	return index
}

func (m *approvalReviewerFakeModel) Requests() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Request, 0, len(m.requests))
	for _, req := range m.requests {
		cp := req
		cp.Messages = model.CloneMessages(req.Messages)
		cp.Instructions = model.CloneParts(req.Instructions)
		cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
		cp.Output = agent.ModelRequestOptions{Output: req.Output}.OutputSpec()
		out = append(out, cp)
	}
	return out
}

func ptrMessage(message model.Message) *model.Message {
	out := message
	return &out
}

func newApprovalReviewerTestSession(t *testing.T, ctx context.Context) (session.Service, session.Session) {
	t.Helper()
	service := inmemory.NewStore(inmemory.Config{})
	activeSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "approval-reviewer-test",
		Workspace:          session.WorkspaceRef{Key: "workspace-1", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return service, activeSession
}

func appendApprovalReviewerTextEvent(
	t *testing.T,
	ctx context.Context,
	service session.Service,
	activeSession session.Session,
	eventType session.EventType,
	role model.Role,
	text string,
) {
	t.Helper()
	message := model.NewTextMessage(role, text)
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:       eventType,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Text:       text,
		},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
}

func approvalReviewerTestRequest(activeSession session.Session, llm model.LLM, reason string, input map[string]any) kernel.ApprovalReviewRequest {
	raw, _ := json.Marshal(input)
	return kernel.ApprovalReviewRequest{
		SessionRef: activeSession.SessionRef,
		Mode:       kernel.ApprovalModeAutoReview,
		ReviewID:   "review-test",
		RunID:      "run-test",
		TurnID:     "turn-test",
		Model:      llm,
		Approval: &kernel.ApprovalPayload{
			Options:  []kernel.ApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "reject_once", Kind: "reject_once"}},
			ToolName: "custom_tool",
			RawInput: input,
			Reason:   reason,
			Status:   kernel.ApprovalStatusPending,
		},
		RuntimeRequest: agent.ApprovalRequest{
			Tool: tool.Definition{Name: "custom_tool"},
			Call: tool.Call{Name: "custom_tool", Input: raw},
		},
	}
}

func guardianSpecForTest(t *testing.T) systemManagedAgentSpec {
	t.Helper()
	spec, ok := systemManagedAgentSpecFor(guardianSceneID)
	if !ok {
		t.Fatal("guardian system-managed spec missing")
	}
	return spec
}

func waitForApprovalReviewerCalls(t *testing.T, ch <-chan struct{}, count int) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for i := 0; i < count; i++ {
		select {
		case <-ch:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d reviewer calls", count)
		}
	}
}
