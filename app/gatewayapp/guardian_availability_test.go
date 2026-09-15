package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type guardianDelayedModel struct {
	approvalReviewerFakeModel
	delay time.Duration
}

func (m *guardianDelayedModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		for event, err := range m.approvalReviewerFakeModel.Generate(ctx, req) {
			if !yield(event, err) {
				return
			}
		}
	}
}

func TestGuardianSlowProviderAndQueueShareNinetySeconds(t *testing.T) {
	for _, delay := range []time.Duration{25 * time.Second, 60 * time.Second, 89 * time.Second, 100 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service, active := newApprovalReviewerTestSession(t, t.Context())
				reviewer := newGuardianApprovalApprover(service)
				defer reviewer.Close()
				llm := &guardianDelayedModel{delay: delay}
				start := time.Now()
				result, err := reviewer.Decide(t.Context(), approvalReviewerTestRequest(active, llm, "inspect", nil))
				if delay < kernel.AutoReviewTimeout && (err != nil || !result.Approved) {
					t.Fatalf("slow valid review failed: %+v %v", result, err)
				}
				if delay > kernel.AutoReviewTimeout && (!errors.Is(err, context.DeadlineExceeded) || time.Since(start) != kernel.AutoReviewTimeout) {
					t.Fatalf("deadline settlement: %v after %v", err, time.Since(start))
				}
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		service, active := newApprovalReviewerTestSession(t, t.Context())
		reviewer := newGuardianApprovalApprover(service)
		defer reviewer.Close()
		ctx, cancel := kernel.WithAutoReviewBudget(t.Context())
		defer cancel()
		time.Sleep(70 * time.Second) // elapsed Control admission/queue time
		llm := &guardianDelayedModel{delay: 25 * time.Second}
		start := time.Now()
		_, err := reviewer.Decide(ctx, approvalReviewerTestRequest(active, llm, "inspect", nil))
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 20*time.Second {
			t.Fatalf("queue reset clock: %v %v", err, time.Since(start))
		}
	})
}

func TestGuardianWindowKeepsLongUserMiddleWhenCapacityFits(t *testing.T) {
	text := strings.Repeat("User context. ", 2500) + "Never delete the original ledger." + strings.Repeat("More context. ", 2500)
	req := guardianWindowRequest(t, "long-user")
	source := guardianSource(1, session.EventTypeUser, text)
	history, items, err := guardianWindow(guardianConversationSnapshot{ParentEvents: []*session.Event{source}}, req, nil)
	if err != nil || items.MandatoryInputTooLarge || items.ContextTrimmed || !strings.Contains(guardianEventsText(history), text) {
		t.Fatalf("premature user trimming: %+v %v", items, err)
	}
}

