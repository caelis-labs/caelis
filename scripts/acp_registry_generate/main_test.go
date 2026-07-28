package main

import (
	"bytes"
	"testing"
)

func TestRenderFiltersToNPXAndMapsProductIdentityDeterministically(t *testing.T) {
	t.Parallel()

	generated, count, err := render(registryDocument{Agents: []registryAgent{
		{
			ID: "codex-acp", Name: "Codex", Version: "1.2.3", Description: "ACP adapter",
			Distribution: registryDistribution{NPX: &registryNPX{
				Package: "@agentclientprotocol/codex-acp@1.2.3",
				Args:    []string{"--stdio"},
				Env:     map[string]string{"Z": "last", "A": "first"},
			}},
		},
		{ID: "binary-only", Distribution: registryDistribution{}},
	}})
	if err != nil {
		t.Fatalf("render() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("render() count = %d, want one npx Agent", count)
	}
	for _, want := range [][]byte{
		[]byte(`ID: "codex"`),
		[]byte(`RegistryID: "codex-acp"`),
		[]byte(`"A": "first"`),
		[]byte(`"Z": "last"`),
	} {
		if !bytes.Contains(generated, want) {
			t.Fatalf("generated output missing %q:\n%s", want, generated)
		}
	}
	if bytes.Index(generated, []byte(`"A": "first"`)) > bytes.Index(generated, []byte(`"Z": "last"`)) {
		t.Fatalf("generated environment is not deterministic:\n%s", generated)
	}
	if bytes.Contains(generated, []byte("binary-only")) {
		t.Fatalf("generated output included unsupported distribution:\n%s", generated)
	}
}
