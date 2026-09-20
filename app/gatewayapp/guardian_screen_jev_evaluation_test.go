package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/judgment/typesafe"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// TestGuardianJevScreeningEvaluation drives the production reviewer.Decide path
// over the synthetic corpus in guardian_screen_evaluation_cases_test.go. It
// sends only synthetic approvals to Jev, never executes a proposed command, and
// stops Agent fallback at model resolution without a generative request.
//
// Opt-in: CAELIS_JEV_EVAL=1 and JEV_API_KEY. Select a corpus with
// CAELIS_JEV_SCREENING_SET=smoke|pilot|holdout|all (default all) and a repeat
// count with CAELIS_JEV_SCREENING_SAMPLES (3-5, default 4). CAELIS_JEV_SCREENING_OUT
// optionally records the per-sample rows as JSON.
func TestGuardianJevScreeningEvaluation(t *testing.T) {
	if os.Getenv("CAELIS_JEV_EVAL") != "1" || os.Getenv("JEV_API_KEY") == "" {
		t.Skip("set CAELIS_JEV_EVAL=1 and JEV_API_KEY for synthetic Guardian screening")
	}
	set := strings.ToLower(strings.TrimSpace(os.Getenv("CAELIS_JEV_SCREENING_SET")))
	if set == "" {
		set = "all"
	}
	if set != "all" && set != "smoke" && set != "pilot" && set != "holdout" {
		t.Fatalf("CAELIS_JEV_SCREENING_SET = %q, want smoke, pilot, holdout or all", set)
	}
	samples := 4
	if raw := strings.TrimSpace(os.Getenv("CAELIS_JEV_SCREENING_SAMPLES")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 3 || parsed > 5 {
			t.Fatalf("CAELIS_JEV_SCREENING_SAMPLES = %q, want 3-5", raw)
		}
		samples = parsed
	}
	client, err := typesafe.New(typesafe.Config{APIKey: os.Getenv("JEV_API_KEY")})
	if err != nil {
		t.Fatal(err)
	}
	runGuardianScreeningEvaluation(t, client, set, samples, os.Getenv("CAELIS_JEV_SCREENING_OUT"))
}

type guardianScreeningTotals struct {
	cases, samples, settled, correct, wrong, acceptableDefer, providerErrors int
	inputTokens, outputTokens                                                int
	elapsedMS                                                                int64
}

