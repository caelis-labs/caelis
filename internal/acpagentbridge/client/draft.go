package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

type draftActivitySettlement struct {
	reason   string
	sequence uint64
}

func (s draftActivitySettlement) result() (PromptResponse, error) {
	var err error
	if s.reason == "error" {
		err = &draftActivityError{}
	}
	return PromptResponse{StopReason: s.reason}, err
}

type draftClient struct {
	client       *Client
	mu           sync.Mutex
	sessions     map[string]*draftSession
	loginMethods map[string]bool
	cacheBytes   int
	failure      error
}

type draftSession struct {
	activityID         string
	active             bool
	prompt             *draftPromptCall
	receiptWaiters     int
	messageSequence    uint64
	userMessages       map[string]uint64
	changed            chan struct{}
	messages           map[string]*draftMessage
	terminals          map[string]*draftTerminal
	tools              map[string]*draftTool
	narrativeKey       string
	settledActivities  map[string]draftActivitySettlement
	activitySequence   uint64
	activeSequence     uint64
	activityCacheBytes int
	cacheBytes         int
}

func newDraftClient(client *Client) *draftClient {
	return &draftClient{client: client, sessions: make(map[string]*draftSession)}
}

func (d *draftClient) sessionLocked(id string) *draftSession {
	s := d.sessions[id]
	if s == nil {
		s = &draftSession{userMessages: make(map[string]uint64), changed: make(chan struct{}), messages: make(map[string]*draftMessage), terminals: make(map[string]*draftTerminal), settledActivities: make(map[string]draftActivitySettlement)}
		d.sessions[id] = s
	}
	return s
}

// ensureSessionLocked is the only externally driven Session allocation path.
// Keep identity limits at admission as well as notification ingress.
func (d *draftClient) ensureSessionLocked(id string) (*draftSession, error) {
	if len(id) > 4096 || strings.TrimSpace(id) == "" {
		return nil, errors.New("acp draft: Session identity exceeds bridge limit")
	}
	if session := d.sessions[id]; session != nil {
		return session, nil
	}
	if len(d.sessions) >= draftMaxSessions {
		return nil, errors.New("acp draft: retained Session limit exceeded")
	}
	baseline := len(id) + 512
	if d.cacheBytes+baseline > draftMaxClientCacheBytes {
		return nil, errors.New("acp draft: retained display cache byte limit exceeded")
	}
	session := d.sessionLocked(id)
	session.cacheBytes = baseline
	d.cacheBytes += baseline
	return session, nil
}

func (d *draftClient) failureCause() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failure
}

func (d *draftClient) changedLocked(session *draftSession) {
	close(session.changed)
	session.changed = make(chan struct{})
}

func (d *draftClient) initialize(ctx context.Context) (InitializeResponse, error) {
	info := v2.Implementation{Name: "caelis", Version: "development"}
	if source := d.client.cfg.ClientInfo; source != nil {
		info.Name, info.Title, info.Version = source.Name, source.Title, source.Version
	}
	capabilities := v2.ClientCapabilities{}
	if d.client.cfg.TerminalAuth {
		capabilities.Auth = &v2.ClientAuthCapabilities{Terminal: &v2.TerminalAuthCapabilities{}}
	}
	response, err := sendRequest[v2.InitializeResponse](d.client, ctx, v2.AgentMethodInitialize, v2.InitializeRequest{ProtocolVersion: 2, Info: info, Capabilities: capabilities, Meta: acpdraft.InitializeMeta()})
	if err != nil {
		return InitializeResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return InitializeResponse{}, err
	}
	if err := acpdraft.ValidatePeer(response); err != nil {
		return InitializeResponse{}, err
	}
	caps := AgentCapabilities{LoadSession: true, SessionCapabilities: map[string]json.RawMessage{"resume": json.RawMessage(`{}`), "list": json.RawMessage(`{}`), "close": json.RawMessage(`{}`)}}
	if prompt := response.Capabilities.Session.Prompt; prompt != nil {
		caps.PromptCapabilities = acpsdk.PromptCapabilities{Image: prompt.Image != nil, Audio: prompt.Audio != nil, EmbeddedContext: prompt.EmbeddedContext != nil, Meta: cloneRawMessages(prompt.Meta)}
	}
	if mcp := response.Capabilities.Session.Mcp; mcp != nil {
		caps.McpCapabilities = acpsdk.McpCapabilities{Http: mcp.Http != nil, Meta: cloneRawMessages(mcp.Meta)}
	}
	meta := cloneRawMessages(response.Meta)
	// This is the existing Host-private capability view, not a v1 wire advertisement.
	meta[SessionSteeringMetaKey] = json.RawMessage(`{"supported":true}`)
	methods, loginMethods, err := draftAuthMethods(response.AuthMethods)
	if err != nil {
		return InitializeResponse{}, err
	}
	d.mu.Lock()
	d.loginMethods = loginMethods
	d.mu.Unlock()
	return InitializeResponse{ProtocolVersion: 2, AgentCapabilities: caps, AgentInfo: &Implementation{Name: response.Info.Name, Version: response.Info.Version, Title: stringValue(response.Info.Title)}, AuthMethods: methods, Meta: meta}, nil
}