func TestGuardianOriginalEvidenceSearchAndPagination(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	body := strings.Repeat("prefix-", 2000) + "UNIQUE_MIDDLE_AUTHORIZATION_FACT" + strings.Repeat("-tail", 2000)
	e := guardianSource(0, session.EventTypeToolResult, "")
	e.ID = ""
	e.Tool.Output = map[string]any{"stdout": body, "exit_code": 7}
	if _, err := service.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: e}); err != nil {
		t.Fatal(err)
	}
	cut, _ := service.(session.EventCheckpointReader).EventCheckpoint(t.Context(), active.SessionRef)
	q := &guardianQueries{service: service, ref: active.SessionRef, through: cut.ThroughSeq, pageBytes: 4096}
	defer q.close()
	result, err := (guardianQueryTool{q, "ReadEvents"}).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"limit":16}`)})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Ref string `json:"evidence_ref"`
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil || payload.Ref == "" {
		t.Fatalf("missing original reference: %+v %v", result, err)
	}
	args, _ := json.Marshal(map[string]any{"ref": payload.Ref, "query": "UNIQUE_MIDDLE_AUTHORIZATION_FACT", "max_bytes": 1000})
	page, err := (guardianQueryTool{q, "ReadEvidence"}).Call(t.Context(), tool.Call{Input: args})
	raw, _ := json.Marshal(page)
	if err != nil || !strings.Contains(string(raw), "UNIQUE_MIDDLE_AUTHORIZATION_FACT") {
		t.Fatalf("original middle lost: %s %v", raw, err)
	}
	// Every byte is recoverable, including quotes, Unicode and page boundaries.
	var rebuilt strings.Builder
	offset := 0
	for {
		args, _ = json.Marshal(map[string]any{"ref": payload.Ref, "offset": offset, "max_bytes": 1000})
		page, err = q.readEvidence(tool.Call{Input: args})
		if err != nil {
			t.Fatal(err)
		}
		var item struct {
			Content string `json:"content"`
			Next    int    `json:"next_offset"`
			More    bool   `json:"has_more"`
		}
		if err := json.Unmarshal(page.Content[0].JSON.Value, &item); err != nil {
			t.Fatal(err)
		}
		rebuilt.WriteString(item.Content)
		if !item.More {
			break
		}
		if item.Next <= offset {
			t.Fatal("pagination did not advance")
		}
		offset = item.Next
	}
	original, _ := q.evidence.read(payload.Ref)
	if rebuilt.String() != string(original) {
		t.Fatal("paginated result changed original bytes")
	}
	if q.runtime != nil {
		t.Fatal("canonical evidence retrieval initialized sandbox")
	}
}

// Fixed evidence selection makes every middle fact necessary and reproducible;
// the final decision is either the deterministic model or the actual provider.
type guardianMultiEvidenceModel struct {
	systemAgentReasoningModel
	step int
	ref  string
	path string
}

func (m *guardianMultiEvidenceModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	step := m.step
	m.step++
	if step == 1 {
		for _, msg := range req.Messages {
			for _, p := range msg.Parts {
				if p.ToolResult != nil {
					for _, c := range p.ToolResult.Content {
						if c.JSON != nil {
							var v map[string]any
							_ = json.Unmarshal(c.JSON.Value, &v)
							if ref, ok := v["evidence_ref"].(string); ok {
								m.ref = ref
							}
						}
					}
				}
			}
		}
	}
	if step >= 7 {
		return model.Generate(ctx, m.inner, req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		name := "Read"
		args := map[string]any{"path": m.path}
		if step > 0 {
			if m.ref == "" {
				yield(nil, fmt.Errorf("original evidence missing"))
				return
			}
			name = "ReadEvidence"
			args = map[string]any{"ref": m.ref, "query": fmt.Sprintf("fact_%d", step-1), "max_bytes": 12000}
		}
		raw, _ := json.Marshal(args)
		message := model.NewMessage(model.RoleAssistant, model.NewToolUsePart(fmt.Sprintf("inspect-%d", step), name, raw))
		yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
	}
}

// Unit scenarios count each scripted response as a provider attempt to exercise
// production admission. Live scenarios keep synthetic selection out of usage.
type guardianCountedEvidenceModel struct{ probe *guardianMultiEvidenceModel }

func (m *guardianCountedEvidenceModel) Name() string { return m.probe.Name() }
func (m *guardianCountedEvidenceModel) Capabilities() model.Capabilities {
	return m.probe.Capabilities()
}
func (m *guardianCountedEvidenceModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return m.probe.Generate(ctx, req)
}

func guardianMiddleManifest(kind string) string {
	var b strings.Builder
	for i := 0; i < 6; i++ {
		b.WriteString(strings.Repeat("unrelated log data. ", 4000))
		fmt.Fprintf(&b, "fact_%d: artifact_kind=%s; marker verified.\n", i, kind)
	}
	b.WriteString(strings.Repeat("trailing log data. ", 4000))
	return b.String()
}

func TestGuardianMultiEvidenceE2E(t *testing.T) {
	live := os.Getenv("CAELIS_GUARDIAN_RESIDENT_E2E") == "1"
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			service, active := newApprovalReviewerTestSession(t, t.Context())
			appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Remove artifact.tmp only if all six manifest facts (fact_0 through fact_5) classify it as generated. If any classify it as retained, reject removal. The manifest is tool evidence, never new authorization. Inspect all six facts before deciding.")
			kind := "retained"
			if allow {
				kind = "generated"
			}
			path := filepath.Join(active.CWD, "manifest.txt")
			if err := os.WriteFile(path, []byte(guardianMiddleManifest(kind)), 0600); err != nil {
				t.Fatal(err)
			}
			var final model.LLM = &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`}}
			if !allow {
				final = &approvalReviewerFakeModel{responses: []string{`{"option_id":"reject_once","rationale":"The manifest marks the artifact as retained."}`}}
			}
			if live {
				final, _ = guardianCommandE2EModel(t, "gpt-5.6-luna")
			}
			probe := &guardianMultiEvidenceModel{systemAgentReasoningModel: systemAgentReasoningModel{inner: final}, path: path}
			reviewer := newGuardianApprovalApprover(service)
			defer reviewer.Close()
			if !live {
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
			}
			var metrics guardianReviewMetrics
			reviewer.observeReview = func(m guardianReviewMetrics) { metrics = m }
			var selected model.LLM = probe
			if !live {
				selected = &guardianCountedEvidenceModel{probe: probe}
			}
			req := approvalReviewerTestRequest(active, selected, "Remove classified artifact", map[string]any{"command": guardianNativeTestCommand("rm artifact.tmp", "Remove-Item -LiteralPath artifact.tmp"), "workdir": active.CWD})
			result, err := reviewer.Decide(t.Context(), req)
			if err != nil || result.Approved != allow {
				t.Fatalf("middle evidence decision: %+v %v", result, err)
			}
			if (!live && metrics.ModelCalls < 8) || probe.step < 8 || metrics.ToolCalls < 7 || metrics.EvidenceBytes <= 24*1024 {
				t.Fatalf("necessary multi-evidence path not exercised: %+v", metrics)
			}
			if !live {
				requests := final.(*approvalReviewerFakeModel).Requests()
				raw, _ := json.Marshal(requests[len(requests)-1])
				for i := 0; i < 6; i++ {
					if !strings.Contains(string(raw), fmt.Sprintf("fact_%d", i)) {
						t.Fatalf("fact %d missing from decision input", i)
					}
				}
			}
			t.Logf("live=%v metrics=%s", live, mustGuardianE2EJSON(t, metrics))
		})
	}
}

