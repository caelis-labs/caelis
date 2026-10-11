package controlprompt

import (
	"context"
	"errors"
	"testing"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

type receiptRouterService struct {
	*fakeService
	id                     string
	recordErr, dispatchErr error
	order                  []string
}

func (s *receiptRouterService) RecordInteraction(context.Context, string) (string, error) {
	s.order = append(s.order, "record")
	return s.id, s.recordErr
}

func (s *receiptRouterService) UseModel(ctx context.Context, model, reasoning string, fast bool) (controlstatus.StatusSnapshot, error) {
	s.order = append(s.order, "dispatch")
	if s.dispatchErr != nil {
		return controlstatus.StatusSnapshot{}, s.dispatchErr
	}
	return s.fakeService.UseModel(ctx, model, reasoning, fast)
}

func TestRouterDurableCommandReceiptOrdersAdmissionBeforeEffects(t *testing.T) {
	svc := &receiptRouterService{fakeService: &fakeService{}, id: "real-event-id"}
	r := New(RouterConfig{Service: svc})
	result, err := r.Route(t.Context(), Request{Submission: Submission{Text: "/model ollama/llama3 high"}, RequireInteractionReceipt: true, InteractionObserver: func(_ context.Context, id string) error {
		if id != svc.id {
			t.Errorf("receipt id = %q", id)
		}
		svc.order = append(svc.order, "observe")
		return nil
	}})
	if err != nil || result.InteractionMessageID != svc.id {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(svc.order) != 3 || svc.order[0] != "record" || svc.order[1] != "observe" || svc.order[2] != "dispatch" {
		t.Fatalf("ordering=%v", svc.order)
	}
	if svc.usedModel != "ollama/llama3" {
		t.Fatal("original command route did not run")
	}
}

func TestRouterReceiptSurvivesFailuresWithoutUnadmittedEffects(t *testing.T) {
	for _, stage := range []string{"before_insert", "after_insert", "observer", "dispatch"} {
		t.Run(stage, func(t *testing.T) {
			sentinel := errors.New("fixture failure")
			svc := &receiptRouterService{fakeService: &fakeService{}, id: "real-event-id"}
			req := Request{Submission: Submission{Text: "/model ollama/llama3 high"}, RequireInteractionReceipt: true}
			switch stage {
			case "before_insert":
				svc.id, svc.recordErr = "", sentinel
			case "after_insert":
				svc.recordErr = sentinel
			case "observer":
				req.InteractionObserver = func(context.Context, string) error { return sentinel }
			case "dispatch":
				svc.dispatchErr = sentinel
			}
			result, err := New(RouterConfig{Service: svc}).Route(t.Context(), req)
			if err == nil || result.InteractionMessageID != svc.id {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			wantCalls := 1
			if stage == "dispatch" {
				wantCalls = 2
			}
			if len(svc.order) != wantCalls || svc.usedModel != "" {
				t.Fatalf("effects=%v usedModel=%q", svc.order, svc.usedModel)
			}
		})
	}
}

func TestRouterStableCommandsDoNotRequireOrAddConversationReceipts(t *testing.T) {
	svc := &receiptRouterService{fakeService: &fakeService{}, recordErr: errors.New("unavailable")}
	result, err := New(RouterConfig{Service: svc}).Route(t.Context(), Request{Submission: Submission{Text: "/help"}})
	if err != nil || !result.Handled || result.InteractionMessageID != "" || len(svc.order) != 0 {
		t.Fatalf("result=%#v err=%v order=%v", result, err, svc.order)
	}
}