// draftAuthMethods supplies the existing Host-private authentication owner
// with its maintained descriptor shape, while requests stay standard v2.
func draftAuthMethods(methods []v2.AuthMethod) ([]json.RawMessage, map[string]bool, error) {
	out := make([]json.RawMessage, 0, len(methods))
	loginMethods := make(map[string]bool)
	for _, method := range methods {
		var value any
		switch {
		case method.Agent != nil:
			m := method.Agent
			value = map[string]any{"id": m.MethodId, "name": m.Name, "description": m.Description, "type": "agent"}
			loginMethods[string(m.MethodId)] = true
		case method.Terminal != nil:
			m := method.Terminal
			env := make(map[string]string, len(m.Env))
			for _, variable := range m.Env {
				if _, exists := env[variable.Name]; exists {
					return nil, nil, errors.New("acp draft: duplicate terminal auth environment variable")
				}
				env[variable.Name] = variable.Value
			}
			value = map[string]any{"id": m.MethodId, "name": m.Name, "description": m.Description, "type": "terminal", "args": m.Args, "env": env}
		default:
			value = method
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, raw)
	}
	return out, loginMethods, nil
}

func draftConvert[T any](value any) (T, error) {
	var out T
	raw, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err = decoder.Decode(&out)
	return out, err
}

// draftMCPServers retains the stable Host-private declaration while emitting
// only the selected draft's standard transports. No unsupported transport is
// silently discarded and no server credentials leave their existing owner.
func draftMCPServers(servers []acpsdk.McpServer) ([]v2.McpServer, error) {
	out := make([]v2.McpServer, 0, len(servers))
	for _, server := range servers {
		if server.Sse != nil {
			return nil, errors.New("acp draft: SSE MCP transport has no standard equivalent in the selected ACP draft")
		}
		raw, err := json.Marshal(server)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		if server.Stdio != nil {
			fields["type"] = json.RawMessage(`"stdio"`)
		}
		raw, err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		var converted v2.McpServer
		if err := json.Unmarshal(raw, &converted); err != nil {
			return nil, err
		}
		if err := converted.Validate(); err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func (d *draftClient) newSession(ctx context.Context, cwd string, meta map[string]any) (NewSessionResponse, error) {
	rawMeta, err := rawMessagesFromValues(meta)
	if err != nil {
		return NewSessionResponse{}, err
	}
	mcp, err := draftMCPServers(d.client.cfg.MCPServers)
	if err != nil {
		return NewSessionResponse{}, err
	}
	response, err := sendRequest[v2.NewSessionResponse](d.client, ctx, v2.AgentMethodSessionNew, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd), McpServers: mcp, Meta: rawMeta})
	if err != nil {
		return NewSessionResponse{}, err
	}
	if strings.TrimSpace(string(response.SessionId)) == "" {
		return NewSessionResponse{}, errors.New("acp draft: empty Session identity")
	}
	if d.client.cfg.MCPGrant != nil {
		if err := d.client.cfg.MCPGrant.Bind(string(response.SessionId)); err != nil {
			return NewSessionResponse{}, err
		}
	}
	return NewSessionResponse{SessionID: string(response.SessionId), ConfigOptions: draftConfigOptions(response.ConfigOptions)}, nil
}

func (d *draftClient) resumeRequest(sessionID, cwd string, meta map[string]any, replay bool) (v2.ResumeSessionRequest, error) {
	rawMeta, err := rawMessagesFromValues(meta)
	if err != nil {
		return v2.ResumeSessionRequest{}, err
	}
	mcp, err := draftMCPServers(d.client.cfg.MCPServers)
	if err != nil {
		return v2.ResumeSessionRequest{}, err
	}
	request := v2.ResumeSessionRequest{SessionId: v2.SessionId(sessionID), Cwd: v2.AbsolutePath(cwd), McpServers: mcp, Meta: rawMeta}
	if replay {
		var cursor v2.ReplayFrom
		if err := json.Unmarshal([]byte(`{"type":"start"}`), &cursor); err != nil {
			return request, err
		}
		request.ReplayFrom = &cursor
	}
	return request, nil
}