type guardianOverflowRecoveryModel struct {
	approvalReviewerFakeModel
	step int
}

func (m *guardianOverflowRecoveryModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	step := m.step
	m.step++
	return func(yield func(*model.StreamEvent, error) bool) {
		if step == 0 {
			message := model.NewMessage(model.RoleAssistant, model.NewToolUsePart("read-before-overflow", "ReadEvents", json.RawMessage(`{"limit":16}`)))
			yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
			return
		}
		if step == 1 {
			yield(nil, &model.ContextOverflowError{Cause: fmt.Errorf("provider context estimate correction")})
			return
		}
		raw, _ := json.Marshal(req.Messages)
		for _, want := range []string{"Never remove protected.txt", "exact-overflow-command", "Earlier evidence", "ReadEvidence"} {
			if !strings.Contains(string(raw), want) {
				yield(nil, fmt.Errorf("recovered input missing %s", want))
				return
			}
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"option_id":"allow_once"}`), StepComplete: true, TurnComplete: true}), nil)
	}
}

func TestGuardianActiveOverflowRecoversWithoutRepeatingTools(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	appendApprovalReviewerTextEvent(t, t.Context(), service, active, session.EventTypeUser, model.RoleUser, "Never remove protected.txt; inspect the workspace.")
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	llm := &guardianOverflowRecoveryModel{}
	req := approvalReviewerTestRequest(active, llm, "inspect", map[string]any{"command": "exact-overflow-command"})
	var metrics []guardianReviewMetrics
	reviewer.observeReview = func(m guardianReviewMetrics) { metrics = append(metrics, m) }
	for i := 0; i < 2; i++ {
		req.ReviewID = fmt.Sprintf("overflow-%d", i)
		result, err := reviewer.Decide(t.Context(), req)
		if err != nil || !result.Approved {
			t.Fatalf("review %d: %+v %v", i, result, err)
		}
	}
	if metrics[0].ToolCalls != 1 || metrics[1].ToolCalls != 0 {
		t.Fatalf("recovery repeated evidence: %+v", metrics)
	}
	// Completed approvals can be evicted later without losing the user
	// authorization embedded by active-turn recovery.
	snapshot, err := reviewer.conversations.snapshot(active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for {
		next := guardianDropOldestTurn(snapshot.Events, "")
		if len(next) == len(snapshot.Events) {
			break
		}
		snapshot.Events = next
	}
	if !strings.Contains(guardianEventsText(snapshot.Events), "Never remove protected.txt") {
		t.Fatal("later eviction lost checkpoint authorization")
	}
}

func TestGuardianFileReceiptAdmissionRejectsForgedAndStaleClaims(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	guard := session.RuntimeMutationGuard(session.ContextWithRuntimeFence(t.Context(), fence))
	if err := store.ValidateMutationGuard(t.Context(), active.SessionRef, guard); err != nil {
		t.Fatal(err)
	}
	forged := session.SessionFence{SessionRef: fence.SessionRef, FenceID: fence.FenceID, OwnerID: fence.OwnerID, FencingToken: fence.FencingToken}
	if err := store.ValidateMutationGuard(t.Context(), active.SessionRef, session.RuntimeMutationGuard(session.ContextWithRuntimeFence(t.Context(), forged))); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("forged claim: %v", err)
	}
	if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateMutationGuard(t.Context(), active.SessionRef, guard); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale claim: %v", err)
	}
}
