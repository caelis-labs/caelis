package gatewayapp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type fairAccountingHTTPModel struct {
	url       string
	mode      string
	currentID func() string
}

func (*fairAccountingHTTPModel) Name() string { return "accounting-http-fixture" }
func (m *fairAccountingHTTPModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, nil)
		if err != nil {
			yield(nil, err)
			return
		}
		req.Header.Set("X-Acceptance-Invocation", m.currentID())
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			yield(nil, err)
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if m.mode == "error" {
			yield(nil, errors.New("fixture stream failed"))
			return
		}
		if !yield(&model.StreamEvent{Type: model.StreamEventPartDelta, Response: &model.Response{Usage: model.Usage{PromptTokens: 7}}}, nil) {
			return
		}
		if m.mode == "incomplete" {
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "done"), TurnComplete: true, Usage: model.Usage{PromptTokens: 7, CompletionTokens: 2}}), nil)
	}
}

func TestFairTracePreservesInvocationAccounting(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		for _, mode := range []string{"complete", "error", "incomplete", "consumer-stop"} {
			t.Run(fmt.Sprintf("tracked=%v/%s", tracked, mode), func(t *testing.T) {
				var mu sync.Mutex
				var wireIDs []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					wireIDs = append(wireIDs, r.Header.Get("X-Acceptance-Invocation"))
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
				}))
				defer server.Close()
				var starts, receipts []model.Invocation
				current := ""
				var underlying model.LLM = &fairAccountingHTTPModel{url: server.URL, mode: mode, currentID: func() string { return current }}
				if tracked {
					underlying = model.WithRetry(underlying, model.RetryConfig{MaxRetries: 0})
				}
				trace := &fairTrace{LLM: underlying, root: t.TempDir(), turnCalls: map[int]int{}}
				var wrapper model.LLM = trace
				if mode == "complete" {
					wrapper = &valueTraceModel{fairTrace: trace}
				}
				wrapped := fairPreserveInvocationTracking(wrapper, underlying)
				_, marker := wrapped.(model.InvocationTracker)
				// Check after execution so the original red includes the duplicate receipts.
				ctx := model.WithInvocationStart(t.Context(), func(_ context.Context, in model.Invocation, _ *model.Request) error {
					starts = append(starts, in)
					current = in.ID
					return nil
				})
				ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) { receipts = append(receipts, in) })
				var seenErr error
				for _, err := range model.Generate(ctx, wrapped, &model.Request{Stream: true}) {
					if err != nil {
						seenErr = err
						break
					}
					if mode == "consumer-stop" {
						break
					}
				}
				mu.Lock()
				wire := append([]string(nil), wireIDs...)
				mu.Unlock()
				if len(wire) != 1 || len(receipts) != 1 || len(starts) != 1 || marker != tracked {
					t.Fatalf("HTTP=%v starts=%d receipts=%+v marker=%v want tracked=%v", wire, len(starts), receipts, marker, tracked)
				}
				if wire[0] == "" || wire[0] != receipts[0].ID || starts[0].ID != receipts[0].ID {
					t.Fatalf("original identity mismatch wire=%v receipts=%+v", wire, receipts)
				}
				if mode == "complete" && receipts[0].Outcome != "completed" {
					t.Fatalf("receipt=%+v", receipts[0])
				}
				if (mode == "error" || mode == "incomplete") && seenErr == nil {
					t.Fatal("failure/incomplete stream swallowed")
				}
				t.Logf("HTTP=%d receipt=%s outcome=%s tracked=%v", len(wire), receipts[0].ID, receipts[0].Outcome, marker)
			})
		}
	}
}
