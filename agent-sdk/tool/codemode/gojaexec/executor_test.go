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
func (testHost) Emit(context.Context, string) error { return nil }
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
	if err != nil || !strings.Contains(string(result), "9223372036854775807") || !strings.Contains(string(result), "undefined") {
		t.Fatalf("%s %v", result, err)
	}
	e.Observe = nil
	result, err = e.Execute(t.Context(), fixed(`return typeof saved;`), codemode.Limits{}, h)
	if err != nil || string(result) != `"undefined"` {
		t.Fatalf("heap retained: %s %v", result, err)
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
	code := `Object.defineProperty=()=>{throw Error("untrusted override")};const r=await tools.call("Read",{});output.text(r);output.text(r.raw_json);output.text({...r,raw_json:r.raw_json});return {keys:Object.keys(r),spread:{...r},stringified:JSON.stringify(r),content:r.parts[0].json.content};`
	result, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Output []json.RawMessage `json:"output"`
		Result struct {
			Keys        []string                   `json:"keys"`
			Spread      map[string]json.RawMessage `json:"spread"`
			Stringified string                     `json:"stringified"`
			Content     string                     `json:"content"`
		} `json:"result"`
	}
	if err = json.Unmarshal(result, &got); err != nil {
		t.Fatal(err)
	}
	var explicit string
	if len(got.Output) != 3 || json.Unmarshal(got.Output[1], &explicit) != nil || explicit != string(raw) {
		t.Fatalf("lossless explicit raw JSON lost: %s", result)
	}
	for _, key := range got.Result.Keys {
		if key == "raw_json" {
			t.Fatal("raw_json became enumerable")
		}
	}
	var defaultOutput map[string]json.RawMessage
	_ = json.Unmarshal(got.Output[0], &defaultOutput)
	if defaultOutput["raw_json"] != nil || got.Result.Spread["raw_json"] != nil || strings.Contains(got.Result.Stringified, "raw_json") || got.Result.Content != "retained payload" || defaultOutput["parts"] == nil {
		t.Fatalf("default output duplicated raw JSON or lost parts: %s", result)
	}
	if !strings.Contains(string(got.Output[0]), "9223372036854776000") {
		t.Fatalf("unexpected JS Number semantics: %s", got.Output[0])
	}
	t.Logf("default_output_bytes=%d explicit_raw_bytes=%d enumerable_equivalent_bytes=%d; UTF-8 JSON bytes, not model tokens", len(got.Output[0]), len(explicit), len(got.Output[2]))
}
