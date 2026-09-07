package providers

import (
	"encoding/json"
	"testing"
)

func TestProviderUsagePresenceDistinguishesMissingAndZero(t *testing.T) {
	for _, raw := range []string{`{}`, `{"usage":null}`, `{"usage":{}}`, `{"usage":{"total_tokens":0}}`} {
		var compat struct {
			Usage openAICompatUsage `json:"usage"`
		}
		var codex struct {
			Usage openAICodexUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(raw), &compat); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &codex); err != nil {
			t.Fatal(err)
		}
		want := raw == `{"usage":{"total_tokens":0}}`
		if compat.Usage.toKernelUsage().IsReported() != want || codex.Usage.toKernelUsage().IsReported() != want {
			t.Fatalf("presence mismatch for %s", raw)
		}
	}
}
