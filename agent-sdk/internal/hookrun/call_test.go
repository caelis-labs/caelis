package hookrun

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestPureTimeoutDiscardsDetachedLateResult(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(t.Context(), key{}, "private-bearer")
	release := make(chan struct{})
	finished := make(chan struct{})
	h := agent.Hook[model.Message, model.Message]{Name: "late", Timeout: 10 * time.Millisecond, Apply: func(ctx context.Context, _ agent.HookContext, m model.Message) (model.Message, error) {
		defer close(finished)
		if ctx.Value(key{}) != nil {
			t.Error("private context value leaked")
		}
		<-release
		m.Parts[0].Text.Text = "late mutation"
		return m, nil
	}}
	original := model.NewTextMessage(model.RoleUser, "original")
	out, err := Call(ctx, "input", h, agent.HookContext{}, original, model.CloneMessage, model.CloneMessage)
	var hookErr *agent.HookError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &hookErr) || hookErr.Phase != "input" || hookErr.Name != "late" {
		t.Fatalf("error=%v", err)
	}
	close(release)
	<-finished
	if original.TextContent() != "original" || out.TextContent() != "" {
		t.Fatal("late transform changed owner")
	}
}

func TestAroundSingleConsumptionExpiresAndUnwinds(t *testing.T) {
	var effects atomic.Int64
	var retained func(context.Context) (int, error)
	var order []string
	base := func(context.Context) (int, error) { order = append(order, "effect"); effects.Add(1); return 1, nil }
	inner := func(ctx context.Context) (int, error) {
		return Around(ctx, "tool_invoke", "inner", time.Second, func(ctx context.Context, next func(context.Context) (int, error)) (int, error) {
			order = append(order, "inner before")
			v, e := next(ctx)
			order = append(order, "inner after")
			return v, e
		}, base)
	}
	_, err := Around(t.Context(), "tool_invoke", "outer", time.Second, func(ctx context.Context, next func(context.Context) (int, error)) (int, error) {
		retained = next
		order = append(order, "outer before")
		v, e := next(ctx)
		order = append(order, "outer after")
		return v, e
	}, inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = retained(t.Context()); err == nil {
		t.Fatal("retained next remained active")
	}
	if effects.Load() != 1 || !reflect.DeepEqual(order, []string{"outer before", "inner before", "effect", "inner after", "outer after"}) {
		t.Fatalf("effects=%d order=%v", effects.Load(), order)
	}
	_, err = Around(t.Context(), "tool_invoke", "concurrent", time.Second, func(ctx context.Context, next func(context.Context) (int, error)) (int, error) {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() { _, _ = next(ctx) })
		}
		wg.Wait()
		return 0, nil
	}, func(context.Context) (int, error) { effects.Add(1); return 1, nil })
	if err == nil || effects.Load() != 2 {
		t.Fatalf("duplicate consumption effects=%d err=%v", effects.Load(), err)
	}
}

func TestAroundDrainsUncooperativeProducer(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Around(t.Context(), "tool_invoke", "drain", 10*time.Millisecond, func(ctx context.Context, next func(context.Context) (int, error)) (int, error) {
			go func() { _, _ = next(ctx) }()
			<-started
			<-ctx.Done()
			return 0, ctx.Err()
		}, func(context.Context) (int, error) { close(started); <-release; return 1, nil })
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("released live producer: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("timeout hidden")
	}
}

func TestPurePanicIdentifiesPhaseAndName(t *testing.T) {
	h := agent.Hook[int, int]{Name: "panic", Apply: func(context.Context, agent.HookContext, int) (int, error) { panic("broken") }}
	_, err := Call(t.Context(), "model_prepare", h, agent.HookContext{}, 1, func(v int) int { return v }, func(v int) int { return v })
	var e *agent.HookError
	if !errors.As(err, &e) || e.Phase != "model_prepare" || e.Name != "panic" {
		t.Fatalf("panic error=%v", err)
	}
}

func TestAroundDeadlineCannotAcceptLateProducerContent(t *testing.T) {
	for range 20 {
		_, err := Around(t.Context(), "tool_invoke", "deadline", time.Millisecond, func(ctx context.Context, next func(context.Context) (string, error)) (string, error) {
			r, err := next(ctx)
			if err != nil {
				return r, err
			}
			<-ctx.Done()
			return r, nil
		}, func(context.Context) (string, error) { return "unprocessed", nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired extension returned successful content: %v", err)
		}
	}
}
