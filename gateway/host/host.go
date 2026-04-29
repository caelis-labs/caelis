package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	gatewaycore "github.com/OnslaughtSnail/caelis/gateway/core"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type HostMode string

const (
	HostModeForeground HostMode = "foreground"
	HostModeDaemon     HostMode = "daemon"
)

type HostConfig struct {
	Gateway *gatewaycore.Gateway
	ID      string
	Mode    HostMode
	Clock   func() time.Time
}

type Host struct {
	gateway   *gatewaycore.Gateway
	id        string
	mode      HostMode
	clock     func() time.Time
	startedAt time.Time

	mu           sync.RWMutex
	shuttingDown bool
}

type HostStatus struct {
	ID           string    `json:"id,omitempty"`
	Mode         HostMode  `json:"mode,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	ShuttingDown bool      `json:"shutting_down,omitempty"`
	ActiveTurns  int       `json:"active_turns,omitempty"`
	Bindings     int       `json:"bindings,omitempty"`
}

type RemoteAddress struct {
	Surface   string `json:"surface,omitempty"`
	Channel   string `json:"channel,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

type RemoteActor struct {
	Kind        string `json:"kind,omitempty"`
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type RemoteSessionRequest struct {
	AppName            string
	UserID             string
	Workspace          sdksession.WorkspaceRef
	SessionRef         sdksession.SessionRef
	PreferredSessionID string
	Title              string
	Metadata           map[string]any
	Address            RemoteAddress
	Actor              RemoteActor
	Owner              string
	BindingKey         string
	ExpiresAt          time.Time
}

type RemoteTurnRequest struct {
	Session   RemoteSessionRequest
	Input     string
	ModeName  string
	ModelHint string
	Metadata  map[string]any
	Request   sdkruntime.ModelRequestOptions
}

func NewHost(cfg HostConfig) (*Host, error) {
	if cfg.Gateway == nil {
		return nil, fmt.Errorf("gateway: host gateway is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	mode := cfg.Mode
	if mode == "" {
		mode = HostModeForeground
	}
	return &Host{
		gateway:   cfg.Gateway,
		id:        firstNonEmptyHost(strings.TrimSpace(cfg.ID), string(mode)+"-host"),
		mode:      mode,
		clock:     clock,
		startedAt: clock(),
	}, nil
}

func (h *Host) Status() HostStatus {
	if h == nil {
		return HostStatus{}
	}
	h.mu.RLock()
	shuttingDown := h.shuttingDown
	h.mu.RUnlock()
	active, bindings := h.gateway.ActiveCounts()
	return HostStatus{
		ID:           h.id,
		Mode:         h.mode,
		StartedAt:    h.startedAt,
		ShuttingDown: shuttingDown,
		ActiveTurns:  active,
		Bindings:     bindings,
	}
}

func (h *Host) Shutdown(_ context.Context) error {
	if h == nil || h.gateway == nil {
		return nil
	}
	h.mu.Lock()
	h.shuttingDown = true
	h.mu.Unlock()
	h.gateway.CancelActiveTurns()
	return nil
}

func (h *Host) EnsureRemoteSession(ctx context.Context, req RemoteSessionRequest) (sdksession.Session, error) {
	if h == nil || h.gateway == nil {
		return sdksession.Session{}, fmt.Errorf("gateway: host is unavailable")
	}
	bindingKey := remoteBindingKey(req.BindingKey, req.Address)
	binding := remoteBindingDescriptor(req.Address, req.Actor, req.Owner, req.ExpiresAt)

	if ref := sdksession.NormalizeSessionRef(req.SessionRef); strings.TrimSpace(ref.SessionID) != "" {
		loaded, err := h.gateway.LoadSession(ctx, gatewaycore.LoadSessionRequest{
			SessionRef: ref,
			BindingKey: bindingKey,
			Binding:    binding,
		})
		if err != nil {
			return sdksession.Session{}, err
		}
		return loaded.Session, nil
	}
	if ref, ok := h.gateway.CurrentSession(bindingKey); ok && strings.TrimSpace(ref.SessionID) != "" {
		loaded, err := h.gateway.LoadSession(ctx, gatewaycore.LoadSessionRequest{
			SessionRef: ref,
			BindingKey: bindingKey,
			Binding:    binding,
		})
		if err != nil {
			return sdksession.Session{}, err
		}
		return loaded.Session, nil
	}

	loaded, err := h.gateway.ResumeSession(ctx, gatewaycore.ResumeSessionRequest{
		AppName:    req.AppName,
		UserID:     req.UserID,
		Workspace:  req.Workspace,
		BindingKey: bindingKey,
		Binding:    binding,
	})
	if err == nil {
		return loaded.Session, nil
	}
	var gatewayErr *gatewaycore.Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != gatewaycore.CodeNoResumableSession {
		return sdksession.Session{}, err
	}
	return h.gateway.StartSession(ctx, gatewaycore.StartSessionRequest{
		AppName:            req.AppName,
		UserID:             req.UserID,
		Workspace:          req.Workspace,
		PreferredSessionID: req.PreferredSessionID,
		Title:              req.Title,
		Metadata:           cloneMap(req.Metadata),
		BindingKey:         bindingKey,
		Binding:            binding,
	})
}

func (h *Host) BeginRemoteTurn(ctx context.Context, req RemoteTurnRequest) (gatewaycore.BeginTurnResult, error) {
	if h == nil || h.gateway == nil {
		return gatewaycore.BeginTurnResult{}, fmt.Errorf("gateway: host is unavailable")
	}
	session, err := h.EnsureRemoteSession(ctx, req.Session)
	if err != nil {
		return gatewaycore.BeginTurnResult{}, err
	}
	beginReq := gatewaycore.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      strings.TrimSpace(req.Input),
		ModeName:   strings.TrimSpace(req.ModeName),
		ModelHint:  strings.TrimSpace(req.ModelHint),
		Surface:    strings.TrimSpace(req.Session.Address.Surface),
		Metadata:   cloneMap(req.Metadata),
		Request:    req.Request,
	}
	return h.gateway.BeginTurn(ctx, beginReq)
}

func remoteBindingKey(override string, address RemoteAddress) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	parts := []string{
		strings.TrimSpace(address.Surface),
		strings.TrimSpace(address.Channel),
		strings.TrimSpace(address.AccountID),
		strings.TrimSpace(address.ThreadID),
	}
	return strings.Join(parts, ":")
}

func remoteBindingDescriptor(address RemoteAddress, actor RemoteActor, owner string, expiresAt time.Time) gatewaycore.BindingDescriptor {
	return gatewaycore.BindingDescriptor{
		Surface:   strings.TrimSpace(address.Surface),
		ActorKind: firstNonEmptyHost(strings.TrimSpace(actor.Kind), "remote_user"),
		ActorID:   strings.TrimSpace(actor.ID),
		Owner:     strings.TrimSpace(owner),
		ExpiresAt: expiresAt,
	}
}

func firstNonEmptyHost(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
