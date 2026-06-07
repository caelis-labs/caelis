package runner

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/agent/approval/autoreview"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

// ─── Test tools ──────────────────────────────────────────────────────

type echoTool struct{}

func (t *echoTool) Definition() tool.Definition {
	return tool.Definition{Name: "ECHO", Schema: tool.Schema{Type: "object"}}
}

func (t *echoTool) Run(_ tool.Context, call tool.Call) (tool.Result, error) {
	text, _ := call.Args["text"].(string)
	return tool.Result{Output: text}, nil
}

type denyPolicy struct{}

func (p *denyPolicy) Evaluate(_ context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{
		Outcome: policy.OutcomeDeny,
		Reason:  "denied by test policy",
	}, nil
}

type approveNeededPolicy struct{}

func (p *approveNeededPolicy) Evaluate(_ context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{
		Outcome: policy.OutcomeApprovalNeeded,
		Reason:  "needs approval",
	}, nil
}

type allowPolicy struct{}

func (p *allowPolicy) Evaluate(_ context.Context, req policy.Request) (policy.Decision, error) {
	return policy.Decision{Outcome: policy.OutcomeAllow}, nil
}

type mockApprover struct {
	approved bool
}

func (a *mockApprover) RequestApproval(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	return agent.ApprovalResponse{Approved: a.approved, Reason: "test"}, nil
}

// ─── Tests ───────────────────────────────────────────────────────────

func TestPolicyAllow(t *testing.T) {
	base := &echoTool{}
	wrapped := WrapTools([]tool.Tool{base}, &allowPolicy{}, nil, nil)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Args: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "hello" {
		t.Errorf("got %q, want %q", result.Output, "hello")
	}
}

func TestPolicyDeny(t *testing.T) {
	base := &echoTool{}
	wrapped := WrapTools([]tool.Tool{base}, &denyPolicy{}, nil, nil)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for denied tool")
	}
}

func TestPolicyApprovalApproved(t *testing.T) {
	base := &echoTool{}
	approver := &mockApprover{approved: true}
	wrapped := WrapTools([]tool.Tool{base}, &approveNeededPolicy{}, approver, nil)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "approved"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Output)
	}
	if result.Output != "approved" {
		t.Errorf("got %q, want %q", result.Output, "approved")
	}
}

func TestPolicyApprovalDenied(t *testing.T) {
	base := &echoTool{}
	approver := &mockApprover{approved: false}
	wrapped := WrapTools([]tool.Tool{base}, &approveNeededPolicy{}, approver, nil)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "denied"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for denied approval")
	}
}

func TestPolicyApprovalUsesAutoReviewRequester(t *testing.T) {
	base := &echoTool{}
	approver := autoreview.New(autoreview.Config{
		Model: &toolwrapReviewLLM{events: []model.ResponseEvent{{TextDelta: `{"outcome":"allow"}`}}},
	})
	wrapped := WrapTools([]tool.Tool{base}, &approveNeededPolicy{}, approver, nil)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "auto-approved"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Output)
	}
	if result.Output != "auto-approved" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestPolicyApprovalPassesTranscriptContext(t *testing.T) {
	base := &echoTool{}
	approver := &capturingApprover{approved: true}
	activeSession := session.Session{
		Ref: session.Ref{AppName: "test", UserID: "user", WorkspaceKey: "workspace", SessionID: "sess-1"},
	}
	transcript := []session.Event{{
		ID:         "event-1",
		Kind:       session.EventKindUser,
		Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "approve writes"}},
		},
	}}
	wrapped := WrapTools(
		[]tool.Tool{base},
		&approveNeededPolicy{},
		approver,
		nil,
		WithApprovalContext(activeSession, transcript),
	)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "with transcript"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Output)
	}
	if !approver.request.Session.Ref.Equal(activeSession.Ref) {
		t.Fatalf("approval session = %#v, want %#v", approver.request.Session.Ref, activeSession.Ref)
	}
	if len(approver.request.Transcript) != 1 || approver.request.Transcript[0].TextContent() != "approve writes" {
		t.Fatalf("approval transcript = %#v", approver.request.Transcript)
	}
}

func TestPolicyApprovalNoApprover(t *testing.T) {
	base := &echoTool{}
	wrapped := WrapTools([]tool.Tool{base}, &approveNeededPolicy{}, nil, nil)

	result, _ := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "no approver"},
	})
	if !result.IsError {
		t.Error("expected error when no approver configured")
	}
}

func TestNoPolicyPassthrough(t *testing.T) {
	base := &echoTool{}
	wrapped := WrapTools([]tool.Tool{base}, nil, nil, nil)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Args: map[string]any{"text": "passthrough"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "passthrough" {
		t.Errorf("got %q, want %q", result.Output, "passthrough")
	}
}

func TestTruncation(t *testing.T) {
	bigOutput := make([]byte, 50000)
	for i := range bigOutput {
		bigOutput[i] = 'x'
	}
	bigTool := &staticResultTool{output: string(bigOutput)}
	wrapped := WrapTools([]tool.Tool{bigTool}, nil, nil, nil)

	result, err := wrapped[0].Run(fakeCtx(), tool.Call{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Truncated {
		t.Error("expected result to be truncated")
	}
	if len(result.Output) >= 50000 {
		t.Errorf("output not truncated: %d bytes", len(result.Output))
	}
}

func TestObserverCalled(t *testing.T) {
	base := &echoTool{}
	obs := &testObserver{}
	wrapped := WrapTools([]tool.Tool{base}, nil, nil, obs)

	_, err := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "observed"},
	})
	require.NoError(t, err)

	if !obs.beforeCalled {
		t.Error("BeforeTool not called")
	}
	if !obs.afterCalled {
		t.Error("AfterTool not called")
	}
}

