package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/application"
)

const (
	ActionApplicationCreate Action = "application.session.create"
	ActionApplicationPrompt Action = "application.prompt"
)

// CreateApplicationSessionRequest creates one immutable application execution
// profile. Directory and ownership are allocated by Control, not client metadata.
type CreateApplicationSessionRequest struct {
	WriteBase
	Profile application.Profile `json:"profile"`
}

// ApplicationPromptRequest preserves the origin of admitted input. Summary and
// external material are evidence, never additional user authorization.
type ApplicationPromptRequest struct {
	PromptRequest
	SourceKind string `json:"source_kind"`
}

// ApplicationResourceRequest uploads an immutable byte snapshot, not a path.
// Data uses standard JSON base64 encoding. SHA256 is required and verified.
type ApplicationResourceRequest struct {
	WriteBase
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
	SHA256    string `json:"sha256"`
}

// ApplicationResourceContent is the public byte-transfer response.
type ApplicationResourceContent struct {
	Resource application.Resource `json:"resource"`
	Data     []byte               `json:"data"`
}

// ApplicationOperation is a permanent intent/receipt anchor. An unknown outcome
// is not permission to dispatch again with another ID.
type ApplicationOperation struct {
	OperationID string         `json:"operation_id"`
	Outcome     Outcome        `json:"outcome"`
	Result      *CommandResult `json:"result,omitempty"`
}

// ApplicationServiceConfig binds the generic application authority to the
// existing Session command and observation owners.
type ApplicationServiceConfig struct {
	Store    *application.Store
	Commands *CommandService
	Sessions Service
}

// ApplicationService owns scoped application admission; it does not execute
// models, implement business tools, or maintain a second transcript.
type ApplicationService struct{ config ApplicationServiceConfig }

func NewApplicationService(config ApplicationServiceConfig) (*ApplicationService, error) {
	if config.Store == nil || config.Commands == nil || config.Sessions == nil {
		return nil, errors.New("application: store, commands and sessions are required")
	}
	return &ApplicationService{config: config}, nil
}

// Store exposes the focused application authority to trusted transport adapters.
func (s *ApplicationService) Store() *application.Store { return s.config.Store }

// ApplicationScope derives ownership only from authenticated adapter context.
func ApplicationScope(p Principal) (application.Scope, error) {
	if strings.TrimSpace(p.ID) == "" || p.ApplicationID == "" || p.ConnectionID == "" {
		return application.Scope{}, ErrUnauthorized
	}
	return application.Scope{PrincipalID: p.ID, ApplicationID: p.ApplicationID, ConnectionID: p.ConnectionID}, nil
}

func (s *ApplicationService) Register(ctx context.Context, p Principal, req application.Registration) (application.Connection, error) {
	if strings.TrimSpace(p.ID) == "" || p.ApplicationID != "" || p.ConnectionID != "" {
		return application.Connection{}, ErrUnauthorized
	}
	return s.config.Store.Register(ctx, p.ID, req)
}

func (s *ApplicationService) Create(ctx context.Context, p Principal, req CreateApplicationSessionRequest) (CommandResult, error) {
	if req.SessionID != "" || req.ExpectedRevision != nil || req.ExpectedControllerEpoch != "" {
		return CommandResult{}, errorcode.New(errorcode.InvalidArgument, "application: creation cannot select a Session or revision")
	}
	if err := application.ValidateProfile(req.Profile); err != nil {
		return CommandResult{}, err
	}
	return s.execute(ctx, p, req.OperationID, req, func() (CommandResult, error) { return s.config.Commands.CreateApplicationSession(ctx, p, req) })
}

func (s *ApplicationService) Prompt(ctx context.Context, p Principal, req ApplicationPromptRequest) (CommandResult, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return CommandResult{}, err
	}
	binding, err := s.config.Store.GetBinding(ctx, scope, req.SessionID)
	if err != nil {
		return CommandResult{}, err
	}
	if binding.Archived {
		return CommandResult{}, ErrSessionClosed
	}
	if err := validateApplicationPrompt(req); err != nil {
		return CommandResult{}, err
	}
	return s.execute(ctx, p, req.OperationID, req, func() (CommandResult, error) { return s.config.Commands.PromptApplication(ctx, p, req) })
}

// Archive closes canonical Session admission without deleting history/resources.
// Observation detach and application connection revocation never call Archive.
func (s *ApplicationService) Archive(ctx context.Context, p Principal, req CloseSessionRequest) (CommandResult, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return CommandResult{}, err
	}
	if _, err = s.config.Store.GetBinding(ctx, scope, req.SessionID); err != nil {
		return CommandResult{}, err
	}
	return s.execute(ctx, p, req.OperationID, struct {
		Action  string
		Request CloseSessionRequest
	}{"archive", req}, func() (CommandResult, error) {
		out, err := s.config.Sessions.CloseSession(ctx, p, req)
		if err == nil && out.Outcome == OutcomeCommitted {
			err = s.config.Store.ArchiveBinding(ctx, scope, req.SessionID)
		}
		return out, err
	})
}