func (d *draftClient) resumeSession(ctx context.Context, sessionID, cwd string, meta map[string]any) (ResumeSessionResponse, error) {
	request, err := d.resumeRequest(sessionID, cwd, meta, false)
	if err != nil {
		return ResumeSessionResponse{}, err
	}
	response, err := sendRequest[v2.ResumeSessionResponse](d.client, ctx, v2.AgentMethodSessionResume, request)
	if err != nil {
		return ResumeSessionResponse{}, err
	}
	if d.client.cfg.MCPGrant != nil {
		if err := d.client.cfg.MCPGrant.Bind(sessionID); err != nil {
			return ResumeSessionResponse{}, err
		}
	}
	return ResumeSessionResponse{ConfigOptions: draftConfigOptions(response.ConfigOptions)}, nil
}

func (d *draftClient) loadSession(ctx context.Context, sessionID, cwd string, meta map[string]any, replayEnd func(context.Context) error) (LoadSessionResponse, error) {
	request, err := d.resumeRequest(sessionID, cwd, meta, true)
	if err != nil {
		return LoadSessionResponse{}, err
	}
	response, err := acpsdk.SendRequestWithResponseHook[v2.ResumeSessionResponse](d.client.conn, ctx, v2.AgentMethodSessionResume, request, func(hookCtx context.Context, _ v2.ResumeSessionResponse) error {
		if replayEnd != nil {
			return replayEnd(hookCtx)
		}
		return nil
	})
	if err != nil {
		return LoadSessionResponse{}, err
	}
	if d.client.cfg.MCPGrant != nil {
		if err := d.client.cfg.MCPGrant.Bind(sessionID); err != nil {
			return LoadSessionResponse{}, err
		}
	}
	return LoadSessionResponse{ConfigOptions: draftConfigOptions(response.ConfigOptions)}, nil
}

func (d *draftClient) listSessions(ctx context.Context, request acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	converted, err := draftConvert[v2.ListSessionsRequest](request)
	if err != nil {
		return acpsdk.ListSessionsResponse{}, err
	}
	response, err := sendRequest[v2.ListSessionsResponse](d.client, ctx, v2.AgentMethodSessionList, converted)
	if err != nil {
		return acpsdk.ListSessionsResponse{}, err
	}
	return draftConvert[acpsdk.ListSessionsResponse](response)
}

func (d *draftClient) setConfig(ctx context.Context, sessionID, configID string, value any) (SetSessionConfigOptionResponse, error) {
	switch value.(type) {
	case string, bool:
	default:
		return SetSessionConfigOptionResponse{}, fmt.Errorf("acp client: unsupported config value %T", value)
	}
	request, err := draftConvert[v2.SetSessionConfigOptionRequest](map[string]any{"sessionId": sessionID, "configId": configID, "value": value, "type": func() string {
		if _, ok := value.(bool); ok {
			return "boolean"
		}
		return "id"
	}()})
	if err != nil {
		return SetSessionConfigOptionResponse{}, err
	}
	response, err := sendRequest[v2.SetSessionConfigOptionResponse](d.client, ctx, v2.AgentMethodSessionSetConfigOption, request)
	if err != nil {
		return SetSessionConfigOptionResponse{}, err
	}
	return SetSessionConfigOptionResponse{ConfigOptions: draftConfigOptions(response.ConfigOptions)}, nil
}

func draftConfigOptions(options []v2.SessionConfigOption) []SessionConfigOption {
	out := make([]SessionConfigOption, 0, len(options))
	for _, option := range options {
		raw, err := json.Marshal(option)
		if err != nil {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			continue
		}
		fields["id"] = fields["configId"]
		delete(fields, "configId")
		raw, err = json.Marshal(fields)
		if err != nil {
			continue
		}
		var normalized SessionConfigOption
		if json.Unmarshal(raw, &normalized) == nil {
			out = append(out, normalized)
		}
	}
	return out
}

func (d *draftClient) authenticate(ctx context.Context, methodID string) error {
	d.mu.Lock()
	allowed := d.loginMethods[methodID]
	d.mu.Unlock()
	if !allowed {
		return errors.New("acp draft: remote authentication method was not advertised")
	}
	request, err := draftConvert[v2.LoginAuthRequest](map[string]any{"methodId": methodID})
	if err != nil {
		return err
	}
	_, err = sendRequest[v2.LoginAuthResponse](d.client, ctx, v2.AgentMethodAuthLogin, request)
	return err
}

