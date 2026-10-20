package jsonvalue

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
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

func TestBytePayloadValidationAndCloning(t *testing.T) {
	t.Parallel()

	type namedBytes []byte
	for _, value := range []any{
		[]byte(nil), []byte{}, []byte{0, 255},
		namedBytes(nil), namedBytes{}, namedBytes{0, 255},
		json.RawMessage(nil), json.RawMessage(`{"integer":900719925474099312345678901234567890,"decimal":1.00}`),
		[4]byte{0, 1, 254, 255},
	} {
		if err := Validate(value); err != nil {
			t.Fatalf("Validate(%T) = %v", value, err)
		}
		cloned := Clone(value)
		if reflect.TypeOf(cloned) != reflect.TypeOf(value) || !reflect.DeepEqual(cloned, value) {
			t.Fatalf("Clone(%T) changed value or type", value)
		}
		before, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(cloned)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("Clone(%T) changed JSON: %s -> %s, %v", value, before, after, err)
		}
		original, copyValue := reflect.ValueOf(value), reflect.ValueOf(cloned)
		if original.Kind() == reflect.Slice && original.Len() > 0 {
			first := original.Index(0).Uint()
			copyValue.Index(0).SetUint(first ^ 255)
			if original.Index(0).Uint() != first {
				t.Fatalf("Clone(%T) shares source bytes", value)
			}
		}
	}
}

func TestBytePayloadValidationStillRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	for _, value := range []any{
		json.RawMessage{}, json.RawMessage(`{"broken":`),
		map[string]any{"nested": []any{json.RawMessage(`NaN`)}},
		invalidByteJSON{1, 2, 3},
	} {
		var validationErr *ValidationError
		if err := Validate(value); !errors.As(err, &validationErr) {
			t.Fatalf("Validate(%T) = %v, want invalid JSON", value, err)
		}
	}
}

type invalidByteJSON []byte

func (invalidByteJSON) MarshalJSON() ([]byte, error) { return []byte(`{"broken":`), nil }

func TestBytePayloadWorkDoesNotAllocatePerByte(t *testing.T) {
	// A byte payload is scalar encoded data. Walking it as a JSON array used
	// to allocate a formatted error path for every byte in every journal read.
	value := map[string]any{"result": json.RawMessage(`"` + strings.Repeat("x", 64<<10) + `"`)}
	for name, operation := range map[string]func(){
		"validate": func() {
			if err := Validate(value); err != nil {
				t.Fatal(err)
			}
		},
		"clone": func() { _ = Clone(value) },
	} {
		t.Run(name, func(t *testing.T) {
			if allocations := testing.AllocsPerRun(5, operation); allocations > 100 {
				t.Fatalf("%s allocated %.0f objects for a byte payload, want bounded overhead", name, allocations)
			}
		})
	}
}

func BenchmarkValidateRawJSON(b *testing.B) {
	value := map[string]any{"result": json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)}
	b.SetBytes(1 << 20)
	b.ReportAllocs()
	for b.Loop() {
		if err := Validate(value); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCloneRawJSON(b *testing.B) {
	value := map[string]any{"result": json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)}
	b.SetBytes(1 << 20)
	b.ReportAllocs()
	for b.Loop() {
		_ = Clone(value)
	}
}
