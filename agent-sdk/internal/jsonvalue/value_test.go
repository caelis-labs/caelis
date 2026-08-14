package jsonvalue

import (
	"errors"
	"math"
	"testing"
)

func TestValidateRejectsNonJSONValues(t *testing.T) {
	t.Parallel()

	cycle := map[string]any{}
	cycle["self"] = cycle
	tests := []any{
		map[string]any{"number": math.NaN()},
		map[string]any{"callback": func() {}},
		map[string]any{"cycle": cycle},
		map[string]any{"object": map[int]string{1: "one"}},
	}
	for _, value := range tests {
		err := Validate(value)
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("Validate(%T) error = %v, want *ValidationError", value, err)
		}
	}
}

func TestCloneRecursivelyIsolatesJSONValues(t *testing.T) {
	t.Parallel()

	original := map[string]any{"nested": []any{map[string]any{"value": "original"}}}
	cloned := CloneMap(original)
	cloned["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
	if got := original["nested"].([]any)[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("CloneMap() leaked nested mutation: %v", got)
	}
}
