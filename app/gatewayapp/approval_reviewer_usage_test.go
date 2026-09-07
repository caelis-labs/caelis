package gatewayapp

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type guardianMeasuredModel struct {
	*approvalReviewerFakeModel
	interrupt func()
}

func (m *guardianMeasuredModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		for event, err := range m.approvalReviewerFakeModel.Generate(ctx, req) {
			if event != nil && event.Response != nil {
				event.Usage = model.Usage{PromptTokens: 10, CachedInputTokens: 3, CompletionTokens: 2, TotalTokens: 12}
			}
			if m.interrupt != nil {
				m.interrupt()
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

type guardianReceiptFailureStore struct {
	session.Service
	failure error
}

func (s guardianReceiptFailureStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if session.IsModelInvocationReceipt(req.Event) {
		return nil, s.failure
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestGuardianRepairPersistsAllUsageToParentAndReopens(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{responses: []string{`invalid assessment`, `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"authorized"}`}}}
	reviewer := newGuardianApprovalApprover(store)
	req := approvalReviewerTestRequest(active, llm, "inspect", map[string]any{"cmd": "echo approved"})
	result, err := reviewer.Decide(context.Background(), req)
	if err != nil || !result.Approved {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	events, err := reopened.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	count, total := 0, 0
	for _, event := range events {
		if session.IsModelInvocationReceipt(event) {
			count++
			usage := session.UsageSnapshotFromSessionEvent(event)
			if usage == nil {
				t.Fatal("missing reported usage")
			}
			total += usage.TotalTokens
			if event.Scope.Executor.ID != "guardian" || event.Meta["usage_category"] != "auto_review" {
				t.Fatalf("wrong owner: %#v", event)
			}
		}
	}
	if count != 2 || len(llm.Requests()) != 2 || total != 24 {
		t.Fatalf("receipts=%d calls=%d tokens=%d", count, len(llm.Requests()), total)
	}
	if usage, _, err := reviewer.ApprovalReviewAccounting(context.Background(), req, result); err != nil || usage != nil {
		t.Fatalf("duplicate legacy accounting: %#v %v", usage, err)
	}
}

func TestGuardianAccountingFailureKeepsCompletedDecision(t *testing.T) {
	store, active := newApprovalReviewerTestSession(t, context.Background())
	failure := errors.New("receipt store unavailable")
	reviewer := newGuardianApprovalApprover(guardianReceiptFailureStore{Service: store, failure: failure})
	llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{responses: []string{`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"authorized action"}`}}}
	req := approvalReviewerTestRequest(active, llm, "inspect", nil)
	result, err := reviewer.Decide(context.Background(), req)
	if err != nil || !result.Approved || !strings.Contains(result.DisplayText, "authorized action") || !strings.Contains(result.DisplayText, "usage accounting could not be persisted") {
		t.Fatalf("decision changed: %#v %v", result, err)
	}
	if len(llm.Requests()) != 1 {
		t.Fatal("accounting fault repeated decision")
	}
	usage, _, err := reviewer.ApprovalReviewAccounting(context.Background(), req, result)
	var typed *guardianUsagePersistenceError
	if usage != nil || !errors.As(err, &typed) || !errors.Is(err, failure) {
		t.Fatalf("accounting failure hidden: %#v %v", usage, err)
	}
	if usage, _, err := reviewer.ApprovalReviewAccounting(context.Background(), req, result); usage != nil || err != nil {
		t.Fatal("second candidate repeated incomplete usage")
	}
}

func TestGuardianCancelledAttemptStillPersistsParentUsage(t *testing.T) {
	store, active := newApprovalReviewerTestSession(t, context.Background())
	reviewer := newGuardianApprovalApprover(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{}, interrupt: cancel}
	req := approvalReviewerTestRequest(active, llm, "inspect", nil)
	_, err := reviewer.Decide(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel cause=%v", err)
	}
	events, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if session.IsModelInvocationReceipt(event) {
			count++
			if session.UsageSnapshotFromSessionEvent(event) == nil {
				t.Fatal("cancelled usage missing")
			}
		}
	}
	if count != 1 || len(llm.Requests()) != 1 {
		t.Fatalf("receipts=%d calls=%d", count, len(llm.Requests()))
	}
}

func TestGuardianConcurrentReviewsDoNotShareAccounting(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	reviewer := newGuardianApprovalApprover(store)
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		active, err := store.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: name, Workspace: session.WorkspaceRef{Key: name, CWD: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{name: name}}
			req := approvalReviewerTestRequest(active, llm, "inspect", nil)
			req.ReviewID = name
			result, err := reviewer.Decide(context.Background(), req)
			if err != nil || !result.Approved {
				t.Errorf("%s decision: %#v %v", name, result, err)
				return
			}
			events, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Error(err)
				return
			}
			count := 0
			for _, event := range events {
				if session.IsModelInvocationReceipt(event) {
					count++
					if event.Invocation.Model != name || event.Meta["review_id"] != name {
						t.Errorf("cross-review accounting: %#v", event)
					}
				}
			}
			if count != 1 {
				t.Errorf("%s receipts=%d", name, count)
			}
		}()
	}
	wg.Wait()
}

