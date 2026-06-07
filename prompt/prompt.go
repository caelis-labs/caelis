package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/skill"
)

// Assembler builds one model-visible system prompt from runtime contributions.
type Assembler interface {
	Assemble(context.Context, Request) (string, error)
}

// Request contains prompt contributions collected by runner.
type Request struct {
	Base   string
	Skills []skill.Bundle
}

// DefaultAssembler returns the built-in prompt assembler.
func DefaultAssembler() Assembler {
	return defaultAssembler{}
}

type defaultAssembler struct{}

func (defaultAssembler) Assemble(_ context.Context, req Request) (string, error) {
	return Assemble(req), nil
}

// Assemble renders a prompt using the default Layer 4 format.
func Assemble(req Request) string {
	var parts []string
	if base := strings.TrimSpace(req.Base); base != "" {
		parts = append(parts, base)
	}
	if len(req.Skills) > 0 {
		parts = append(parts, buildSkillsSection(req.Skills))
	}
	return strings.Join(parts, "\n\n")
}

func buildSkillsSection(skills []skill.Bundle) string {
	var b strings.Builder
	b.WriteString("## Skills\n\n")
	b.WriteString("Use a skill only when its description clearly matches the task.\n\n")
	for _, one := range skills {
		name := strings.TrimSpace(one.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(one.Description)
		path := strings.TrimSpace(one.Path)
		if path != "" {
			fmt.Fprintf(&b, "- %s: %s (file: %s)\n", name, desc, path)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", name, desc)
		}
	}
	return strings.TrimSpace(b.String())
}
