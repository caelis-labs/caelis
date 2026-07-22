package acpagentbridge

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

// acpTaskStreamMux keeps the standard ACP bridge compatible with mounted
// RunCommand terminals without flattening subagent semantics into the main
// session/update stream. After its parent prompt is sealed, active command
// subscriptions remain available until their Task stream ends. Stopping the
// mux closes only delivery subscriptions, never Tasks.
type acpTaskStreamMux struct {
	ctx       context.Context
	cancel    context.CancelFunc
	service   taskstream.Service
	principal taskstream.Principal
	sessionID string
	events    chan eventstream.Envelope

	mu         sync.Mutex
	resolving  map[string]struct{}
	started    map[string]struct{}
	active     int
	sealed     bool
	eventsOnce sync.Once
	wg         sync.WaitGroup
}

func newACPTaskStreamMux(parent context.Context, service taskstream.Service, principal taskstream.Principal, sessionID string) *acpTaskStreamMux {
	if service == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	return &acpTaskStreamMux{
		ctx: ctx, cancel: cancel, service: service, principal: principal,
		sessionID: strings.TrimSpace(sessionID), events: make(chan eventstream.Envelope, 128),
		resolving: map[string]struct{}{}, started: map[string]struct{}{},
	}
}

func (a *RuntimeAgent) startACPTaskStreamMux(parent context.Context, sessionID string) *acpTaskStreamMux {
	if a == nil {
		return nil
	}
	mux := newACPTaskStreamMux(context.WithoutCancel(parent), a.taskStreams, a.taskStreamPrincipal, sessionID)
	if mux == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	if a.taskMuxes == nil {
		a.taskMuxes = map[string]map[*acpTaskStreamMux]struct{}{}
	}
	if a.taskMuxes[sessionID] == nil {
		a.taskMuxes[sessionID] = map[*acpTaskStreamMux]struct{}{}
	}
	a.taskMuxes[sessionID][mux] = struct{}{}
	a.mu.Unlock()
	return mux
}

// detachACPTaskStreamMux seals discovery after the parent Prompt ends, then
// keeps forwarding already-subscribed RunCommand delivery until those Task
// streams end or the Session is closed.
func (a *RuntimeAgent) detachACPTaskStreamMux(parent context.Context, mux *acpTaskStreamMux, cb acp.PromptCallbacks, sessionID string, filter *acpNarrativeFilter) {
	if a == nil || mux == nil {
		return
	}
	mux.Seal()
	deliveryCtx := context.WithoutCancel(parent)
	go func() {
		defer a.unregisterACPTaskStreamMux(sessionID, mux)
		for envelope := range mux.Events() {
			if err := a.emitControlEnvelope(deliveryCtx, cb, sessionID, nil, envelope, filter); err != nil {
				mux.Close()
				return
			}
		}
	}()
}

func (a *RuntimeAgent) unregisterACPTaskStreamMux(sessionID string, mux *acpTaskStreamMux) {
	if a == nil || mux == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	delete(a.taskMuxes[sessionID], mux)
	if len(a.taskMuxes[sessionID]) == 0 {
		delete(a.taskMuxes, sessionID)
	}
	a.mu.Unlock()
}

func (a *RuntimeAgent) closeACPTaskStreamMuxes(sessionID string) {
	if a == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	muxes := make([]*acpTaskStreamMux, 0, len(a.taskMuxes[sessionID]))
	for mux := range a.taskMuxes[sessionID] {
		muxes = append(muxes, mux)
	}
	delete(a.taskMuxes, sessionID)
	a.mu.Unlock()
	for _, mux := range muxes {
		mux.Close()
	}
}

func (m *acpTaskStreamMux) Events() <-chan eventstream.Envelope {
	if m == nil {
		return nil
	}
	return m.events
}

func (m *acpTaskStreamMux) Observe(envelope eventstream.Envelope) {
	if m == nil {
		return
	}
	callID, handle, ok := runCommandTaskStreamAnchor(envelope)
	if !ok {
		return
	}
	m.mu.Lock()
	_, resolving := m.resolving[callID]
	_, started := m.started[callID]
	if m.sealed || resolving || started {
		m.mu.Unlock()
		return
	}
	m.resolving[callID] = struct{}{}
	m.active++
	m.wg.Add(1)
	m.mu.Unlock()
	go m.resolveAndForward(callID, handle)
}

