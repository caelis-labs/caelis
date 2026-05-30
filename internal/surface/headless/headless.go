// Package headless runs a one-shot prompt over the shared app services.
package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
)

type ApprovalPolicy string

const (
	ApprovalPolicyAutoDeny   ApprovalPolicy = "auto_deny"
	ApprovalPolicyApproveAll ApprovalPolicy = "approve_all"
)

type OutputFormat string

const (
	OutputText OutputFormat = "text"
	OutputJSON OutputFormat = "json"
)

type Request struct {
	Services           services.Services
	SessionRef         session.Ref
	Workspace          session.Workspace
	PreferredSessionID string
	Title              string
	Input              string
	ContentParts       []model.ContentPart
	Model              string
	SessionMode        string
	Surface            string
	ApprovalPolicy     ApprovalPolicy
	ResolveApproval    func(context.Context, *session.ApprovalEvent) (coreruntime.ApprovalDecision, error)
}

type Result struct {
	Session    session.Session `json:"session"`
	Output     string          `json:"output,omitempty"`
	LastCursor session.Cursor  `json:"last_cursor,omitempty"`
	Usage      model.Usage     `json:"usage,omitempty"`
	EventCount int             `json:"event_count,omitempty"`
}

func RunOnce(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Services.Engine() == nil {
		return Result{}, errors.New("surface/headless: services engine is required")
	}
	preferredSessionID := strings.TrimSpace(firstNonEmpty(req.PreferredSessionID, req.SessionRef.SessionID))
	active, err := req.Services.Sessions().Start(ctx, services.StartSessionRequest{
		Workspace:          req.Workspace,
		PreferredSessionID: preferredSessionID,
		Title:              strings.TrimSpace(req.Title),
	})
	if err != nil {
		return Result{}, err
	}
	if mode := coreruntime.NormalizeSessionMode(req.SessionMode); mode != "" {
		if _, err := req.Services.Modes().Set(ctx, active.Ref, mode); err != nil {
			return Result{}, err
		}
	}
	turn, err := req.Services.Turns().Begin(ctx, services.BeginTurnRequest{
		SessionRef:   active.Ref,
		Input:        req.Input,
		ContentParts: model.CloneContentParts(req.ContentParts),
		Model:        strings.TrimSpace(req.Model),
		Surface:      firstNonEmpty(req.Surface, "headless"),
	})
	if err != nil {
		return Result{}, err
	}
	defer turn.Close()

	out := Result{Session: active}
	for env := range turn.Events() {
		out.LastCursor = env.Cursor
		if env.Err != "" {
			return out, errors.New(env.Err)
		}
		event := session.CloneEvent(env.Event)
		out.EventCount++
		if event.Approval != nil && event.Approval.Status == session.ApprovalPending {
			decision, err := resolveApproval(ctx, req, event.Approval)
			if err != nil {
				return out, err
			}
			if err := turn.Submit(ctx, coreruntime.Submission{
				Kind:     coreruntime.SubmissionApproval,
				Approval: &decision,
			}); err != nil {
				return out, err
			}
			continue
		}
		if event.Type == session.EventAssistant {
			if text := session.EventText(event); text != "" {
				out.Output = text
			}
		}
		if event.Message != nil && event.Message.Usage != nil {
			out.Usage = *event.Message.Usage
		}
	}
	return out, nil
}

func WriteResult(w io.Writer, result Result, format OutputFormat) error {
	if w == nil {
		return errors.New("surface/headless: writer is required")
	}
	switch format {
	case "", OutputText:
		if result.Output == "" {
			return nil
		}
		_, err := fmt.Fprintln(w, result.Output)
		return err
	case OutputJSON:
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	default:
		return fmt.Errorf("surface/headless: unsupported output format %q", format)
	}
}

func resolveApproval(ctx context.Context, req Request, approval *session.ApprovalEvent) (coreruntime.ApprovalDecision, error) {
	if req.ResolveApproval != nil {
		return req.ResolveApproval(ctx, approval)
	}
	approved := req.ApprovalPolicy == ApprovalPolicyApproveAll
	optionID := approvalOptionID(approval, approved)
	outcome := "reject"
	if approved {
		outcome = "allow"
	}
	return coreruntime.ApprovalDecision{
		Outcome:  outcome,
		OptionID: optionID,
		Approved: approved,
		Reason:   outcome,
	}, nil
}

func approvalOptionID(approval *session.ApprovalEvent, approved bool) string {
	if approval == nil {
		return ""
	}
	want := "reject"
	if approved {
		want = "allow"
	}
	for _, option := range approval.Options {
		if strings.Contains(strings.ToLower(strings.TrimSpace(option.Kind+" "+option.ID+" "+option.Name)), want) {
			return strings.TrimSpace(option.ID)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
