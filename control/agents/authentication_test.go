package agents

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAuthenticationInteractionContext(t *testing.T) {
	t.Parallel()

	selection := AuthenticationSelectionRequest{
		AgentID: "codex",
		Methods: []AuthenticationMethod{{ID: "browser", Name: "Browser", Type: AuthenticationAgent}},
	}
	terminal := TerminalAuthenticationRequest{
		AgentID: "codex", MethodID: "terminal", Command: "codex-acp",
		Args: []string{"login"}, Env: map[string]string{"LOGIN": "1"},
	}
	ctx := WithAuthenticationSelection(context.Background(), func(_ context.Context, got AuthenticationSelectionRequest) (string, error) {
		if !reflect.DeepEqual(got, selection) {
			t.Fatalf("selection request = %#v, want %#v", got, selection)
		}
		return "browser", nil
	})
	ctx = WithTerminalAuthentication(ctx, func(_ context.Context, got TerminalAuthenticationRequest) error {
		if !reflect.DeepEqual(got, terminal) {
			t.Fatalf("terminal request = %#v, want %#v", got, terminal)
		}
		return nil
	})
	if got, err := RequestAuthenticationSelection(ctx, selection); err != nil || got != "browser" {
		t.Fatalf("RequestAuthenticationSelection() = %q, %v", got, err)
	}
	if !TerminalAuthenticationAvailable(ctx) {
		t.Fatal("TerminalAuthenticationAvailable() = false")
	}
	if err := RequestTerminalAuthentication(ctx, terminal); err != nil {
		t.Fatalf("RequestTerminalAuthentication() error = %v", err)
	}
}

func TestAuthenticationInteractionUnavailable(t *testing.T) {
	t.Parallel()

	if _, err := RequestAuthenticationSelection(context.Background(), AuthenticationSelectionRequest{}); !errors.Is(err, ErrAuthenticationSelectionUnavailable) {
		t.Fatalf("selection error = %v", err)
	}
	if err := RequestTerminalAuthentication(context.Background(), TerminalAuthenticationRequest{}); !errors.Is(err, ErrTerminalAuthenticationUnavailable) {
		t.Fatalf("terminal error = %v", err)
	}
}

func TestNormalizeAuthenticationDefaultsMissingTypeAndPreservesInvalidTypeForValidation(t *testing.T) {
	t.Parallel()

	if got := NormalizeAuthentication(Authentication{MethodID: "browser"}); got != (Authentication{
		MethodID: "browser",
		Type:     AuthenticationAgent,
	}) {
		t.Fatalf("NormalizeAuthentication(missing type) = %#v", got)
	}
	invalid := NormalizeAuthentication(Authentication{MethodID: "browser", Type: "unexpected"})
	if invalid.MethodID != "browser" || invalid.Type != "unexpected" {
		t.Fatalf("NormalizeAuthentication(invalid type) = %#v, must not silently drop selection", invalid)
	}
	if err := ValidateAuthentication(invalid); err == nil {
		t.Fatal("ValidateAuthentication(invalid type) error = nil")
	}
}

func TestCloneAuthenticationMethodsDetachesNestedState(t *testing.T) {
	t.Parallel()

	input := []AuthenticationMethod{{
		ID:   " terminal ",
		Name: " Terminal ",
		Type: AuthenticationTerminal,
		Args: []string{"login"},
		Env:  map[string]string{"LOGIN": "1"},
	}}
	cloned := CloneAuthenticationMethods(input)
	cloned[0].Args[0] = "changed"
	cloned[0].Env["LOGIN"] = "changed"
	if input[0].Args[0] != "login" || input[0].Env["LOGIN"] != "1" {
		t.Fatalf("CloneAuthenticationMethods() aliased input: %#v", input)
	}
	if cloned[0].ID != "terminal" || cloned[0].Name != "Terminal" {
		t.Fatalf("CloneAuthenticationMethods() = %#v, want normalized descriptor", cloned)
	}
}
