// Package static contains simple prompt port implementations for fixed fragments.
package static

import (
	"context"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/prompt"
)

type Assembler struct {
	Fragments []prompt.Fragment
}

func (a Assembler) Assemble(ctx context.Context, req prompt.Request) (prompt.Result, error) {
	if err := ctx.Err(); err != nil {
		return prompt.Result{}, err
	}
	fragments := cloneFragments(a.Fragments)
	sort.SliceStable(fragments, func(i, j int) bool {
		return fragments[i].Priority < fragments[j].Priority
	})
	parts := make([]string, 0, len(fragments)+1)
	for _, fragment := range fragments {
		if text := strings.TrimSpace(fragment.Text); text != "" {
			parts = append(parts, text)
		}
	}
	if text := strings.TrimSpace(req.UserOverride); text != "" {
		parts = append(parts, text)
	}
	return prompt.Result{
		SystemText: strings.Join(parts, "\n\n"),
		Fragments:  fragments,
	}, nil
}

type FragmentProvider struct {
	Items []prompt.Fragment
}

func (p FragmentProvider) Fragments(ctx context.Context, _ prompt.Request) ([]prompt.Fragment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cloneFragments(p.Items), nil
}

func cloneFragments(in []prompt.Fragment) []prompt.Fragment {
	if len(in) == 0 {
		return nil
	}
	out := make([]prompt.Fragment, 0, len(in))
	for _, fragment := range in {
		copied := fragment
		if fragment.Metadata != nil {
			copied.Metadata = make(map[string]any, len(fragment.Metadata))
			for key, value := range fragment.Metadata {
				copied.Metadata[key] = value
			}
		}
		out = append(out, copied)
	}
	return out
}

var _ prompt.Assembler = Assembler{}
var _ prompt.FragmentProvider = FragmentProvider{}
