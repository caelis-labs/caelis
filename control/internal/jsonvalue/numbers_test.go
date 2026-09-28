package jsonvalue

import "testing"

func TestValidateNumbersExaminesEveryTokenInOneJSONValue(t *testing.T) {
	for _, raw := range []string{
		`null`, `true`, `"9007199254740993"`, `9007199254740991`,
		`{"nested":[-9007199254740991,1.25,1e-1000000000]}`,
		`{"id":1,"id":2}`,
	} {
		if err := ValidateNumbers([]byte(raw)); err != nil {
			t.Fatalf("valid in-range JSON %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		``, `{`, `null null`, `1 true`, `NaN`, `01`,
		`9007199254740993`, `-9007199254740993`, `9007199254740991.1`,
		`{"id":9007199254740993,"id":1}`,
		`{"id":{"nested":[9.007199254740993e15]},"id":null}`,
	} {
		if err := ValidateNumbers([]byte(raw)); err == nil {
			t.Fatalf("invalid or out-of-range JSON accepted: %s", raw)
		}
	}
}
