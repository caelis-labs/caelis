package prompt

import (
	"context"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/skill"
)

func TestDefaultAssemblerCombinesBaseAndSkills(t *testing.T) {
	text, err := DefaultAssembler().Assemble(context.Background(), Request{
		Base: "base instructions",
		Skills: []skill.Bundle{{
			Name:        "lint",
			Description: "Run lint checks.",
			Path:        "/skills/lint/SKILL.md",
		}},
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	for _, want := range []string{"base instructions", "## Skills", "lint", "Run lint checks.", "/skills/lint/SKILL.md"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt = %q, want %q", text, want)
		}
	}
}