func (m *acpTaskStreamMux) resolveAndForward(callID, handle string) {
	defer m.wg.Done()
	defer m.finishOperation()
	directory, err := m.service.List(m.ctx, m.principal, taskstream.ListRequest{SessionID: m.sessionID})
	if err != nil {
		m.reportResolveFailure(callID, handle, err)
		return
	}
	var taskID string
	for _, descriptor := range directory.Tasks {
		if descriptor.Kind != task.KindCommand || strings.TrimSpace(descriptor.ParentTool.ToolCallID) != strings.TrimSpace(callID) {
			continue
		}
		if descriptorHandle := task.NormalizeHandle(descriptor.Handle); descriptorHandle != "" && descriptorHandle != task.NormalizeHandle(handle) {
			continue
		}
		if taskID != "" && taskID != strings.TrimSpace(descriptor.TaskID) {
			m.reportResolveFailure(callID, handle, fmt.Errorf("multiple command Tasks match tool call %q", callID))
			return
		}
		taskID = strings.TrimSpace(descriptor.TaskID)
	}
	if taskID == "" {
		m.reportResolveFailure(callID, handle, fmt.Errorf("task is not discoverable yet"))
		return
	}
	result, err := m.service.Subscribe(m.ctx, m.principal, taskstream.SubscribeRequest{
		SessionID: m.sessionID,
		TaskID:    taskID,
	})
	if err != nil {
		m.reportResolveFailure(callID, handle, err)
		return
	}
	if result.Subscription == nil {
		m.reportResolveFailure(callID, handle, fmt.Errorf("subscription was not created"))
		return
	}
	m.mu.Lock()
	delete(m.resolving, callID)
	m.started[callID] = struct{}{}
	m.mu.Unlock()
	defer result.Subscription.Close()
	for events := result.Subscription.Events(); ; {
		var envelope eventstream.Envelope
		select {
		case <-m.ctx.Done():
			return
		case received, ok := <-events:
			if !ok {
				if streamErr := result.Subscription.Err(); streamErr != nil {
					m.reportResolveFailure(callID, handle, streamErr)
				}
				return
			}
			envelope = received
		}
		// Standard ACP has no scoped multi-stream primitive. Only RunCommand's
		// already-mounted terminal extension is safe to project on the main wire.
		if envelope.Scope != eventstream.ScopeMain || envelope.Kind != eventstream.KindSessionUpdate || !envelopeHasTerminalDelivery(envelope) {
			continue
		}
		select {
		case <-m.ctx.Done():
			return
		case m.events <- envelope:
		}
	}
}

func (m *acpTaskStreamMux) reportResolveFailure(callID, handle string, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.resolving, strings.TrimSpace(callID))
	delete(m.started, strings.TrimSpace(callID))
	m.mu.Unlock()
	m.reportUnavailable(handle, err)
}

func (m *acpTaskStreamMux) reportUnavailable(handle string, err error) {
	if m == nil || err == nil || m.ctx.Err() != nil {
		return
	}
	notice := eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: m.sessionID,
		Scope:     eventstream.ScopeMain,
		ScopeID:   m.sessionID,
		Notice:    fmt.Sprintf("RunCommand live output is unavailable for Task %s: %v", strings.TrimSpace(handle), err),
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Meta: map[string]any{"task_stream": map[string]any{
			"target_handle": strings.TrimSpace(handle), "unavailable": true,
		}},
	}
	select {
	case <-m.ctx.Done():
	case m.events <- notice:
	}
}

func (m *acpTaskStreamMux) Close() {
	if m == nil {
		return
	}
	m.Seal()
	m.cancel()
	m.wg.Wait()
	m.closeEvents()
}

// Seal prevents discovery of new command Tasks while allowing already-started
// subscriptions to deliver until their Task stream ends.
func (m *acpTaskStreamMux) Seal() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.sealed = true
	closeEvents := m.active == 0
	m.mu.Unlock()
	if closeEvents {
		m.closeEvents()
	}
}

func (m *acpTaskStreamMux) finishOperation() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.active > 0 {
		m.active--
	}
	closeEvents := m.sealed && m.active == 0
	m.mu.Unlock()
	if closeEvents {
		m.closeEvents()
	}
}

func (m *acpTaskStreamMux) closeEvents() {
	if m == nil {
		return
	}
	m.eventsOnce.Do(func() { close(m.events) })
}

func runCommandTaskStreamAnchor(envelope eventstream.Envelope) (string, string, bool) {
	if envelope.Kind != eventstream.KindSessionUpdate || (envelope.Scope != "" && envelope.Scope != eventstream.ScopeMain) {
		return "", "", false
	}
	meta := eventstream.UpdateMeta(envelope.Update)
	toolName := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName)
	if identity.CanonicalOrSelf(toolName) != identity.RunCommand {
		return "", "", false
	}
	var input, output map[string]any
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
	case schema.ToolCallUpdate:
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
	default:
		return "", "", false
	}
	handle := display.ToolTaskHandle(input, output, meta)
	callID := strings.TrimSpace(taskStreamToolCallID(envelope.Update))
	return callID, strings.TrimSpace(handle), callID != "" && strings.TrimSpace(handle) != ""
}

func taskStreamToolCallID(update schema.Update) string {
	switch typed := update.(type) {
	case schema.ToolCall:
		return typed.ToolCallID
	case schema.ToolCallUpdate:
		return typed.ToolCallID
	default:
		return ""
	}
}

func envelopeHasTerminalDelivery(envelope eventstream.Envelope) bool {
	meta := eventstream.UpdateMeta(envelope.Update)
	output, ok := metautil.TerminalOutput(meta)
	if ok && output.Data != "" {
		return true
	}
	_, ok = metautil.TerminalExit(meta)
	return ok
}
