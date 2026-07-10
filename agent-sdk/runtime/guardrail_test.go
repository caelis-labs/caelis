package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestGuardrailsApplyMutationsInConfiguredOrder(t *testing.T) {
	t.Parallel()

	var observed []agent.GuardrailInput
	first := guardrailFunc{name: "first", apply: func(_ context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
		observed = append(observed, input)
		input.Input += "-first"
		input.ContentParts = append(input.ContentParts, model.ContentPart{Type: model.ContentPartText, Text: "first"})
		return input, nil
	}}
	second := guardrailFunc{name: "second", apply: func(_ context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
		observed = append(observed, input)
		input.Input += "-second"
		return input, nil
	}}
	runtime, active := newGuardrailRuntime(t, agent.GuardrailSpec{Guardrail: first}, agent.GuardrailSpec{Guardrail: second})
	request := agent.RunRequest{
		SessionRef:   active.SessionRef,
		Input:        "input",
		DisplayInput: "display",
		ContentParts: []model.ContentPart{{Type: model.ContentPartText, Text: "original"}},
	}
	got, err := runtime.applyGuardrails(context.Background(), active, request)
	if err != nil {
		t.Fatalf("applyGuardrails() error = %v", err)
	}
	want := request
	want.Input = "input-first-second"
	want.ContentParts = append(want.ContentParts, model.ContentPart{Type: model.ContentPartText, Text: "first"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
	if len(observed) != 2 || observed[1].Input != "input-first" {
		t.Fatalf("observed = %#v, want ordered prior mutation", observed)
	}
	observed[0].ContentParts[0].Text = "mutated"
	if got.ContentParts[0].Text != "original" {
		t.Fatal("guardrail input retained an external slice reference")
	}
}

func TestGuardrailTimeoutAndFailurePolicy(t *testing.T) {
	t.Parallel()

	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	blocking := guardrailFunc{name: "blocking", apply: func(_ context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
		<-blocked
		return input, nil
	}}
	mutating := guardrailFunc{name: "after", apply: func(_ context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
		input.Input += "-after"
		return input, nil
	}}
	tests := []struct {
		name      string
		policy    agent.GuardrailFailurePolicy
		wantErr   bool
		wantInput string
	}{
		{name: "fail closed", policy: agent.GuardrailFailClosed, wantErr: true},
		{name: "fail open", policy: agent.GuardrailFailOpen, wantInput: "input-after"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, active := newGuardrailRuntime(t,
				agent.GuardrailSpec{Guardrail: blocking, Timeout: 10 * time.Millisecond, OnFailure: tt.policy},
				agent.GuardrailSpec{Guardrail: mutating},
			)
			got, err := runtime.applyGuardrails(context.Background(), active, agent.RunRequest{SessionRef: active.SessionRef, Input: "input"})
			if tt.wantErr {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error = %v, want deadline", err)
				}
				return
			}
			if err != nil || got.Input != tt.wantInput {
				t.Fatalf("applyGuardrails() = input %q, err %v; want %q", got.Input, err, tt.wantInput)
			}
		})
	}
}

func TestGuardrailRejectionNeverFailsOpen(t *testing.T) {
	t.Parallel()

	rejecting := guardrailFunc{name: "reject", apply: func(_ context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
		return input, &agent.GuardrailRejectionError{Guardrail: "reject", Reason: "unsafe"}
	}}
	runtime, active := newGuardrailRuntime(t, agent.GuardrailSpec{Guardrail: rejecting, OnFailure: agent.GuardrailFailOpen})
	_, err := runtime.applyGuardrails(context.Background(), active, agent.RunRequest{SessionRef: active.SessionRef, Input: "input"})
	var rejection *agent.GuardrailRejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("error = %v, want GuardrailRejectionError", err)
	}
}

func newGuardrailRuntime(t *testing.T, specs ...agent.GuardrailSpec) (*Runtime, session.Session) {
	t.Helper()
	sessions, active := newTestSessionService(t, "guardrail-"+t.Name())
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Guardrails: specs})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runtime, active
}

type guardrailFunc struct {
	name  string
	apply func(context.Context, agent.GuardrailInput) (agent.GuardrailInput, error)
}

func (g guardrailFunc) Name() string { return g.name }

func (g guardrailFunc) ApplyGuardrail(ctx context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
	return g.apply(ctx, input)
}