func TestChainOrder(t *testing.T) {
	base := &echoTool{}
	obs := &testObserver{}
	wrapped := WrapTools([]tool.Tool{base}, &denyPolicy{}, nil, obs)

	result, _ := wrapped[0].Run(fakeCtx(), tool.Call{
		Name: "ECHO",
		Args: map[string]any{"text": "should not reach"},
	})

	if !result.IsError {
		t.Error("expected policy deny")
	}
	if obs.beforeCalled {
		t.Error("observer should not be called when policy denies")
	}
}

func TestTaskAwareShellPassesPolicyConstraintsToBackend(t *testing.T) {
	requests := make(chan sandbox.CommandRequest, 1)
	backend := &recordingCommandBackend{requests: requests}
	tm := NewTaskManager(backend)
	pol := &constraintPolicy{
		constraints: &policy.SandboxConstraints{
			Permission: policy.SandboxPermDefault,
			Paths: []policy.PathRule{
				{Path: "/tmp/workspace", Access: sandbox.PathAccessWrite},
			},
		},
	}

	augmented := AugmentTools([]tool.Tool{&mockShellTool{}}, tm)
	wrapped := WrapTools(augmented, pol, nil, nil)
	ctx := &toolContext{
		Context:      context.Background(),
		sessionRef:   "session-1",
		invocationID: "inv-1",
		agentName:    "agent-1",
		backend:      backend,
	}

	result, err := wrapped[0].Run(ctx, tool.Call{
		Name: "RUN_COMMAND",
		Args: map[string]any{
			"command": "echo ok",
			"workdir": "/tmp/workspace",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful result, got: %s", result.Output)
	}

	select {
	case req := <-requests:
		if req.Command != "echo ok" {
			t.Fatalf("command: got %q", req.Command)
		}
		if len(req.Constraints.Paths) != 1 {
			t.Fatalf("constraints paths: got %d, want 1", len(req.Constraints.Paths))
		}
		rule := req.Constraints.Paths[0]
		if rule.Path != "/tmp/workspace" || rule.Access != sandbox.PathAccessWrite {
			t.Fatalf("constraint rule = %#v", rule)
		}
	default:
		t.Fatal("backend did not receive command request")
	}
}

// fakeCtx returns a minimal tool.Context for testing.
type fakeToolCtx struct{}

func (c *fakeToolCtx) Deadline() (time.Time, bool)    { return time.Time{}, false }
func (c *fakeToolCtx) Done() <-chan struct{}          { return nil }
func (c *fakeToolCtx) Err() error                     { return nil }
func (c *fakeToolCtx) Value(_ any) any                { return nil }
func (c *fakeToolCtx) SessionRef() string             { return "test-session" }
func (c *fakeToolCtx) InvocationID() string           { return "test-inv" }
func (c *fakeToolCtx) AgentName() string              { return "test-agent" }
func (c *fakeToolCtx) FileSystem() sandbox.FileSystem { return nil }

func fakeCtx() tool.Context { return &fakeToolCtx{} }

// ─── Helper tools ────────────────────────────────────────────────────

type constraintPolicy struct {
	constraints *policy.SandboxConstraints
}

func (p *constraintPolicy) Evaluate(_ context.Context, _ policy.Request) (policy.Decision, error) {
	return policy.Decision{
		Outcome:     policy.OutcomeAllow,
		Constraints: p.constraints,
	}, nil
}

type recordingCommandBackend struct {
	requests chan sandbox.CommandRequest
}

func (b *recordingCommandBackend) Name() string { return "recording" }

func (b *recordingCommandBackend) Describe(_ context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{Name: "recording"}, nil
}

func (b *recordingCommandBackend) Run(_ context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	b.requests <- req
	return sandbox.CommandResult{Stdout: []byte("ok"), ExitCode: 0}, nil
}

func (b *recordingCommandBackend) FileSystem(_ context.Context, _ sandbox.Constraints) (sandbox.FileSystem, error) {
	return nil, nil
}

func (b *recordingCommandBackend) Status(_ context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}

func (b *recordingCommandBackend) Close() error { return nil }

type staticResultTool struct {
	output string
}

func (t *staticResultTool) Definition() tool.Definition {
	return tool.Definition{Name: "STATIC"}
}

func (t *staticResultTool) Run(_ tool.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Output: t.output}, nil
}

type testObserver struct {
	beforeCalled bool
	afterCalled  bool
}

func (o *testObserver) BeforeTool(_ tool.Call) { o.beforeCalled = true }
func (o *testObserver) AfterTool(_ tool.Call, _ tool.Result, _ error) {
	o.afterCalled = true
}

type capturingApprover struct {
	approved bool
	request  agent.ApprovalRequest
}

func (a *capturingApprover) RequestApproval(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	a.request = req
	return agent.ApprovalResponse{Approved: a.approved, Reason: "captured"}, nil
}

type toolwrapReviewLLM struct {
	events []model.ResponseEvent
}

func (m *toolwrapReviewLLM) Name() string { return "toolwrap-reviewer" }

func (m *toolwrapReviewLLM) Generate(_ context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		for _, event := range m.events {
			if !yield(event, nil) {
				return
			}
		}
	}
}
