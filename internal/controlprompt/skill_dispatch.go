package controlprompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

// SkillResolver resolves slash-entered skill identities for prompt routing.
// Implementations must preserve agent-sdk/skill.Catalog alias semantics.
type SkillResolver interface {
	ResolveSkill(context.Context, string) (SkillResolveResult, error)
}

// SkillResolveResult distinguishes one canonical skill from an ambiguous set.
// An empty result means the reference was not found.
type SkillResolveResult struct {
	Canonical string
	Matches   []string
}

func (r router) dispatchDirectSkill(ctx context.Context, command string, args string, argsStart int, submission control.Submission) (Result, bool, error) {
	if r.skillResolver == nil {
		return Result{}, false, nil
	}
	resolution, err := r.resolveSkill(ctx, command)
	if err != nil {
		return Result{}, true, FriendlyCommandError("skill", err)
	}
	if resolution.Canonical == "" && len(resolution.Matches) > 0 {
		return r.skillResolutionNotice(command, resolution.Matches), true, nil
	}
	if resolution.Canonical == "" {
		return Result{}, false, nil
	}
	result, err := r.submitResolvedSkill(ctx, submission, resolution.Canonical, args, argsStart)
	return result, true, err
}

func (r router) resolveSkill(ctx context.Context, name string) (SkillResolveResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SkillResolveResult{}, nil
	}
	return r.skillResolver.ResolveSkill(contextOrBackground(ctx), name)
}

func (r router) skillResolutionNotice(name string, matches []string) Result {
	if len(matches) == 0 {
		return r.noticeResult(fmt.Sprintf("unknown skill: %s\nselect an available skill from slash completion", name))
	}
	lines := []string{fmt.Sprintf("ambiguous skill: %s", name), "use one of:"}
	for _, match := range matches {
		lines = append(lines, "  /"+match)
	}
	return r.noticeResult(strings.Join(lines, "\n"))
}

func (r router) submitResolvedSkill(ctx context.Context, submission control.Submission, canonical string, prompt string, promptStart int) (Result, error) {
	canonical = strings.TrimSpace(canonical)
	prompt = strings.TrimSpace(prompt)
	if strings.TrimSpace(submission.DisplayText) == "" {
		submission.DisplayText = strings.TrimSpace(submission.Text)
	}
	internalText := "$" + canonical
	internalPromptStart := len([]rune(internalText))
	if prompt != "" {
		internalText += " " + prompt
		internalPromptStart++
	}

	submission.Text = internalText
	submission.Attachments = shiftSkillAttachments(submission.Attachments, promptStart, internalPromptStart)
	turn, err := r.service.Submit(contextOrBackground(ctx), submission)
	if err != nil {
		return Result{}, FriendlyCommandError("skill", err)
	}
	if turn == nil {
		return Result{Handled: true, ContinueRunning: true, SuppressTurnDivider: true}, nil
	}
	return Result{Handled: true, Turn: turn}, nil
}

func shiftSkillAttachments(attachments []control.Attachment, originalPromptStart int, internalPromptStart int) []control.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	if originalPromptStart < 0 {
		originalPromptStart = 0
	}
	out := make([]control.Attachment, len(attachments))
	for i, attachment := range attachments {
		out[i] = attachment
		offset := attachment.Offset
		if offset < originalPromptStart {
			offset = originalPromptStart
		}
		out[i].Offset = internalPromptStart + offset - originalPromptStart
	}
	return out
}
