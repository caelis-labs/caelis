package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/mod/semver"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

// ServeACPDraft serves the explicitly pinned experimental profile. It never
// negotiates down into the stable ACP data path. Backend Thread ownership and
// native approval resolution remain shared with ServeACP.
func (b *Backend) ServeACPDraft(ctx context.Context, options ConnectionOptions, schema string, input io.Reader, output io.Writer) error {
	if schema != acpdraft.SchemaTag {
		return errors.New("codex adapter: incompatible ACP draft schema")
	}
	if !draftNativeCorrelationSupported(b.nativeUserAgent) {
		return errors.New("codex adapter: ACP draft requires Codex app-server 0.160.0 or newer with native clientUserMessageId correlation")
	}
	if input == nil || output == nil {
		return errors.New("codex adapter: ACP input and output are required")
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return errors.New("codex adapter: backend is closed")
	}
	core := &agent{backend: b, options: normalizeConnectionOptions(options), sessions: make(map[string]*sessionState), connectionReady: make(chan struct{})}
	draft := &draftAgent{core: core, sessions: make(map[string]*draftSession)}
	core.draft = draft
	bridge := &draftBridge{agent: draft}
	core.connection = bridge
	connection, err := v2.NewAgentSideConnection(draft, output, input)
	if err != nil {
		return err
	}
	draft.connection = connection
	close(core.connectionReady)
	defer func() { draft.closeAll(); _ = connection.Close() }()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-b.Done():
		return fmt.Errorf("codex adapter: backend lost: %w", b.Err())
	case <-connection.Done():
		err := connection.Err()
		if errors.Is(err, acp.ErrPeerClosed) {
			return nil
		}
		return err
	}
}

type draftAgent struct {
	core        *agent
	connection  *v2.AgentSideConnection
	mu          sync.Mutex
	initialized bool
	sessions    map[string]*draftSession
}

type draftSession struct {
	mu                sync.Mutex
	pending           *draftInsertion
	terminalTurn      string
	cancelRequested   bool
	permissionCancels map[uint64]draftPermission
	nextPermission    uint64
	// pubMu serializes state changes from native notifications and permission
	// responses so a late running update cannot follow its terminal idle update.
	pubMu sync.Mutex
}

type draftPermission struct {
	activity string
	cancel   context.CancelFunc
}

func (s *draftSession) hasPermissionLocked(activity string) bool {
	for _, permission := range s.permissionCancels {
		if permission.activity == activity {
			return true
		}
	}
	return false
}

type draftInsertion struct {
	turnID    string
	token     string
	echoTurn  string
	messageID v2.MessageId
	changed   chan struct{}
	// Native acknowledgement can still be pending when its projection fails.
	// The original request is cancelled without claiming it was not accepted.
	nativeCancel context.CancelCauseFunc
}

