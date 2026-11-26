package gojaexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func TestFreshWorkerAsync(t *testing.T) {
	t.Parallel()
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{CallID: "original", Outcome: codemode.OutcomeSucceeded, Value: json.RawMessage(`{"n":17}`)}, nil
	}}
	e := Executor{Observe: func(m Metrics) {
		if !m.Reaped || m.Killed || m.HostCalls != 2 {
			t.Errorf("metrics: %+v", m)
		}
	}}
	code := `globalThis.saved=42;const results=await Promise.all([tools.call("Read",{}),tools.call("Read",{})]);return {value:results[0].data.n,apis:[typeof process,typeof require,typeof fetch]};`
	result, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil || !strings.Contains(string(result.Value), `"value":17`) || !strings.Contains(string(result.Value), "undefined") {
		t.Fatalf("%s %v", result.Value, err)
	}
	e.Observe = nil
	result, err = e.Execute(t.Context(), fixed(`return typeof saved;`), codemode.Limits{}, h)
	if err != nil || string(result.Value) != `"undefined"` {
		t.Fatalf("heap retained: %s %v", result.Value, err)
	}
}

func TestCancelInterruptAndCPU(t *testing.T) {
	t.Parallel()
	for _, cancelEarly := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "cpu"}[cancelEarly], func(t *testing.T) {
			ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
			defer stop()
			var metrics Metrics
			e := Executor{Observe: func(m Metrics) { metrics = m }}
			if cancelEarly {
				time.AfterFunc(100*time.Millisecond, stop)
			}
			_, err := e.Execute(ctx, fixed(`while(true){}`), codemode.Limits{}, testHost{})
			if err == nil || !metrics.Reaped {
				t.Fatalf("err=%v metrics=%+v", err, metrics)
			}
			if cancelEarly {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("worker did not preserve cancellation: %v", err)
				}
			} else if !strings.Contains(err.Error(), "exceeded 1s CPU budget") || metrics.CPU <= 0 || metrics.CPU > 2*CPUBudget {
				// CPU admission does not promise wall latency under concurrent
				// test load. Require the budget cause and actual child CPU usage.
				t.Fatalf("worker did not enforce its CPU budget: %v; %+v", err, metrics)
			}
		})
	}
}

func TestHostWaitDoesNotConsumeCPUBudget(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	e := Executor{}
	if _, err := e.Execute(t.Context(), fixed(`return {};`), codemode.Limits{}, testHost{}); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{`return "x".repeat(1100000);`, `return 1n;`} {
		if _, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
			t.Error("oversize request dispatched")
			return codemode.Reply{}, nil
		}}); err == nil {
			t.Errorf("accepted: %s", code)
		}
	}
}

func TestBodyProjectionAndOriginalReturn(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"n":9223372036854775807,"content":"retained payload"}`)
	reply := codemode.Reply{CallID: "original-receipt", Outcome: codemode.OutcomeSucceeded, Value: raw, Parts: []codemode.Part{{Kind: "json", JSON: raw}}}
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) { return reply, nil }}
	code := `Object.defineProperty=()=>{throw Error("untrusted override")};const r=await tools.call("Read",{});return {reply:r,spread:{...r},content:r.data.content};`
	result, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Reply   map[string]json.RawMessage
		Spread  map[string]json.RawMessage
		Content string
	}
	if err = json.Unmarshal(result.Value, &got); err != nil {
		t.Fatal(err)
	}
	if got.Content != "retained payload" || got.Reply["data"] == nil || string(got.Reply["text"]) != `""` || string(got.Reply["media"]) != `[]` || len(got.Reply) != 3 || string(got.Spread["data"]) != string(got.Reply["data"]) || strings.Contains(string(result.Value), "9223372036854775807") {
		t.Fatalf("unexpected public Body: %s", result.Value)
	}
	result, err = (&Executor{}).Execute(t.Context(), fixed(`return await tools.call("Read",{});`), codemode.Limits{}, h)
	if err != nil || !result.Reply || result.CallID != reply.CallID || len(result.Parts) != 0 || len(result.Value) != 0 {
		t.Fatalf("original receipt lost: %+v, %v", result, err)
	}
}

func TestFailedReplyPreservesOutcomeClassification(t *testing.T) {
	t.Parallel()
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

func TestArgumentQuotingUsesPristineStringEncoder(t *testing.T) {
	t.Parallel()
	const code = `JSON.stringify = () => "wrong";
	const key = 'a"\\b';
	await tools.call("Write", {[key]: "line\n\t\u0000中文😀", lone: "\ud800"});
	return true;`
	var raw json.RawMessage
	host := testHost{call: func(_ context.Context, _ string, args json.RawMessage) (codemode.Reply, error) {
		raw = args
		return codemode.Reply{Outcome: codemode.OutcomeSucceeded}, nil
	}}
	e := Executor{}
	if _, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, host); err != nil {
		t.Fatal(err)
	}
	var args map[string]string
	if err := json.Unmarshal(raw, &args); err != nil || args["a\"\\b"] != "line\n\t\x00中文😀" || !strings.Contains(string(raw), `\ud800`) {
		t.Fatalf("invalid quoted arguments: %s (%v)", raw, err)
	}
}