func (s *ApplicationService) Operation(ctx context.Context, p Principal, id string) (ApplicationOperation, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return ApplicationOperation{}, err
	}
	op, err := s.config.Store.GetOperation(ctx, scope, id)
	if err != nil {
		return ApplicationOperation{}, err
	}
	return s.observeOperation(ctx, p, scope, op)
}

func (s *ApplicationService) execute(ctx context.Context, p Principal, id string, req any, dispatch func() (CommandResult, error)) (CommandResult, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return CommandResult{}, err
	}
	if err = s.config.Store.CheckActive(ctx, scope); err != nil {
		return CommandResult{}, err
	}
	op, fresh, err := s.config.Store.BeginOperation(ctx, scope, id, req)
	if err != nil {
		return CommandResult{}, err
	}
	if !fresh {
		if len(op.Result) == 0 {
			observed, err := s.observeOperation(ctx, p, scope, op)
			if err != nil {
				return CommandResult{}, err
			}
			if observed.Result != nil {
				return *observed.Result, nil
			}
			return CommandResult{OperationID: id, Outcome: OutcomeUnknown, Detail: "application intent exists without a proven receipt; query native state, do not redispatch"}, nil
		}
		var result CommandResult
		err = json.Unmarshal(op.Result, &result)
		return result, err
	}
	result, dispatchErr := dispatch()
	result.OperationID = id
	if dispatchErr != nil {
		result = resultForBackendError(result, dispatchErr)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	completion, cancel := operationCompletionContext(ctx)
	defer cancel()
	if err = s.config.Store.CompleteOperation(completion, scope, id, data); err != nil {
		result.Outcome = OutcomeUnknown
		return result, errorcode.Wrap(errorcode.UnknownOutcome, "application: receipt persistence failed", err)
	}
	return result, dispatchErr
}

// observeOperation reconciles creation from the immutable binding and canonical
// Session without redispatch. Other effect kinds with missing receipts stay
// unknown; a read never manufactures an execution terminal or a new grant.
func (s *ApplicationService) observeOperation(ctx context.Context, p Principal, scope application.Scope, op application.Operation) (ApplicationOperation, error) {
	out := ApplicationOperation{OperationID: op.ID, Outcome: OutcomeUnknown}
	if len(op.Result) > 0 {
		out.Result = new(CommandResult)
		if err := json.Unmarshal(op.Result, out.Result); err != nil {
			return ApplicationOperation{}, err
		}
		out.Outcome = out.Result.Outcome
		return out, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(op.Request, &fields); err != nil {
		return ApplicationOperation{}, err
	}
	if _, create := fields["profile"]; !create {
		return out, nil
	}
	id := application.SessionID(scope, op.ID)
	out.Result = &CommandResult{OperationID: op.ID, SessionID: id, Outcome: OutcomeUnknown, Detail: "creation intent has no proven receipt"}
	binding, err := s.config.Store.GetBinding(ctx, scope, id)
	if errors.Is(err, application.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return ApplicationOperation{}, err
	}
	if binding.CreationDigest != op.Digest {
		return ApplicationOperation{}, application.ErrConflict
	}
	state, err := s.config.Sessions.InspectSession(ctx, p, StateRequest{SessionID: id})
	if err != nil {
		return ApplicationOperation{}, err
	}
	if state.SessionID != id {
		return ApplicationOperation{}, application.ErrConflict
	}
	out.Outcome = OutcomeCommitted
	out.Result = &CommandResult{OperationID: op.ID, SessionID: id, Outcome: OutcomeCommitted, Revision: state.Revision}
	return out, nil
}

func validateApplicationPrompt(req ApplicationPromptRequest) error {
	switch req.SourceKind {
	case "user", "application_summary", "external_material":
	default:
		return errorcode.New(errorcode.InvalidArgument, "application: unsupported source_kind; background triggers require a grant and are unsupported")
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return errorcode.New(errorcode.InvalidArgument, "application: session_id is required")
	}
	return validateCommandRequest(ActionPrompt, req.PromptRequest)
}

func (s *CommandService) CreateApplicationSession(ctx context.Context, p Principal, req CreateApplicationSessionRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionApplicationCreate, req.WriteBase, "", req)
}
func (s *CommandService) PromptApplication(ctx context.Context, p Principal, req ApplicationPromptRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionApplicationPrompt, req.WriteBase, req.SessionID, req)
}