func (d *draftAgent) session(id string) *draftSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.sessions[id]
	if state == nil {
		state = &draftSession{}
		d.sessions[id] = state
	}
	return state
}
func (d *draftAgent) ready() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.initialized {
		return acp.NewInvalidRequest(map[string]any{"error": "initialize must succeed before Session methods"})
	}
	return nil
}
func (d *draftAgent) Initialize(ctx context.Context, request v2.InitializeRequest) (v2.InitializeResponse, error) {
	if err := acpdraft.ValidateInitialize(request); err != nil {
		return v2.InitializeResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	d.mu.Lock()
	if d.initialized {
		d.mu.Unlock()
		return v2.InitializeResponse{}, acp.NewInvalidRequest(map[string]any{"error": "initialize has already been received"})
	}
	// Hold admission while the original account owner performs initialization.
	response, err := d.core.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1, ClientCapabilities: acp.ClientCapabilities{Session: &acp.ClientSessionCapabilities{Notices: &acp.NoticeCapabilities{}}}})
	if err != nil {
		d.mu.Unlock()
		return v2.InitializeResponse{}, err
	}
	d.initialized = true
	d.mu.Unlock()
	info, err := draftConvert[v2.Implementation](response.AgentInfo)
	if err != nil {
		return v2.InitializeResponse{}, err
	}
	// Native turn/start is start-or-steer, and native MA v1 can independently
	// start a managed root. The native API has no idle-only compare-and-set.
	return v2.InitializeResponse{ProtocolVersion: 2, Info: info, Meta: acpdraft.InitializeMetaWithIdleOnly(false), Capabilities: v2.AgentCapabilities{Session: &v2.SessionCapabilities{
		AdditionalDirectories: &v2.SessionAdditionalDirectoriesCapabilities{},
		Prompt:                &v2.PromptCapabilities{Image: &v2.PromptImageCapabilities{}, EmbeddedContext: &v2.PromptEmbeddedContextCapabilities{}},
		Mcp:                   &v2.McpCapabilities{Stdio: &v2.McpStdioCapabilities{}},
	}}}, nil
}
func (d *draftAgent) NewSession(ctx context.Context, request v2.NewSessionRequest) (v2.NewSessionResponse, error) {
	if err := d.ready(); err != nil {
		return v2.NewSessionResponse{}, err
	}
	servers, err := draftMCPServers(request.McpServers)
	native := acp.NewSessionRequest{Cwd: string(request.Cwd), AdditionalDirectories: draftPaths(request.AdditionalDirectories), McpServers: servers}
	if err != nil {
		return v2.NewSessionResponse{}, err
	}
	response, err := d.core.NewSession(ctx, native)
	if err != nil {
		return v2.NewSessionResponse{}, err
	}
	d.session(string(response.SessionId))
	state, lookupErr := d.core.lookupSession(string(response.SessionId))
	if lookupErr != nil {
		return v2.NewSessionResponse{}, lookupErr
	}
	state.promptMu.Lock()
	defer state.promptMu.Unlock()
	state.mu.Lock()
	state.draftLoading = false
	state.mu.Unlock()
	if err := d.sendState(string(response.SessionId), "", v2.IdleUpdate("end_turn")); err != nil {
		return v2.NewSessionResponse{}, err
	}
	config, err := draftConfigOptions(response.ConfigOptions)
	return v2.NewSessionResponse{SessionId: v2.SessionId(response.SessionId), ConfigOptions: config}, err
}
func (d *draftAgent) ResumeSession(ctx context.Context, request v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
	if err := d.ready(); err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	if request.ReplayFrom != nil && request.ReplayFrom.Start == nil {
		return v2.ResumeSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unsupported replay cursor"})
	}
	servers, err := draftMCPServers(request.McpServers)
	if err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	native := acp.LoadSessionRequest{Cwd: string(request.Cwd), SessionId: acp.SessionId(request.SessionId), AdditionalDirectories: draftPaths(request.AdditionalDirectories), McpServers: servers}
	response, err := d.core.loadSessionWithReplay(ctx, native, request.ReplayFrom != nil)
	if err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	options := response.ConfigOptions
	state, lookupErr := d.core.lookupSession(string(request.SessionId))
	if lookupErr != nil {
		return v2.ResumeSessionResponse{}, lookupErr
	}
	state.mu.Lock()
	state.draftLoading = false
	state.mu.Unlock()
	config, err := draftConfigOptions(options)
	if err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	return v2.ResumeSessionResponse{ConfigOptions: config}, nil
}
func (d *draftAgent) ListSessions(ctx context.Context, request v2.ListSessionsRequest) (v2.ListSessionsResponse, error) {
	if err := d.ready(); err != nil {
		return v2.ListSessionsResponse{}, err
	}
	params := map[string]any{"limit": 100}
	if request.Cursor != nil {
		params["cursor"] = *request.Cursor
	}
	var requestedCWD string
	if request.Cwd != nil {
		clean, err := cleanAbsolute(string(*request.Cwd))
		if err != nil {
			return v2.ListSessionsResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		if _, err = d.core.options.Workspace.validate(clean, nil); err != nil {
			return v2.ListSessionsResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		requestedCWD = clean
		params["cwd"] = clean
	}
	var response struct {
		Data []struct {
			ID      string `json:"id"`
			CWD     string `json:"cwd"`
			Name    string `json:"name"`
			Preview string `json:"preview"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := d.core.backend.rpc.Request(ctx, "thread/list", params, &response); err != nil {
		return v2.ListSessionsResponse{}, err
	}
	result := v2.ListSessionsResponse{Sessions: []v2.SessionInfo{}}
	for _, thread := range response.Data {
		clean, err := cleanAbsolute(thread.CWD)
		if err != nil {
			continue
		}
		if _, err = d.core.options.Workspace.validate(clean, nil); err != nil || (requestedCWD != "" && requestedCWD != clean) || strings.TrimSpace(thread.ID) == "" {
			continue
		}
		result.Sessions = append(result.Sessions, v2.SessionInfo{SessionId: v2.SessionId(thread.ID), Cwd: v2.AbsolutePath(clean), Title: optionalString(firstNonEmpty(thread.Name, thread.Preview))})
	}
	if response.NextCursor != nil {
		cursor := v2.SessionListCursor(*response.NextCursor)
		result.NextCursor = &cursor
	}
	return result, nil
}
func (d *draftAgent) CloseSession(ctx context.Context, request v2.CloseSessionRequest) (v2.CloseSessionResponse, error) {
	if err := d.ready(); err != nil {
		return v2.CloseSessionResponse{}, err
	}
	_ = d.Cancel(ctx, v2.CancelSessionNotification{SessionId: request.SessionId})
	state, lookupErr := d.core.lookupSession(string(request.SessionId))
	if lookupErr == nil {
		state.promptMu.Lock()
		defer state.promptMu.Unlock()
	}
	native, err := draftConvert[acp.CloseSessionRequest](request)
	if err != nil {
		return v2.CloseSessionResponse{}, err
	}
	_, err = d.core.CloseSession(ctx, native)
	return v2.CloseSessionResponse{}, err
}
func (d *draftAgent) SetSessionConfigOption(ctx context.Context, request v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error) {
	if err := d.ready(); err != nil {
		return v2.SetSessionConfigOptionResponse{}, err
	}
	native, err := draftConvert[acp.SetSessionConfigOptionRequest](request)
	if err != nil {
		return v2.SetSessionConfigOptionResponse{}, err
	}
	state, err := d.core.lookupSession(draftConfigSession(request))
	if err != nil {
		return v2.SetSessionConfigOptionResponse{}, err
	}
	state.mu.Lock()
	active := state.activeTurnID != ""
	state.mu.Unlock()
	if active {
		return v2.SetSessionConfigOptionResponse{}, acp.NewInvalidRequest(map[string]any{"error": "configuration cannot change during a Turn"})
	}
	response, err := d.core.SetSessionConfigOption(ctx, native)
	if err != nil {
		return v2.SetSessionConfigOptionResponse{}, err
	}
	config, err := draftConfigOptions(response.ConfigOptions)
	return v2.SetSessionConfigOptionResponse{ConfigOptions: config}, err
}
func draftConfigSession(request v2.SetSessionConfigOptionRequest) string {
	if request.Id != nil {
		return string(request.Id.SessionId)
	}
	if request.Boolean != nil {
		return string(request.Boolean.SessionId)
	}
	return ""
}
func (d *draftAgent) LoginAuth(context.Context, v2.LoginAuthRequest) (v2.LoginAuthResponse, error) {
	return v2.LoginAuthResponse{}, acp.NewMethodNotFound(v2.AgentMethodAuthLogin)
}
func (d *draftAgent) LogoutAuth(context.Context, v2.LogoutAuthRequest) (v2.LogoutAuthResponse, error) {
	return v2.LogoutAuthResponse{}, acp.NewMethodNotFound(v2.AgentMethodAuthLogout)
}
func (d *draftAgent) closeAll() {
	ctx, cancel := context.WithTimeout(context.Background(), codexThreadUnsubscribeTimeout)
	defer cancel()
	d.core.mu.Lock()
	states := make([]*sessionState, 0, len(d.core.sessions))
	for _, state := range d.core.sessions {
		states = append(states, state)
	}
	d.core.mu.Unlock()
	for _, state := range states {
		if state.route.failure() != nil {
			// The failed route owns cancellation and its targeted interrupt.
			continue
		}
		_ = d.Cancel(ctx, v2.CancelSessionNotification{SessionId: v2.SessionId(state.threadID)})
	}
	d.core.closeAll()
}
func draftConvert[T any](value any) (T, error) {
	var result T
	raw, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}

func draftPaths(paths []v2.AbsolutePath) []string {
	result := make([]string, len(paths))
	for i, path := range paths {
		result[i] = string(path)
	}
	return result
}

func draftMCPServers(servers []v2.McpServer) ([]acp.McpServer, error) {
	result := make([]acp.McpServer, 0, len(servers))
	for _, server := range servers {
		if server.Stdio == nil || server.Http != nil || server.Other != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": "codex adapter MCP injection requires stdio"})
		}
		s := server.Stdio
		env := make([]acp.EnvVariable, 0, len(s.Env))
		for _, value := range s.Env {
			env = append(env, acp.EnvVariable{Name: value.Name, Value: value.Value})
		}
		result = append(result, acp.McpServer{Stdio: &acp.McpServerStdio{Meta: s.Meta, Name: s.Name, Command: string(s.Command), Args: append([]string{}, s.Args...), Env: env}})
	}
	return result, nil
}

func draftConfigOptions(options []acp.SessionConfigOption) ([]v2.SessionConfigOption, error) {
	result := make([]v2.SessionConfigOption, 0, len(options))
	for _, option := range options {
		raw, err := json.Marshal(option)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		fields["configId"] = fields["id"]
		delete(fields, "id")
		raw, err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		var converted v2.SessionConfigOption
		if err = json.Unmarshal(raw, &converted); err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

// The checked minimum is the installed native schema with request-owned
// clientUserMessageId and the corresponding userMessage.clientId echo. Older
// app-servers may ignore unknown JSON fields, so they are rejected before work.
func draftNativeCorrelationSupported(userAgent string) bool {
	first, _, _ := strings.Cut(userAgent, " ")
	_, version, ok := strings.Cut(first, "/")
	return ok && semver.IsValid("v"+version) && semver.Compare("v"+version, "v0.160.0") >= 0
}
