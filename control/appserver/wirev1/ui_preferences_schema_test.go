package wirev1

import "testing"

func TestUIPreferencesSchemaAcceptsSparseZerosAndRejectsInvalidRatios(t *testing.T) {
	validator := openAPIValidator(t, "UIPreferences")
	for _, body := range []string{
		`{}`,
		`{"subagent_layout":""}`,
		`{"subagent_layout":"overlay"}`,
		`{"horizontal_ratio":0}`,
		`{"horizontal_ratio":30}`,
		`{"vertical_ratio":0}`,
		`{"vertical_ratio":70}`,
		`{"theme":""}`,
		`{"theme":"catppuccin","subagent_layout":"","horizontal_ratio":0,"vertical_ratio":0}`,
	} {
		if err := validator.Validate(decodeJSONWithNumbers(t, []byte(body))); err != nil {
			t.Fatalf("accepted body rejected %s: %v", body, err)
		}
	}
	for _, body := range []string{
		`{"horizontal_ratio":1}`,
		`{"horizontal_ratio":29}`,
		`{"horizontal_ratio":71}`,
		`{"vertical_ratio":1}`,
		`{"vertical_ratio":29}`,
		`{"vertical_ratio":71}`,
		`{"subagent_layout":"grid"}`,
	} {
		if err := validator.Validate(decodeJSONWithNumbers(t, []byte(body))); err == nil {
			t.Fatalf("invalid body accepted %s", body)
		}
	}
}