func (d *draftClient) handleRequest(ctx context.Context, method string, raw json.RawMessage) (any, *acpsdk.RequestError) {
	if inbound, ok := acpsdk.InboundInfoFromContext(ctx); ok {
		var id v2.RequestId
		if json.Unmarshal(inbound.RequestID, &id) != nil {
			return nil, acpsdk.NewInvalidRequest(map[string]any{"error": "ACP v2 requires a string or integer request ID"})
		}
	}
	if method != v2.ClientMethodSessionRequestPermission {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	var request v2.RequestPermissionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if err := request.Validate(); err != nil {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if d.client.cfg.OnDraftPermissionRequest == nil {
		return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Cancelled: &v2.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}}}, nil
	}
	response, err := d.client.cfg.OnDraftPermissionRequest(ctx, request)
	if err != nil {
		return nil, acpsdk.NewInternalError(map[string]any{"error": err.Error()})
	}
	if response.Outcome.Selected != nil {
		valid := false
		for _, option := range request.Options {
			if option.OptionId == response.Outcome.Selected.OptionId {
				valid = true
				break
			}
		}
		if !valid {
			return nil, acpsdk.NewInvalidParams(map[string]any{"error": "permission response did not select an original option"})
		}
	}
	if err := response.Validate(); err != nil {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	return response, nil
}

func (d *draftClient) fail(err error) {
	d.mu.Lock()
	if d.failure == nil {
		d.failure = err
	}
	for _, session := range d.sessions {
		if session.prompt != nil {
			session.prompt.finishLocked(PromptResponse{}, err)
		}
		d.changedLocked(session)
	}
	d.mu.Unlock()
	_ = d.client.conn.Close()
}

func (d *draftClient) observeState(sessionID string, fields map[string]json.RawMessage) error {
	var state, reason string
	_ = json.Unmarshal(fields["state"], &state)
	_ = json.Unmarshal(fields["stopReason"], &reason)
	var meta map[string]json.RawMessage
	_ = json.Unmarshal(fields["_meta"], &meta)
	activity := acpdraft.ActivityID(meta)
	d.mu.Lock()
	defer d.mu.Unlock()
	session := d.sessionLocked(sessionID)
	if activity == "" {
		if state == "idle" && session.activityID == "" && !session.active {
			return nil
		}
		return errors.New("acp draft: Activity state has no owner identity")
	}
	switch state {
	case "running", "requires_action":
		if _, settled := session.settledActivities[activity]; settled {
			return errors.New("acp draft: settled Activity identity was reused")
		}
		if session.active && session.activityID != activity {
			return errors.New("acp draft: Activity changed without settlement")
		}
		if session.activityID != activity {
			session.activitySequence++
			session.activeSequence = session.activitySequence
		}
		if err := d.reserveCacheLocked(session, len(activity), session.activityCacheBytes); err != nil {
			return err
		}
		session.activityCacheBytes = len(activity)
		session.activityID, session.active = activity, true
	case "idle":
		if reason == "" {
			return errors.New("acp draft: settled Activity has no completion reason")
		}
		if session.active && session.activityID != activity {
			return errors.New("acp draft: unrelated Activity settlement")
		}
		if settlement, known := session.settledActivities[activity]; known {
			if settlement.reason != reason {
				return errors.New("acp draft: Activity settlement changed its reason")
			}
		} else {
			if len(session.settledActivities) >= draftMaxSessionRecords {
				return errors.New("acp draft: retained Activity identity limit exceeded")
			}
			if err := d.reserveCacheLocked(session, len(activity)+len(reason)+32, 0); err != nil {
				return err
			}
			if session.activityID != activity {
				session.activitySequence++
				session.activeSequence = session.activitySequence
			}
			session.settledActivities[activity] = draftActivitySettlement{reason: reason, sequence: session.activeSequence}
		}
		if err := d.reserveCacheLocked(session, len(activity), session.activityCacheBytes); err != nil {
			return err
		}
		session.activityCacheBytes = len(activity)
		session.activityID, session.active = activity, false
		if call := session.prompt; call != nil && call.activityID == activity {
			call.finishLocked(session.settledActivities[activity].result())
		}
	default:
		return fmt.Errorf("acp draft: unsupported foreground state %q", state)
	}
	d.changedLocked(session)
	return nil
}
