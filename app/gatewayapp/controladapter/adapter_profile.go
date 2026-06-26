package controladapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
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
	prompt, attachmentOffset := gatewayapp.ReviewSubagentPrompt(instructions)
	return d.startSidecarTurn(ctx, startSidecarTurnRequest{
		Agent:       profile.ID,
		Prompt:      prompt,
		Attachments: shiftControlAttachments(attachments, attachmentOffset),
		Source:      "slash_review",
	})
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
		if name == "" {
			continue
		}
		out = append(out, Attachment{Name: name, Offset: max(item.Offset, 0) + offset})
	}
	return out
}
