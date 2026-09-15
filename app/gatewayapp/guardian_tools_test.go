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
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

func TestGuardianFileToolsReuseBuiltinDefinitionsAndResults(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "evidence.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("evidence\n", 230)), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	q := &guardianQueries{runtime: rt, resultBytes: 64 * 1024}
	t.Cleanup(func() { _ = q.close() })
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), rt)
	if err != nil {
		t.Fatal(err)
	}
	grep, err := filesystem.NewSearch(rt)
	if err != nil {
		t.Fatal(err)
	}
	for _, builtin := range []tool.Tool{read, grep} {
		definition := builtin.Definition()
		t.Run(definition.Name, func(t *testing.T) {
			query := guardianQueryTool{q, definition.Name}
			definition.ExecutionRequirements = nil
			if got := query.Definition(); !reflect.DeepEqual(got, definition) {
				t.Fatalf("Guardian changed the built-in definition:\ngot: %#v\nwant: %#v", got, definition)
			}
			args := map[string]any{"path": path}
			if definition.Name == "Grep" {
				args["pattern"] = "evidence"
			}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			call := tool.Call{ID: "query", Name: definition.Name, Input: raw}
			want, err := builtin.Call(t.Context(), call)
			if err != nil {
				t.Fatal(err)
			}
			got, err := query.Call(t.Context(), call)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Guardian changed built-in output:\ngot: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestGuardianCancelledQueryDoesNotOpenSandbox(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	q := &guardianQueries{}
	_, err := (guardianQueryTool{q, "Read"}).Call(ctx, tool.Call{Input: json.RawMessage(`{"path":"unused"}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query error = %v", err)
	}
	if q.runtime != nil || q.scratch != "" || q.calls != 0 {
		t.Fatal("cancelled query opened or consumed query resources")
	}
}

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
	call("RunCommand", map[string]any{"command": guardianNativeTestCommand("printf evidence > \"$TMPDIR/query.txt\"; cat \"$TMPDIR/query.txt\"", "Set-Content -LiteralPath \"$env:TMPDIR/query.txt\" -Value evidence -NoNewline; Get-Content -LiteralPath \"$env:TMPDIR/query.txt\"")})
	if raw, err := os.ReadFile(filepath.Join(q.work, "query.txt")); err != nil || string(raw) != "evidence" {
		t.Fatalf("temporary write failed: %q %v", raw, err)
	}
	missing, _ := json.Marshal(map[string]any{"path": outside + ".missing"})
	if result, err := (guardianQueryTool{q, "Read"}).Call(t.Context(), tool.Call{ID: "missing", Input: missing}); err != nil || !result.IsError {
		t.Fatalf("missing evidence should return a model-visible error: %+v %v", result, err)
	}
	call("Read", map[string]any{"path": outside})
	call("Grep", map[string]any{"path": outside, "pattern": "immutable"})
	quotedOutside := "'" + strings.ReplaceAll(outside, "'", "''") + "'"
	call("RunCommand", map[string]any{"command": guardianNativeTestCommand("printf overwritten > '"+strings.ReplaceAll(outside, "'", "'\\''")+"'", "$ErrorActionPreference='Stop'; Set-Content -LiteralPath "+quotedOutside+" -Value overwritten")})
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "immutable evidence\n" {
		t.Fatalf("escaped temporary-only write policy: %q %v", raw, err)
	}
	call("Read", map[string]any{"path": outside})
	root := q.root
	if err := q.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("review resources remain: %v", err)
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
			input, _ := json.Marshal(map[string]string{"command": guardianNativeTestCommand("printf evidence", "Write-Output evidence")})
			message := model.NewMessage(model.RoleAssistant, model.NewToolUsePart("query-1", "RunCommand", input))
			yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
			return
		}
		if n == 2 {
			for _, message := range req.Messages {
				for _, part := range message.Parts {
					if part.ToolResult == nil || part.ToolResult.ToolUseID != "query-1" {
						continue
					}
					for _, content := range part.ToolResult.Content {
						if content.JSON == nil {
							continue
						}
						var result struct {
							Stdout   string `json:"stdout"`
							ExitCode int    `json:"exit_code"`
						}
						if err := json.Unmarshal(content.JSON.Value, &result); err == nil && !part.ToolResult.IsError && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "evidence" {
							yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"option_id":"allow_once"}`), StepComplete: true, TurnComplete: true}), nil)
							return
						}
					}
				}
			}
			yield(nil, errors.New("Guardian did not receive successful native command evidence"))
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
	defer reviewer.Close()
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
		command := guardianNativeTestCommand("curl --noproxy '*' --max-time 2 -fsS "+server.URL, "$client = New-Object System.Net.WebClient; $client.Proxy = $null; $client.DownloadString('"+server.URL+"')")
		raw, _ := json.Marshal(map[string]any{"command": command})
		result, err := (guardianQueryTool{q, "RunCommand"}).Call(t.Context(), tool.Call{ID: "network", Input: raw})
		_ = q.close()
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "network-evidence") != (runtime.GOOS == "windows" || network == sandbox.NetworkEnabled) {
			t.Fatalf("network=%s result=%s", network, encoded)
		}
	}
}

func TestGuardianEvidenceVolumeDoesNotBlockContinuation(t *testing.T) {
	q := &guardianQueries{bytes: 240 * 1024}
	for i := 0; i < 12; i++ {
		if err := q.admit(t.Context(), &model.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if q.attempts != 12 {
		t.Fatalf("attempts=%d", q.attempts)
	}
}

func TestGuardianBoundedResultReportsMissingContentAndPreservesError(t *testing.T) {
	for _, failed := range []bool{false, true} {
		q := &guardianQueries{resultBytes: 4096}
		raw, err := json.Marshal(map[string]any{"stdout": strings.Repeat("large observed output\n", 10000), "exit_code": 7, "stderr": "inspection diagnostic"})
		if err != nil {
			t.Fatal(err)
		}
		result := q.boundResult(tool.Result{ID: "inspection", Name: "RunCommand", IsError: failed, Content: []model.Part{model.NewJSONPart(raw)}})
		if result.ID != "inspection" || result.IsError != failed || q.truncated != 1 || tool.ResultNeedsTruncation(result, tool.TruncationPolicy{MaxBytes: q.resultBytes}) {
			t.Fatalf("bounded result lost identity, outcome or budget: %+v", result)
		}
		encoded, _ := json.Marshal(result.Content)
		if (!strings.Contains(string(encoded), "truncated") && !strings.Contains(string(encoded), "omitted")) || strings.Contains(string(encoded), "evidence_ref") {
			t.Fatalf("missing content was not reported directly: %s", encoded)
		}
	}
}

func guardianNativeTestCommand(posix, powershell string) string {
	if runtime.GOOS == "windows" {
		return powershell
	}
	return posix
}