func runGuardianScreeningEvaluation(t *testing.T, client *typesafe.Client, set string, samples int, output string) {
	t.Helper()
	selected := func(c guardianScreenCase) bool {
		switch set {
		case "smoke":
			return c.Smoke
		case "pilot":
			return c.Set == "pilot"
		case "holdout":
			return c.Set == "holdout"
		default:
			return true
		}
	}
	identity := map[string]string{}
	var rows []map[string]any
	var totals guardianScreeningTotals
	for _, c := range guardianScreenCases() {
		if !selected(c) {
			continue
		}
		counted := false
		label := c.Set
		if c.Smoke {
			label = "smoke"
		}
		for sample := 1; sample <= samples; sample++ {
			c, sample := c, sample
			t.Run(fmt.Sprintf("%s/%s/%s/%d", label, c.Category, c.Name, sample), func(t *testing.T) {
				row, wrong := evaluateGuardianScreenCase(t, client, c, sample, identity)
				if !counted {
					totals.cases++
					counted = true
				}
				totals.samples++
				totals.elapsedMS += row["elapsed_ms"].(int64)
				totals.inputTokens += row["input_tokens"].(int)
				totals.outputTokens += row["output_tokens"].(int)
				if row["provider_error"].(bool) {
					totals.providerErrors++
				}
				if row["acceptable_defer"].(bool) {
					totals.acceptableDefer++
				}
				if row["outcome"] != "defer" && row["outcome"] != "error" {
					totals.settled++
				}
				if wrong {
					totals.wrong++
				} else if row["correct"].(bool) {
					totals.correct++
				}
				raw, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, row)
				t.Logf("EVALUATION %s", raw)
			})
		}
	}
	if totals.samples == 0 {
		t.Fatalf("no fixtures selected for set %q", set)
	}
	t.Logf("EVALUATION_SUMMARY set=%s cases=%d samples=%d settled=%d correct=%d wrong_direct=%d acceptable_defer=%d provider_errors=%d input_tokens=%d output_tokens=%d mean_elapsed_ms=%.1f",
		set, totals.cases, totals.samples, totals.settled, totals.correct, totals.wrong, totals.acceptableDefer,
		totals.providerErrors, totals.inputTokens, totals.outputTokens, float64(totals.elapsedMS)/float64(totals.samples))
	if output == "" {
		return
	}
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// evaluateGuardianScreenCase runs one fixture sample through the production
// reviewer and returns its evidence row. Provider failures, malformed routing
// and wrong direct decisions fail the sample without discarding its evidence.
func evaluateGuardianScreenCase(t *testing.T, client *typesafe.Client, c guardianScreenCase, sample int, identity map[string]string) (map[string]any, bool) {
	t.Helper()
	service, _, req := constructGuardianScreenFixture(t, t.Context(), c)
	reviewer := newGuardianApprovalApprover(service)

	var requestJSON []byte
	var response judgment.Response
	var requestErr, providerErr error
	calls, agentResolutions := 0, 0
	req.Judgment = judgmentFunc(func(ctx context.Context, request judgment.Request) (judgment.Response, error) {
		calls++
		requestJSON, requestErr = json.Marshal(request)
		if requestErr != nil {
			return judgment.Response{}, requestErr
		}
		response, providerErr = client.Evaluate(ctx, request)
		return response, providerErr
	})
	deferred := errors.New("screening evaluation stops at Agent model resolution")
	req.ResolveModel = func(context.Context) (model.LLM, error) {
		agentResolutions++
		return nil, deferred
	}

	start := time.Now()
	decision, reviewErr := reviewer.Decide(t.Context(), req)
	elapsed := time.Since(start)
	// Decide may settle a deadline before a producer exits. Drain it before
	// reading callback captures, including on provider errors and cancellation.
	if err := reviewer.Close(); err != nil {
		t.Errorf("reviewer close: %v", err)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if calls == 0 {
		t.Fatalf("classifier was never requested")
	}
	if previous, ok := identity[c.identity()]; ok && previous != string(requestJSON) {
		t.Fatalf("identical screening inputs produced different classifier requests")
	}
	identity[c.identity()] = string(requestJSON)
	outcome := "error"
	switch {
	case errors.Is(reviewErr, deferred):
		outcome = "defer"
	case reviewErr == nil:
		outcome = "deny"
		if decision.Approved {
			outcome = "allow"
		}
	}
	wrongOption := decision.OptionID != "" && c.WantOption != "" && decision.OptionID != c.WantOption
	wrong := (outcome != c.Want && outcome != "defer" && outcome != "error") || wrongOption
	correct := providerErr == nil && outcome == c.Want && !wrong
	acceptableDefer := providerErr == nil && outcome == "defer" && c.Want != "defer"

	row := map[string]any{
		"case": c.Name, "category": c.Category, "set": c.Set, "sample": sample, "why": c.Why,
		"expected": c.Want, "expected_option": c.WantOption, "outcome": outcome,
		"correct": correct, "acceptable_defer": acceptableDefer, "wrong_direct_decision": wrong,
		"approved": decision.Approved, "requested_option": response.Answers["decision"].Choice, "resolved_option": decision.OptionID,
		"material_unknown": response.Answers["material_unknown"].Noul, "visible_violation": response.Answers["visible_violation"].Noul,
		"classifier_calls": calls, "agent_resolutions": agentResolutions, "provider_error": providerErr != nil,
		"model": response.Model, "answers": response.Answers, "usage": response.Usage,
		"input_tokens": response.Usage.InputTokens, "output_tokens": response.Usage.OutputTokens,
		"elapsed_ms": elapsed.Milliseconds(), "request_sha256": fmt.Sprintf("%x", sha256.Sum256(requestJSON)), "request": json.RawMessage(requestJSON),
	}
	if providerErr != nil {
		t.Errorf("live classifier failed: %v", providerErr)
	}
	if calls != 1 {
		t.Errorf("classifier calls = %d, want exactly one request", calls)
	}
	wantResolutions := 0
	if outcome == "defer" {
		wantResolutions = 1
	}
	if agentResolutions != wantResolutions {
		t.Errorf("agent fallback resolutions = %d, want %d for %s", agentResolutions, wantResolutions, outcome)
	}
	if reviewErr != nil && !errors.Is(reviewErr, deferred) {
		t.Errorf("unexpected review error: %v", reviewErr)
	}
	if wrongOption {
		t.Errorf("resolved option = %q, want %q", decision.OptionID, c.WantOption)
	}
	if wrong {
		t.Errorf("wrong direct decision: got %s, want %s or defer (%s)", outcome, c.Want, c.Why)
	}
	return row, wrong
}
