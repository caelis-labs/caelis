package session

import "context"

// Service is the durable session store contract. Implementations include
// in-memory (testing) and file-backed (production).
//
// All mutations are atomic at the event level. AppendEvent returns the
// persisted event with server-assigned ID and timestamp.
type Service interface {
	// Create creates a new session.
	Create(context.Context, CreateRequest) (Session, error)

	// Get returns a session by ref.
	Get(context.Context, Ref) (Session, error)

	// List returns sessions matching the request filter.
	List(context.Context, ListRequest) (ListResponse, error)

	// Fork creates a copy of an existing session.
	Fork(context.Context, ForkRequest) (Session, error)

	// Delete removes a session and its events.
	Delete(context.Context, Ref) error

	// AppendEvent appends a durable event to a session. Returns the
	// persisted event with server-assigned fields.
	AppendEvent(context.Context, Ref, Event) (Event, error)

	// Events returns events for a session, optionally filtered.
	Events(context.Context, EventsRequest) ([]Event, error)

	// UpdateState applies a state transformation atomically.
	UpdateState(context.Context, Ref, func(State) (State, error)) error
}

// CreateRequest is the input for Service.Create.
type CreateRequest struct {
	AppName      string
	UserID       string
	WorkspaceKey string
	Title        string
	Workspace    Workspace
	Controller   ControllerBinding
	Participants []ParticipantBinding
	State        State
}

// ListRequest is the input for Service.List.
type ListRequest struct {
	AppName      string
	UserID       string
	WorkspaceKey string
	Cursor       string
	Limit        int
}

// ListResponse is the output of Service.List.
type ListResponse struct {
	Sessions []Session
	Cursor   string
}

// ForkRequest is the input for Service.Fork.
type ForkRequest struct {
	Source Ref
	Title  string
}

// EventsRequest is the input for Service.Events.
type EventsRequest struct {
	SessionRef Ref
	AfterID    string
	Limit      int
	Kinds      []EventKind
}
