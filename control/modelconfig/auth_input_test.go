package modelconfig

import (
	"context"
	"errors"
	"testing"
)

func TestRequestAuthInput(t *testing.T) {
	t.Parallel()

	requested := AuthInputRequest{}
	ctx := WithAuthInput(context.Background(), func(_ context.Context, request AuthInputRequest) (string, error) {
		requested = request
		return "code-1", nil
	})
	got, err := RequestAuthInput(ctx, AuthInputRequest{
		Provider: " xai ",
		Prompt:   " Paste code ",
		Secret:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "code-1" || requested.Provider != "xai" || requested.Prompt != "Paste code" || !requested.Secret {
		t.Fatalf("RequestAuthInput() = %q, %#v", got, requested)
	}
}

func TestRequestAuthInputUnavailable(t *testing.T) {
	t.Parallel()

	if _, err := RequestAuthInput(context.Background(), AuthInputRequest{}); !errors.Is(err, ErrAuthInputUnavailable) {
		t.Fatalf("RequestAuthInput() error = %v, want ErrAuthInputUnavailable", err)
	}
}