type guardianCompactionUsageModel struct {
	*guardianMeasuredModel
	compactCalls int
}

func (m *guardianCompactionUsageModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req.Output != nil {
		return m.guardianMeasuredModel.Generate(ctx, req)
	}
	m.compactCalls++
	return func(yield func(*model.StreamEvent, error) bool) {
		if !yield(&model.StreamEvent{Type: model.StreamEventPartDelta, Response: &model.Response{Usage: model.Usage{TotalTokens: 12}}}, nil) {
			return
		}
		yield(nil, &model.ContextOverflowError{Cause: errors.New("compaction input overflow")})
	}
}
func TestGuardianFailedCompactionIsIncludedInParentAccounting(t *testing.T) {
	ctx := context.Background()
	store, active := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, store, active, session.EventTypeUser, model.RoleUser, "Inspect and apply the focused fix.")
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	_, cursor := collectGuardianTranscriptEntries(events)
	reviewer := newGuardianApprovalApprover(store)
	user, assistant := guardianBudgetConversationPair(strings.Repeat("old Guardian context ", 3000))
	if _, _, err := reviewer.conversations.commitValidated(guardianConversationCommit{SessionID: active.SessionID, ExpectedVersion: 0, ParentCursor: cursor, User: user, Assistant: assistant}); err != nil {
		t.Fatal(err)
	}
	appendApprovalReviewerTextEvent(t, ctx, store, active, session.EventTypeAssistant, model.RoleAssistant, "The focused fix is ready for verification.")
	llm := &guardianCompactionUsageModel{guardianMeasuredModel: &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{contextWindowTokens: 16384}}}
	result, err := reviewer.Decide(ctx, approvalReviewerTestRequest(active, llm, "verify", map[string]any{"cmd": "go test ./focused"}))
	if err != nil || !result.Approved {
		t.Fatalf("decision=%#v err=%v", result, err)
	}
	events, err = store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if session.IsModelInvocationReceipt(event) {
			count++
		}
	}
	if llm.compactCalls == 0 || count != llm.compactCalls+len(llm.Requests()) {
		t.Fatalf("compact calls=%d normal calls=%d parent receipts=%d", llm.compactCalls, len(llm.Requests()), count)
	}
}

func TestGuardianReceiptRetainsOriginalParentFence(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned_fence", false: "public_fields_are_not_authority"}[valid], func(t *testing.T) {
			store, active := newApprovalReviewerTestSession(t, context.Background())
			fences := store.(session.SessionFenceService)
			fence, err := fences.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "parent"})
			if err != nil {
				t.Fatal(err)
			}
			if !valid {
				fence = session.SessionFence{SessionRef: fence.SessionRef, FenceID: fence.FenceID, OwnerID: fence.OwnerID, FencingToken: fence.FencingToken}
			}
			ctx := session.ContextWithRuntimeFence(context.Background(), fence)
			reviewer := newGuardianApprovalApprover(store)
			llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{}}
			req := approvalReviewerTestRequest(active, llm, "inspect", nil)
			result, err := reviewer.Decide(ctx, req)
			if err != nil || !result.Approved {
				t.Fatalf("decision changed: %#v %v", result, err)
			}
			warned := strings.Contains(result.DisplayText, "usage accounting could not be persisted")
			if warned == valid {
				t.Fatalf("valid=%v warning=%v", valid, warned)
			}
			events, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if session.IsModelInvocationReceipt(event) {
					count++
				}
			}
			if (valid && count != 1) || (!valid && count != 0) {
				t.Fatalf("valid=%v count=%d", valid, count)
			}
			if !valid {
				_, _, err := reviewer.ApprovalReviewAccounting(context.Background(), req, result)
				if !errors.Is(err, session.ErrFenceConflict) {
					t.Fatalf("fence failure hidden: %v", err)
				}
			}
		})
	}
}
