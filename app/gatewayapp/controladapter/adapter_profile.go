package controladapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
)

func (d *Adapter) AgentProfileStatus(ctx context.Context) (AgentProfileStatusSnapshot, error) {
	if d.stack.AgentProfile.StatusFn == nil {
		return AgentProfileStatusSnapshot{}, missingRuntimeDependency("agent profile")
	}
	return d.stack.AgentProfile.StatusFn(ctx)
}

func (d *Adapter) BindAgentProfile(ctx context.Context, cfg AgentProfileBindingConfig) (AgentProfileStatusSnapshot, error) {
	if d.stack.AgentProfile.BindFn == nil {
		return AgentProfileStatusSnapshot{}, missingRuntimeDependency("agent profile binding")
	}
	return d.stack.AgentProfile.BindFn(ctx, cfg)
}

func (d *Adapter) StartReviewSubagent(ctx context.Context, instructions string, attachments []Attachment) (Turn, error) {
	profile, err := d.runnableReviewerProfile(ctx)
	if err != nil {
		return nil, err
	}
	prompt, attachmentOffset := gatewayapp.ReviewSubagentPromptForProfileTarget(instructions, agentprofile.BindingTargetKind(profile.Target))
	return d.startSidecarTurn(ctx, startSidecarTurnRequest{
		Agent:            profile.ID,
		LabelBase:        reviewSidecarLabelBase(profile),
		Prompt:           prompt,
		DisplayInput:     displayInputWithAttachments(instructions, attachments),
		DisplayTitle:     reviewDisplayTitle(instructions),
		Attachments:      shiftControlAttachments(attachments, attachmentOffset),
		Source:           "slash_review",
		DetachOnComplete: true,
	})
}

func reviewSidecarLabelBase(profile AgentProfileSnapshot) string {
	if agentprofile.NormalizeBindingTarget(agentprofile.BindingTargetKind(profile.Target)) == agentprofile.BindingTargetACP {
		if agent := strings.TrimSpace(profile.ACPAgent); agent != "" {
			return agent
		}
	}
	return strings.TrimSpace(profile.ID)
}

func reviewDisplayTitle(instructions string) string {
	if strings.TrimSpace(instructions) != "" {
		return ""
	}
	return "Code review requested"
}

func (d *Adapter) runnableReviewerProfile(ctx context.Context) (AgentProfileSnapshot, error) {
	if d.stack.AgentProfile.StatusFn == nil {
		return AgentProfileSnapshot{}, missingRuntimeDependency("agent profile")
	}
	status, err := d.stack.AgentProfile.StatusFn(ctx)
	if err != nil {
		return AgentProfileSnapshot{}, err
	}
	for _, profile := range status.Profiles {
		if !strings.EqualFold(strings.TrimSpace(profile.ID), gatewayapp.ReviewerAgentProfileID) {
			continue
		}
		if profile.SystemManaged {
			return AgentProfileSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: reviewer profile is system-managed and cannot be started as a review sidecar")
		}
		if !profile.Enabled {
			return AgentProfileSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: reviewer profile is disabled; run /subagent bind reviewer default to enable it")
		}
		if bindingStatus := strings.ToLower(strings.TrimSpace(profile.Status)); bindingStatus == "stale" {
			detail := strings.TrimSpace(profile.Warning)
			if detail == "" {
				detail = strings.TrimSpace(profile.Status)
			}
			return AgentProfileSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: reviewer profile is not ready: %s", detail)
		}
		return profile, nil
	}
	return AgentProfileSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: reviewer profile is unavailable; run /subagent list to inspect configured profiles")
}

func shiftControlAttachments(items []Attachment, offset int) []Attachment {
	if len(items) == 0 || offset == 0 {
		return append([]Attachment(nil), items...)
	}
	out := make([]Attachment, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		data := strings.TrimSpace(item.Data)
		if name == "" && data == "" {
			continue
		}
		out = append(out, Attachment{
			Name:     name,
			Offset:   max(item.Offset, 0) + offset,
			MimeType: strings.TrimSpace(item.MimeType),
			Data:     data,
		})
	}
	return out
}
