package gojaexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == WorkerArgument {
		os.Exit(WorkerMain())
	}
	os.Exit(m.Run())
}

type testHost struct {
	call func(context.Context, string, json.RawMessage) (codemode.Reply, error)
}

func (h testHost) Call(ctx context.Context, n string, a json.RawMessage) (codemode.Reply, error) {
	return h.call(ctx, n, a)
}
func fixed(code string) codemode.Source {
	sum := sha256.Sum256([]byte(code))
	return codemode.Source{Code: code, SHA256: hex.EncodeToString(sum[:])}
}

func TestFreshWorkerAsyncAndRawJSON(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{CallID: "original", Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "json", JSON: json.RawMessage(`{"n":9223372036854775807}`)}}}, nil
	}}
	e := Executor{Observe: func(m Metrics) {
		if !m.Reaped || m.Killed || m.HostCalls != 2 {
			t.Errorf("metrics: %+v", m)
		}
	}}
	code := `globalThis.saved=42;const results=await Promise.all([tools.call("Read",{}),tools.call("Read",{})]);return {raw:results[0].raw_json,rounded:results[0].parts[0].json.n,apis:[typeof process,typeof require,typeof fetch]};`
	result, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil || !strings.Contains(string(result.Value), "9223372036854775807") || !strings.Contains(string(result.Value), "undefined") {
		t.Fatalf("%s %v", result.Value, err)
	}
	e.Observe = nil
	result, err = e.Execute(t.Context(), fixed(`return typeof saved;`), codemode.Limits{}, h)
	if err != nil || string(result.Value) != `"undefined"` {
		t.Fatalf("heap retained: %s %v", result.Value, err)
	}
}

func TestCancelInterruptAndCPU(t *testing.T) {
	for _, cancelEarly := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "cpu"}[cancelEarly], func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			var metrics Metrics
			e := Executor{Observe: func(m Metrics) { metrics = m }}
			if cancelEarly {
				time.AfterFunc(100*time.Millisecond, stop)
			}
			_, err := e.Execute(ctx, fixed(`while(true){}`), codemode.Limits{}, testHost{})
			if err == nil || !metrics.Reaped || metrics.Wall > 4*time.Second {
				t.Fatalf("err=%v metrics=%+v", err, metrics)
			}
		})
	}
}

func TestHostWaitDoesNotConsumeCPUBudget(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		time.Sleep(1200 * time.Millisecond)
		return codemode.Reply{}, nil
	}}
	e := Executor{}
	if _, err := e.Execute(t.Context(), fixed(`await tools.call("Wait",{});return {};`), codemode.Limits{}, h); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetsAndDefaultUnboundedHeap(t *testing.T) {
	e := Executor{}
	if _, err := e.Execute(t.Context(), fixed(`return {};`), codemode.Limits{}, testHost{}); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{`await tools.call("Read",{x:"a".repeat(70000)});return {};`, `return "x".repeat(1100000);`, `return 1n;`} {
		if _, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
			t.Error("oversize request dispatched")
			return codemode.Reply{}, nil
		}}); err == nil {
			t.Errorf("accepted: %s", code)
		}
	}
}

func TestRawJSONIsExplicitAndDefaultOutputDoesNotDuplicate(t *testing.T) {
	reply := codemode.Reply{CallID: "original-receipt", Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "json", JSON: json.RawMessage(`{"n":9223372036854775807,"content":"retained payload"}`)}}}
	raw, _ := json.Marshal(reply)
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) { return reply, nil }}
	// Mutating the global intrinsic cannot replace the trusted decoder's captured
	// defineProperty. Explicit access remains exact; ordinary enumeration omits it.
	code := `Object.defineProperty=()=>{throw Error("untrusted override")};const r=await tools.call("Read",{});return {reply:r,raw:r.raw_json,keys:Object.keys(r),spread:{...r},stringified:JSON.stringify(r),content:r.parts[0].json.content,removed:[typeof output,typeof tools.describe]};`
	result, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Reply       map[string]json.RawMessage `json:"reply"`
		Raw         string                     `json:"raw"`
		Keys        []string                   `json:"keys"`
		Spread      map[string]json.RawMessage `json:"spread"`
		Stringified string                     `json:"stringified"`
		Content     string                     `json:"content"`
		Removed     []string                   `json:"removed"`
	}
	if err = json.Unmarshal(result.Value, &got); err != nil {
		t.Fatal(err)
	}
	if got.Raw != string(raw) || got.Reply["raw_json"] != nil || got.Spread["raw_json"] != nil || strings.Contains(got.Stringified, "raw_json") || got.Content != "retained payload" || got.Reply["parts"] == nil || got.Reply["call_id"] != nil {
		t.Fatalf("unexpected public reply: %s", result.Value)
	}
	if strings.Join(got.Removed, ",") != "undefined,undefined" || !strings.Contains(string(got.Reply["parts"]), "9223372036854776000") {
		t.Fatalf("interface/Number semantics: %s", result.Value)
	}
}

func TestFailedReplyPreservesOutcomeClassification(t *testing.T) {
	for _, outcome := range []string{codemode.OutcomeFailed, codemode.OutcomeUnknown} {
		t.Run(outcome, func(t *testing.T) {
			h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
				return codemode.Reply{Outcome: outcome, IsError: true, Parts: []codemode.Part{{Kind: "json", JSON: json.RawMessage(`{"error":"rejected before dispatch"}`)}}}, nil
			}}
			_, err := (&Executor{}).Execute(t.Context(), fixed(`return await tools.call("Target",{});`), codemode.Limits{}, h)
			if err == nil {
				t.Fatal("failed reply became success")
			}
			unknown := strings.Contains(strings.ToLower(err.Error()), "unknown outcome")
			if outcome == codemode.OutcomeFailed && !strings.Contains(err.Error(), "rejected before dispatch") {
				t.Fatal("original factual error lost")
			}
			if unknown != (outcome == codemode.OutcomeUnknown) {
				t.Fatalf("outcome=%s error=%v", outcome, err)
			}
		})
	}
}
