package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type asyncMeasurement struct {
	Evidence         string             `json:"evidence"`
	Mode             string             `json:"mode"`
	ForegroundWaitMS int64              `json:"foreground_wait_ms"`
	GenerationMS     int64              `json:"generation_ms"`
	AdoptionMS       int64              `json:"adoption_ms"`
	Usage            model.Usage        `json:"usage"`
	Attempts         []model.Invocation `json:"attempts,omitempty"`
	Answer           string             `json:"answer,omitempty"`
}

func writeAsyncMeasurements(t *testing.T, measurements []asyncMeasurement) {
	t.Helper()
	data, err := json.MarshalIndent(measurements, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if path := os.Getenv("CAELIS_ASYNC_POC_REPORT"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// This measures controlled scheduling, not provider performance. A 300 ms
// summary delay is inserted in both runs; input/output usage is synthetic.
func TestAsyncCompactionFixtureMeasurement(t *testing.T) {
	var measurements []asyncMeasurement
	for _, mode := range []string{"synchronous", "async"} {
		h := newAsyncHarness(t, "")
		if mode == "synchronous" {
			h.rt.compaction.Async.Enabled = false
			h.rt.compaction.WatermarkRatio = .1
		}
		run := h.run(t)
		receiveSnapshotValue(t, h.llm.foreground)
		start := time.Now()
		h.llm.responses <- asyncToolResponse("first")
		receiveSnapshotValue(t, h.llm.background)
		generationStart := time.Now()
		var next asyncProbeRequest
		if mode == "async" {
			next = receiveSnapshotValue(t, h.llm.foreground)
		}
		foregroundWait := time.Since(start)
		// Only the test's response scheduler sleeps; production never waits for
		// speculative readiness at an ordinary model request boundary.
		time.Sleep(300 * time.Millisecond)
		h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 2100)
		if mode == "async" {
			receiveSnapshotValue(t, next.scope.job.done)
		} else {
			next = receiveSnapshotValue(t, h.llm.foreground)
			foregroundWait = time.Since(start)
		}
		generation := time.Since(generationStart)
		h.llm.responses <- asyncToolResponse("second")
		receiveSnapshotValue(t, h.secondTool)
		adoptionStart := time.Now()
		close(h.releaseSecond)
		receiveSnapshotValue(t, h.llm.foreground)
		adoption := time.Since(adoptionStart)
		h.llm.responses <- asyncTextResponse("done", 500)
		if err := run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
		measurements = append(measurements, asyncMeasurement{
			Evidence: "fixture; 300ms injected generation delay; synthetic usage", Mode: mode,
			ForegroundWaitMS: foregroundWait.Milliseconds(), GenerationMS: generation.Milliseconds(), AdoptionMS: adoption.Milliseconds(),
			Usage: model.Usage{Reported: true, PromptTokens: 2100, CompletionTokens: 16, TotalTokens: 2116},
		})
	}
	writeAsyncMeasurements(t, measurements)
}

// asyncLiveModel uses a real provider only for candidate generation. Foreground
// steps remain controlled so timing and fault interleavings are reproducible.
// Its 10k window is a test trigger, not a claim about the provider's capacity.
type asyncLiveModel struct {
	*asyncProbeModel
	provider model.LLM
	started  chan struct{}
}

func (m *asyncLiveModel) Name() string { return m.provider.Name() }
func (m *asyncLiveModel) ProviderName() string {
	return m.provider.(interface{ ProviderName() string }).ProviderName()
}
func (*asyncLiveModel) TracksInvocations() {}
func (m *asyncLiveModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if isRuntimeCompactionRequest(req) {
		select {
		case m.started <- struct{}{}:
		default:
		}
		return model.Generate(ctx, m.provider, req)
	}
	return model.Generate(ctx, m.asyncProbeModel, req)
}

// Run explicitly with CAELIS_ASYNC_LIVE=1 and DEEPSEEK_API_KEY. No Host, user
// configuration or production Session is opened. All transmitted text is the
// synthetic fixture below, the fixed compaction prompt, and inert tool specs.
func TestAsyncCompactionLiveProvider(t *testing.T) {
	if os.Getenv("CAELIS_ASYNC_LIVE") != "1" {
		t.Skip("opt-in provider experiment")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is unavailable")
	}
	factory := providers.NewFactory()
	if err := factory.Register(providers.Config{
		Alias: "async-live", Provider: "deepseek", API: providers.APIDeepSeek, Model: "deepseek-flash",
		BaseURL: "https://api.deepseek.com/anthropic", Auth: providers.AuthConfig{Type: providers.AuthAPIKey, Token: key},
		Timeout: 45 * time.Second, MaxOutputTok: 1200, ContextWindowTokens: 10000, ReasoningEffort: "none",
	}); err != nil {
		t.Fatal(err)
	}
	provider, err := factory.NewByAlias("async-live")
	if err != nil {
		t.Fatal(err)
	}
	h := newAsyncHarness(t, "")
	h.rt.compaction.Async.GenerationTimeout = 45 * time.Second
	semantic := `Synthetic audit case. User goal: prepare an ORBIT deployment draft. Preserve the exact code COBALT-731 and budget cap 17 units. Earlier permission to publish is revoked; do not publish or send anything. The question about target region remains unanswered: a host closure notice is not an answer or approval. A remote write with receipt action-42 has unknown outcome; reconcile only that original receipt, never retry under a new ID. Local verification completed, but the report has not yet been delivered. A later note asking for a concise answer does not replace these requirements.`
	if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: userTextEvent(semantic)}); err != nil {
		t.Fatal(err)
	}
	live := &asyncLiveModel{asyncProbeModel: h.llm, provider: provider, started: make(chan struct{}, 4)}
	h.source.set(agent.ModelRequestSnapshot{Revision: "live-epoch", Model: live, Tools: []tool.Tool{h.tool}, Reasoning: model.ReasoningConfig{Effort: "none"}})
	run := h.run(t)
	receiveSnapshotValue(t, h.llm.foreground)
	start := time.Now()
	h.llm.responses <- asyncToolResponse("first")
	next := receiveSnapshotValue(t, h.llm.foreground)
	foregroundWait := time.Since(start)
	receiveSnapshotValue(t, live.started)
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	wait, cancel := context.WithTimeout(t.Context(), 55*time.Second)
	defer cancel()
	select {
	case <-next.scope.job.done:
	case <-wait.Done():
		t.Fatal(wait.Err())
	}
	if next.scope.job.err != nil || next.scope.job.receiptErr != nil {
		t.Fatalf("provider generation=%v receipt=%v", next.scope.job.err, next.scope.job.receiptErr)
	}
	generation := time.Since(start)
	adoptionStart := time.Now()
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground).request
	adoption := time.Since(adoptionStart)
	h.llm.responses <- asyncTextResponse("fixture foreground complete", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkpoint, _, ok := compact.LatestCompactEvent(h.load(t).Events)
	if !ok {
		t.Fatal("real candidate was not adopted")
	}
	var measurements []asyncMeasurement
	for _, event := range h.load(t).Events {
		if session.IsModelInvocationReceipt(event) && event.Invocation.Provider == "deepseek" {
			measurements = append(measurements, asyncMeasurement{Evidence: "real provider candidate; scripted foreground; synthetic 10k window", Mode: "async", ForegroundWaitMS: foregroundWait.Milliseconds(), GenerationMS: generation.Milliseconds(), AdoptionMS: adoption.Milliseconds(), Answer: session.EventText(checkpoint), Usage: usageFromAsyncReceipt(event)})
		}
	}
	question := `Return a JSON object with keys objective, code, budget, publish_allowed, region_answered, receipt_id, receipt_status, retry_new_id_allowed, report_delivered. Answer from the conversation evidence; do not invent approval or success.`
	for _, input := range []struct {
		name    string
		request *model.Request
	}{{"original", next.request}, {"compacted", after}} {
		request := model.CloneRequest(input.request)
		request.Tools = nil
		request.DisableTools = true
		request.Messages = append(request.Messages, model.NewTextMessage(model.RoleUser, question))
		measurement := asyncMeasurement{Evidence: "real provider semantic probe; synthetic evidence; manual comparison required", Mode: input.name}
		ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { measurement.Attempts = append(measurement.Attempts, in) })
		probeStart := time.Now()
		for event, err := range model.Generate(ctx, provider, request) {
			if err != nil {
				t.Fatal(err)
			}
			if event != nil && event.Response != nil && event.TurnComplete {
				measurement.Answer = event.Response.Message.TextContent()
				measurement.Usage = event.Usage
			}
		}
		measurement.GenerationMS = time.Since(probeStart).Milliseconds()
		if strings.TrimSpace(measurement.Answer) == "" {
			t.Fatal("empty semantic probe")
		}
		measurements = append(measurements, measurement)
	}
	writeAsyncMeasurements(t, measurements)
}

func usageFromAsyncReceipt(event *session.Event) model.Usage {
	usage := event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["usage"]
	data, err := json.Marshal(usage)
	if err != nil {
		panic(fmt.Sprintf("fixture receipt encoding: %v", err))
	}
	var out model.Usage
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	out.Reported = true
	return out
}
