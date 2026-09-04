package controladapter

import (
	"context"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestDisconnectProviderCatalogGroupsProvidersAndExcludesACP(t *testing.T) {
	t.Parallel()

	choices := []ModelChoice{
		{ID: "codex-luna", Provider: "openai-codex", Alias: "openai-codex/luna", Backend: "provider"},
		{ID: "acp:codex:sol", Provider: "codex", Alias: "Codex - Sol", Backend: "acp"},
		{ID: "deepseek-pro", Provider: "deepseek", Alias: "deepseek/pro", Backend: "provider"},
		{ID: "codex-astra", Provider: "openai-codex", Alias: "openai-codex/astra", Backend: "provider"},
		{ID: "deepseek-flash", Provider: "deepseek", Alias: "deepseek/flash", Backend: "provider"},
		{ID: "acp:grok:4.6", Provider: "grok", Alias: "grok - Grok 4.6", Backend: "acp"},
	}
	original := slices.Clone(choices)
	driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{
		ListChoicesFn: func(_ context.Context, ref session.SessionRef) ([]ModelChoice, error) {
			if ref.SessionID != "" {
				t.Fatalf("disconnect catalog requested Session override %q", ref.SessionID)
			}
			return choices, nil
		},
	}}, "", "")
	for _, tt := range []struct {
		query string
		limit int
		want  []string
	}{
		{limit: 20, want: []string{"deepseek-flash", "deepseek-pro", "codex-astra", "codex-luna"}},
		{limit: 2, want: []string{"deepseek-flash", "deepseek-pro"}},
		{query: "openai-codex", limit: 20, want: []string{"codex-astra", "codex-luna"}},
		{query: "Codex -", limit: 20, want: []string{}},
	} {
		candidates, err := driver.CompleteSlashArg(context.Background(), "disconnect-provider", tt.query, tt.limit)
		if err != nil {
			t.Fatal(err)
		}
		if got := slashCandidateValues(candidates); !slices.Equal(got, tt.want) {
			t.Fatalf("disconnect(%q, %d) = %#v, want %#v", tt.query, tt.limit, got, tt.want)
		}
	}
	models, err := driver.CompleteSlashArg(context.Background(), "model", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := slashCandidateValues(models); !slices.Equal(got, []string{"codex-luna", "acp:codex:sol", "deepseek-pro", "codex-astra", "deepseek-flash", "acp:grok:4.6"}) {
		t.Fatalf("model switch order changed: %#v", got)
	}
	for i, choice := range choices {
		if choice.ID != original[i].ID {
			t.Fatalf("shared catalog order mutated: %#v", choices)
		}
	}
}
