package gatewayapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
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

func (s guardianReceiptFailureStore) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	return s.Service.(session.PagedReader).EventsPage(ctx, req)
}
func (s guardianReceiptFailureStore) EventCheckpoint(ctx context.Context, ref session.SessionRef) (session.EventCheckpoint, error) {
	return s.Service.(session.EventCheckpointReader).EventCheckpoint(ctx, ref)
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
	llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{responses: []string{`invalid assessment`, `{"option_id":"allow_once"}`}}}
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
	var diagnostics bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&diagnostics, nil))
	reviewer := newGuardianApprovalApprover(guardianReceiptFailureStore{Service: store, failure: failure}, logger)
	llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`}}}
	req := approvalReviewerTestRequest(active, llm, "inspect", nil)
	result, err := reviewer.Decide(context.Background(), req)
	if err != nil || !result.Approved || !strings.Contains(result.DisplayText, "approved") || !strings.Contains(result.DisplayText, "usage accounting could not be persisted") {
		t.Fatalf("decision changed: %#v %v", result, err)
	}
	if len(llm.Requests()) != 1 {
		t.Fatal("accounting fault repeated decision")
	}
	var diagnostic map[string]any
	lines := bytes.Split(bytes.TrimSpace(diagnostics.Bytes()), []byte("\n"))
	if err := json.Unmarshal(lines[len(lines)-1], &diagnostic); err != nil {
		t.Fatalf("decode accounting diagnostic: %v; %s", err, diagnostics.String())
	}
	if diagnostic["msg"] != "Guardian usage persistence failed" || diagnostic["session_id"] != req.SessionRef.SessionID || diagnostic["review_id"] != req.ReviewID || diagnostic["error"] != (&guardianUsagePersistenceError{cause: failure}).Error() {
		t.Fatalf("accounting diagnostic lost its cause or correlation: %#v", diagnostic)
	}
	if strings.Contains(result.DisplayText, failure.Error()) {
		t.Fatal("private storage failure leaked into the approval rationale")
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
	reviewer.reviews.Wait()
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

func TestGuardianReceiptRetainsOriginalParentFence(t *testing.T) {
	for _, scenario := range []string{"owned_fence", "public_fields_are_not_authority", "stale_fence", "empty_runtime_claim"} {
		t.Run(scenario, func(t *testing.T) {
			valid := scenario == "owned_fence"
			store, active := newApprovalReviewerTestSession(t, context.Background())
			fences := store.(session.SessionFenceService)
			fence, err := fences.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "parent"})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "public_fields_are_not_authority":
				fence = session.SessionFence{SessionRef: fence.SessionRef, FenceID: fence.FenceID, OwnerID: fence.OwnerID, FencingToken: fence.FencingToken}
			case "stale_fence":
				if err := fences.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
					t.Fatal(err)
				}
				if _, err := fences.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "next-turn"}); err != nil {
					t.Fatal(err)
				}
			case "empty_runtime_claim":
				// The parent still owns the active fence. An empty Runtime claim
				// must not become Control authority that overlaps it.
				fence = session.SessionFence{}
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
