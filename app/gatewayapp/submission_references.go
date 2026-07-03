package gatewayapp

import (
	"context"
	"strings"

	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp/control/promptrefs"
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
		// Skill shorthand uses the runtime prompt snapshot. Skills added after
		// runtime assembly become available after a runtime rebuild, not midway
		// through the current runtime context.
		skillNames = s.skillCatalogSnapshot().NameLookup()
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
