package gatewayapp

import (
	"context"
	"strings"

	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/skill"
	"github.com/OnslaughtSnail/caelis/protocol/acp/control/promptrefs"
)

func (s *Stack) submissionReferenceProjector() kernelimpl.SubmissionReferenceProjector {
	if s == nil {
		return nil
	}
	return kernelimpl.SubmissionReferenceProjectorFunc(s.projectSubmissionReferences)
}

func (s *Stack) projectSubmissionReferences(ctx context.Context, req kernelimpl.SubmissionReferenceProjectionRequest) (kernelimpl.SubmissionReferenceProjection, error) {
	text := strings.TrimSpace(req.Input)
	if text == "" {
		return kernelimpl.SubmissionReferenceProjection{}, nil
	}
	tokens := promptrefs.ScanSubmissionReferences(text)
	if len(tokens) == 0 {
		return kernelimpl.SubmissionReferenceProjection{Input: text}, nil
	}
	var skillNames map[string]string
	if submissionReferenceTokensContain(tokens, promptrefs.KindSkill) {
		// Skill shorthand is opportunistic: an unavailable catalog must not
		// turn ordinary prompts containing $NAME into submission failures.
		skills, err := s.Skills().Discover(ctx, req.Session.CWD)
		if err == nil {
			skillNames = canonicalSkillNameMap(skills)
		}
	}
	projected := promptrefs.ProjectSubmissionReferences(text, promptrefs.ProjectionOptions{
		WorkspaceDir: req.Session.CWD,
		SkillNames:   skillNames,
	})
	return kernelimpl.SubmissionReferenceProjection{
		Input:   projected.Text,
		Changed: projected.Changed,
	}, nil
}

func submissionReferenceTokensContain(tokens []promptrefs.Token, kind promptrefs.Kind) bool {
	for _, token := range tokens {
		if token.Kind == kind {
			return true
		}
	}
	return false
}

func canonicalSkillNameMap(skills []skill.Meta) map[string]string {
	if len(skills) == 0 {
		return nil
	}
	out := make(map[string]string, len(skills))
	for _, one := range skills {
		name := strings.TrimSpace(one.Name)
		if name == "" {
			continue
		}
		out[strings.ToLower(name)] = name
	}
	return out
}
