package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/memorytool"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

func (s *runtimeComposition) admitCreatedMemorySession(ctx context.Context, created session.Session) (session.Session, error) {
	if s == nil || s.process == nil || s.authorities.store == nil || s.sessions == nil {
		return created, nil
	}
	doc, err := s.authorities.store.LoadContext(ctx)
	if err != nil {
		return created, &session.CommittedError{Err: fmt.Errorf("load Memory binding after Session creation: %w", err)}
	}
	binding, err := selectRuntimeMemoryBinding(
		ctx,
		doc.Memory,
		s.runtimeProcessSnapshot(),
		created.SessionRef,
		session.WorkspaceRef{Key: created.WorkspaceKey, CWD: created.CWD},
	)
	if err != nil {
		return created, &session.CommittedError{Err: err}
	}
	if binding == nil {
		return created, nil
	}
	if s.authorities.memoryHost == nil {
		return created, &session.CommittedError{Err: fmt.Errorf("gatewayapp: resolved Memory binding has no embedded runtime")}
	}
	if err := s.authorities.memoryHost.ValidateBinding(*binding); err != nil {
		return created, &session.CommittedError{Err: fmt.Errorf("validate created Session Memory host binding: %w", err)}
	}
	if err := memorybinding.AdmitSession(ctx, s.sessions, created.SessionRef, *binding); err != nil {
		if errors.Is(err, memorybinding.ErrSessionAdmissionConflict) {
			coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: Session Memory binding conflict", err)
			return created, appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
		}
		return created, &session.CommittedError{Err: fmt.Errorf("admit created Session Memory binding: %w", err)}
	}
	admitted, err := s.sessions.Session(ctx, created.SessionRef)
	if err != nil {
		return created, &session.CommittedError{Err: fmt.Errorf("reload admitted Session: %w", err)}
	}
	return admitted, nil
}

func (s *runtimeComposition) buildMemoryTools() ([]tool.Tool, error) {
	if s == nil || s.activation == nil || s.activation.memoryBinding == nil {
		return nil, nil
	}
	if s.authorities.memoryHost == nil {
		return nil, fmt.Errorf("gatewayapp: resolved Memory binding has no embedded runtime")
	}
	binding := *s.activation.memoryBinding
	ref := s.activation.sessionRef
	client, err := s.authorities.memoryHost.Bind(binding, v1alpha1.SourceContext{
		ActorRef:     string(binding.RuntimeActorRef),
		SessionRef:   strings.TrimSpace(ref.SessionID),
		WorkspaceRef: strings.TrimSpace(s.workspace.Key),
		SourceType:   "caelis-runtime-tool",
	}, memorytool.DefaultRecallBudget())
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: bind Memory SDK client: %w", err)
	}
	return memorytool.New(memorytool.Config{
		Client:             client,
		Sessions:           s.sessions,
		SessionRef:         ref,
		Binding:            binding,
		MaxProjectionBytes: memorytool.DefaultRecallProjectionBytes,
	})
}
