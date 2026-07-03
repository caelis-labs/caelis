package kernel

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/ports/userdisplay"
)

type SubmissionReferenceProjectionRequest struct {
	Session session.Session
	Input   string
}

// SubmissionReferenceProjection is the model-visible text after resolving
// surface shorthand references for one user submission.
type SubmissionReferenceProjection struct {
	Input   string
	Changed bool
}

// SubmissionReferenceProjector resolves user shorthand references at the
// canonical gateway turn boundary.
type SubmissionReferenceProjector interface {
	ProjectSubmissionReferences(context.Context, SubmissionReferenceProjectionRequest) (SubmissionReferenceProjection, error)
}

// SubmissionReferenceProjectorFunc adapts a function to
// SubmissionReferenceProjector.
type SubmissionReferenceProjectorFunc func(context.Context, SubmissionReferenceProjectionRequest) (SubmissionReferenceProjection, error)

func (f SubmissionReferenceProjectorFunc) ProjectSubmissionReferences(ctx context.Context, req SubmissionReferenceProjectionRequest) (SubmissionReferenceProjection, error) {
	return f(ctx, req)
}

func (g *Gateway) prepareBeginTurnRequest(ctx context.Context, activeSession session.Session, req BeginTurnRequest) (BeginTurnRequest, error) {
	input, displayInput, parts, meta, err := g.prepareUserInput(ctx, activeSession, req.Input, req.DisplayInput, req.ContentParts, req.Metadata)
	if err != nil {
		return BeginTurnRequest{}, err
	}
	req.Input = input
	req.DisplayInput = displayInput
	req.ContentParts = parts
	req.Metadata = meta
	return req, nil
}

func (g *Gateway) preparePromptParticipantRequest(ctx context.Context, activeSession session.Session, req PromptParticipantRequest) (PromptParticipantRequest, error) {
	input, displayInput, parts, _, err := g.prepareUserInput(ctx, activeSession, req.Input, req.DisplayInput, req.ContentParts, nil)
	if err != nil {
		return PromptParticipantRequest{}, err
	}
	req.Input = input
	req.DisplayInput = displayInput
	req.ContentParts = parts
	return req, nil
}

func (g *Gateway) prepareSubmitRequest(ctx context.Context, activeSession session.Session, req SubmitRequest) (SubmitRequest, error) {
	if req.Kind != SubmissionKindConversation {
		return req, nil
	}
	input, displayInput, parts, meta, err := g.prepareUserInput(ctx, activeSession, req.Text, req.DisplayText, req.ContentParts, req.Metadata)
	if err != nil {
		return SubmitRequest{}, err
	}
	req.Text = input
	req.DisplayText = displayInput
	req.ContentParts = parts
	req.Metadata = meta
	return req, nil
}

func (g *Gateway) prepareUserInput(
	ctx context.Context,
	activeSession session.Session,
	input string,
	displayInput string,
	parts []model.ContentPart,
	meta map[string]any,
) (string, string, []model.ContentPart, map[string]any, error) {
	rawInput := strings.TrimSpace(input)
	displayInput = userdisplay.ResolveDisplayInput(displayInput, meta)
	modelInput := rawInput
	preparedParts := append([]model.ContentPart(nil), parts...)
	if g != nil && g.submissionReferences != nil && rawInput != "" {
		projection, err := g.submissionReferences.ProjectSubmissionReferences(ctx, SubmissionReferenceProjectionRequest{
			Session: activeSession,
			Input:   rawInput,
		})
		if err != nil {
			return "", "", nil, nil, err
		}
		projected := strings.TrimSpace(projection.Input)
		if projected != "" {
			modelInput = projected
		}
		if projection.Changed {
			if displayInput == "" {
				displayInput = rawInput
			}
			preparedParts = projectedContentParts(preparedParts, modelInput)
		}
	}
	displayInput, meta = canonicalDisplayInput(modelInput, displayInput, preparedParts, meta)
	return modelInput, displayInput, preparedParts, meta, nil
}

func canonicalDisplayInput(input string, displayInput string, parts []model.ContentPart, meta map[string]any) (string, map[string]any) {
	message, displayText, outMeta := userdisplay.Resolve(input, displayInput, parts, meta)
	if strings.TrimSpace(displayText) == "" || strings.TrimSpace(displayText) == strings.TrimSpace(message.TextContent()) {
		displayText = ""
	}
	return displayText, outMeta
}

func projectedContentParts(parts []model.ContentPart, projected string) []model.ContentPart {
	projected = strings.TrimSpace(projected)
	if len(parts) == 0 || projected == "" {
		return parts
	}
	out := make([]model.ContentPart, 0, len(parts)+1)
	out = append(out, model.ContentPart{Type: model.ContentPartText, Text: projected})
	for _, part := range parts {
		if part.Type == model.ContentPartText {
			continue
		}
		out = append(out, part)
	}
	return out
}
