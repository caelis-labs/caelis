package gatewayapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/judgment/typesafe"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Explicitly opt in to sending an existing session's evidence to Jev. Only the
// approval classifier runs; no historical tool or Memory effect is executed.
func TestGuardianSessionJevReplay(t *testing.T) {
	path := os.Getenv("CAELIS_JEV_SESSION_EVENTS")
	if os.Getenv("CAELIS_JEV_SESSION_E2E") != "1" || path == "" || os.Getenv("JEV_API_KEY") == "" {
		t.Skip("set CAELIS_JEV_SESSION_E2E=1, CAELIS_JEV_SESSION_EVENTS and JEV_API_KEY")
	}
	client, err := typesafe.New(typesafe.Config{APIKey: os.Getenv("JEV_API_KEY")})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	metaPath := strings.TrimSuffix(path, ".events.jsonl") + ".json"
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Session session.Session `json:"session"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	service, active := newApprovalReviewerTestSession(t, t.Context())
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	var pending []*session.Event
	var results []map[string]any
	totalCalls := 0
	decoder := json.NewDecoder(bufio.NewReader(f))
	for {
		var event session.Event
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if session.IsCanonicalHistoryEvent(&event) {
			copy := session.CloneEvent(&event)
			copy.ID, copy.IdempotencyKey, copy.SessionID, copy.Seq = "", "", active.SessionID, 0
			pending = append(pending, copy)
		}
		if event.Journal == nil || event.Journal.PauseToken == nil || event.Journal.PauseToken.Status != session.PauseTokenPending {
			continue
		}
		if len(results) >= 8 {
			t.Fatal("replay limited to eight approvals")
		}
		if len(pending) > 0 {
			if _, err := service.(session.EventBatchService).AppendEvents(t.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: pending}); err != nil {
				t.Fatal(err)
			}
			pending = nil
		}
		pause := event.Journal.PauseToken
		// JSON journal metadata needs its typed trusted sandbox value restored.
		if value, ok := pause.Metadata[policy.MetadataSandboxPolicy]; ok {
			encoded, _ := json.Marshal(value)
			var snapshot sandbox.PolicySnapshot
			if err := json.Unmarshal(encoded, &snapshot); err != nil {
				t.Fatal(err)
			}
			pause.Metadata[policy.MetadataSandboxPolicy] = snapshot
		}
		runtimeReq := agentsdk.ApprovalRequest{
			SessionRef: active.SessionRef, Session: active, RunID: pause.RunID, TurnID: pause.TurnID,
			Tool: tool.Definition{Name: pause.ToolName}, Call: tool.Call{ID: pause.ToolCallID, Name: pause.ToolName, Input: pause.Input}, Approval: pause.Approval, Metadata: pause.Metadata,
			Origin: &agentsdk.ApprovalOrigin{Role: agentsdk.ApprovalRoleMain, Endpoint: agentsdk.ApprovalEndpointBuiltin, SessionID: metadata.Session.SessionID, WorkingDirectory: metadata.Session.CWD, ToolCallID: pause.ToolCallID},
		}
		req := approvalReviewerTestRequest(active, nil, "", nil)
		req.RuntimeRequest = runtimeReq
		req.Approval = approval.PayloadFromRuntimeRequest(runtimeReq)
		req.ReviewID = pause.TokenID
		calls, requestBytes, evidenceCount := 0, 0, 0
		var response judgment.Response
		req.Judgment = judgmentFunc(func(ctx context.Context, r judgment.Request) (judgment.Response, error) {
			calls++
			totalCalls++
			encoded, _ := json.Marshal(r)
			requestBytes = len(encoded)
			evidenceCount = len(r.State.(map[string]any)["evidence"].([]map[string]any))
			var err error
			response, err = client.Evaluate(ctx, r)
			return response, err
		})
		attempts := &guardianInvocationCollector{}
		start := time.Now()
		decision, screenErr := reviewer.runGuardianJudgment(t.Context(), req, attempts)
		outcome := "defer"
		if screenErr == nil {
			outcome = "deny"
			if decision.Approved {
				outcome = "allow"
			}
		}
		reason := ""
		if screenErr != nil {
			reason = guardianScreenReason(screenErr)
		}
		row := map[string]any{"seq": event.Seq, "outcome": outcome, "reason": reason, "requests": calls, "request_bytes": requestBytes, "evidence_records": evidenceCount, "input_tokens": response.Usage.InputTokens, "output_tokens": response.Usage.OutputTokens, "elapsed_ms": time.Since(start).Milliseconds(), "answers": response.Answers}
		results = append(results, row)
		encoded, _ := json.Marshal(row)
		t.Logf("REPLAY %s", encoded)
		if calls != 1 || len(attempts.snapshot()) != 1 {
			t.Errorf("approval %d did not reach Jev exactly once", event.Seq)
		}
		// Abstention is a valid screening outcome; record it independently of
		// transport coverage and never claim it saved an Agent request. Fail
		// unexpected provider errors and confident denials of these known allows.
		var failure *guardianScreenError
		if screenErr != nil && (!errors.As(screenErr, &failure) || failure.reason != "judgment_inconclusive") {
			t.Errorf("approval %d failed: %s", event.Seq, reason)
		}
		if screenErr == nil && !decision.Approved {
			t.Errorf("approval %d incorrectly denied", event.Seq)
		}
	}
	if len(results) == 0 {
		t.Fatal("no approvals found")
	}
	if output := os.Getenv("CAELIS_JEV_SESSION_E2E_OUT"); output != "" {
		encoded, _ := json.MarshalIndent(map[string]any{"session_id": metadata.Session.SessionID, "source": filepath.Base(path), "requests": totalCalls, "approvals": results}, "", "  ")
		if err := os.WriteFile(output, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
