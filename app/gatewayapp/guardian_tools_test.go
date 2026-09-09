package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestGuardianNativeTemporaryWritesAndReadOnlyEvidence(t *testing.T) {
	if os.Getenv("CAELIS_TEST_GUARDIAN_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_GUARDIAN_NATIVE=1 for native sandbox execution")
	}
	outside := filepath.Join(t.TempDir(), "evidence.jsonl")
	if err := os.WriteFile(outside, []byte("immutable evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	q := &guardianQueries{network: sandbox.NetworkEnabled}
	defer q.close()
	call := func(name string, args map[string]any) tool.Result {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := (guardianQueryTool{q, name}).Call(t.Context(), tool.Call{ID: name, Name: name, Input: raw})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	call("RunCommand", map[string]any{"command": "printf evidence > \"$TMPDIR/query.txt\"; cat \"$TMPDIR/query.txt\""})
	if raw, err := os.ReadFile(filepath.Join(q.scratch, "query.txt")); err != nil || string(raw) != "evidence" {
		t.Fatalf("temporary write failed: %q %v", raw, err)
	}
	missing, _ := json.Marshal(map[string]any{"path": outside + ".missing"})
	if _, err := (guardianQueryTool{q, "Read"}).Call(t.Context(), tool.Call{ID: "missing", Input: missing}); err == nil {
		t.Fatal("missing evidence should report a tool error")
	}
	if q.failure != nil {
		t.Fatalf("recoverable query poisoned review: %v", q.failure)
	}
	call("Read", map[string]any{"path": outside})
	call("RunCommand", map[string]any{"command": "printf overwritten > " + outside})
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "immutable evidence\n" {
		t.Fatalf("escaped temporary-only write policy: %q %v", raw, err)
	}
	scratch := q.scratch
	if err := q.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch remains: %v", err)
	}
}

type guardianToolLoopModel struct {
	requests   []*model.Request
	affinities []string
}

func (*guardianToolLoopModel) Name() string { return "guardian-tool-loop" }
func (*guardianToolLoopModel) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, ToolCalls: true, StructuredOutput: true}
}
func (m *guardianToolLoopModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	metadata, _ := model.ProviderRequestMetadataFromContext(ctx)
	m.affinities = append(m.affinities, metadata.SessionAffinity)
	m.requests = append(m.requests, model.CloneRequest(req))
	n := len(m.requests)
	return func(yield func(*model.StreamEvent, error) bool) {
		if n == 1 {
			message := model.NewMessage(model.RoleAssistant, model.NewToolUsePart("query-1", "RunCommand", json.RawMessage(`{"command":"printf evidence"}`)))
			yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"option_id":"allow_once"}`), StepComplete: true, TurnComplete: true}), nil)
	}
}
func TestGuardianNativeToolTurnReplaysAsStablePrefix(t *testing.T) {
	if os.Getenv("CAELIS_TEST_GUARDIAN_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_GUARDIAN_NATIVE=1 for native sandbox execution")
	}
	service, active := newApprovalReviewerTestSession(t, t.Context())
	llm := &guardianToolLoopModel{}
	reviewer := newGuardianApprovalApprover(service)
	req := approvalReviewerTestRequest(active, llm, "check", nil)
	if result, err := reviewer.Decide(t.Context(), req); err != nil || !result.Approved {
		t.Fatalf("decision=%v err=%v", result, err)
	}
	snapshot, err := reviewer.conversations.snapshot(active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot.Events)
	if !strings.Contains(string(raw), "query-1") || !strings.Contains(string(raw), "evidence") {
		t.Fatal("tool loop was discarded")
	}
	for _, e := range snapshot.Events {
		if guardianTurn(e) == "" {
			t.Fatal("tool event is outside complete turn")
		}
	}
	req.ReviewID = "second"
	if _, err := reviewer.Decide(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 3 {
		t.Fatalf("provider calls=%d", len(llm.requests))
	}
	if len(llm.affinities) != 3 || llm.affinities[0] == "" || llm.affinities[0] != llm.affinities[2] {
		t.Fatalf("provider affinity changed: %v", llm.affinities)
	}
	a, b := llm.requests[1], llm.requests[2]
	if !reflect.DeepEqual(a.Instructions, b.Instructions) || !reflect.DeepEqual(a.Tools, b.Tools) || !reflect.DeepEqual(a.Messages, b.Messages[:len(a.Messages)]) {
		t.Fatal("tool-loop prefix changed on next approval")
	}
	events, err := service.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil || len(events) != 0 {
		t.Fatalf("private tool turns leaked to parent: %d %v", len(events), err)
	}
}

func TestGuardianNativeInheritedNetwork(t *testing.T) {
	if os.Getenv("CAELIS_TEST_GUARDIAN_NATIVE") != "1" {
		t.Skip("native sandbox test")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("network-evidence")) }))
	defer server.Close()
	for _, network := range []sandbox.Network{sandbox.NetworkEnabled, sandbox.NetworkDisabled} {
		q := &guardianQueries{network: network}
		raw, _ := json.Marshal(map[string]any{"command": "curl --noproxy '*' --max-time 2 -fsS " + server.URL})
		result, err := (guardianQueryTool{q, "RunCommand"}).Call(t.Context(), tool.Call{ID: "network", Input: raw})
		_ = q.close()
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "network-evidence") != (network == sandbox.NetworkEnabled) {
			t.Fatalf("network=%s result=%s", network, encoded)
		}
	}
}

func TestGuardianExhaustedEvidenceStopsFurtherProviderAdmission(t *testing.T) {
	q := &guardianQueries{calls: 8}
	_, err := (guardianQueryTool{q, "Read"}).Call(t.Context(), tool.Call{ID: "over-budget", Input: json.RawMessage(`{"path":"unused"}`)})
	if err == nil || q.failure == nil {
		t.Fatal("expected evidence budget failure")
	}
	if next := q.admit(t.Context(), &model.Request{}); !errors.Is(next, q.failure) {
		t.Fatalf("provider admission ignored evidence failure: %v", next)
	}
	if q.attempts != 0 {
		t.Fatalf("exhausted review admitted %d provider attempts", q.attempts)
	}
}
