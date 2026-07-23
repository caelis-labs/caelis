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
// RunCommand and Spawn terminals without flattening subagent semantics into
// the main session/update stream. After its parent prompt is sealed, active
// command subscriptions remain available until their Task stream ends.
// Stopping the mux closes only delivery subscriptions, never Tasks.
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
	boundaries map[string]chan struct{}
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
		resolving: map[string]struct{}{}, started: map[string]struct{}{}, boundaries: map[string]chan struct{}{},
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
// keeps forwarding already-subscribed terminal delivery until those Task
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
	anchor, ok := acpTaskStreamAnchorFromEnvelope(envelope)
	if !ok {
		return
	}
	m.mu.Lock()
	_, resolving := m.resolving[anchor.callID]
	_, started := m.started[anchor.callID]
	if m.sealed || resolving || started {
		m.mu.Unlock()
		return
	}
	m.resolving[anchor.callID] = struct{}{}
	if m.boundaries[anchor.callID] == nil {
		m.boundaries[anchor.callID] = make(chan struct{}, 1)
	}
	m.active++
	m.wg.Add(1)
	m.mu.Unlock()
	go m.resolveAndForward(anchor)
}

type acpTaskStreamAnchor struct {
	callID string
	handle string
	kind   task.Kind
}

func (m *acpTaskStreamMux) resolveAndForward(anchor acpTaskStreamAnchor) {
	defer m.wg.Done()
	defer m.finishOperation()
	directory, err := m.service.List(m.ctx, m.principal, taskstream.ListRequest{SessionID: m.sessionID})
	if err != nil {
		m.reportResolveFailure(anchor, err)
		return
	}
	var taskID string
	for _, descriptor := range directory.Tasks {
		if descriptor.Kind != anchor.kind || strings.TrimSpace(descriptor.ParentTool.ToolCallID) != anchor.callID ||
			identity.CanonicalOrSelf(descriptor.ParentTool.ToolName) != taskStreamParentToolName(anchor.kind) {
			continue
		}
		if descriptorHandle := task.NormalizeHandle(descriptor.Handle); descriptorHandle != "" && descriptorHandle != task.NormalizeHandle(anchor.handle) {
			continue
		}
		if taskID != "" && taskID != strings.TrimSpace(descriptor.TaskID) {
			m.reportResolveFailure(anchor, fmt.Errorf("multiple %s Tasks match tool call %q", anchor.kind, anchor.callID))
			return
		}
		taskID = strings.TrimSpace(descriptor.TaskID)
	}
	if taskID == "" {
		m.reportResolveFailure(anchor, fmt.Errorf("task is not discoverable yet"))
		return
	}
	result, err := m.service.Subscribe(m.ctx, m.principal, taskstream.SubscribeRequest{
		SessionID: m.sessionID,
		TaskID:    taskID,
	})
	if err != nil {
		m.reportResolveFailure(anchor, err)
		return
	}
	if result.Subscription == nil {
		m.reportResolveFailure(anchor, fmt.Errorf("subscription was not created"))
		return
	}
	m.mu.Lock()
	delete(m.resolving, anchor.callID)
	m.started[anchor.callID] = struct{}{}
	m.mu.Unlock()
	defer result.Subscription.Close()
	sawBoundary := false
	defer func() {
		if !sawBoundary {
			m.signalBoundary(anchor.callID)
		}
	}()
	for events := result.Subscription.Events(); ; {
		var envelope eventstream.Envelope
		select {
		case <-m.ctx.Done():
			return
		case received, ok := <-events:
			if !ok {
				if streamErr := result.Subscription.Err(); streamErr != nil {
					m.reportResolveFailure(anchor, streamErr)
				}
				return
			}
			envelope = received
		}
		if anchor.kind == task.KindSubagent && envelope.Kind == eventstream.KindLifecycle && envelope.Final {
			if acpSubagentTaskLifecycleAllowed(anchor, envelope) {
				select {
				case <-m.ctx.Done():
					return
				case m.events <- envelope:
				}
			}
			sawBoundary = true
			m.signalBoundary(anchor.callID)
			return
		}
		if !acpTaskStreamEnvelopeAllowed(anchor, envelope) {
			continue
		}
		select {
		case <-m.ctx.Done():
			return
		case m.events <- envelope:
		}
	}
}

func (m *acpTaskStreamMux) reportResolveFailure(anchor acpTaskStreamAnchor, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.resolving, anchor.callID)
	delete(m.started, anchor.callID)
	m.mu.Unlock()
	m.reportUnavailable(anchor, err)
	m.signalBoundary(anchor.callID)
}

func (m *acpTaskStreamMux) reportUnavailable(anchor acpTaskStreamAnchor, err error) {
	if m == nil || err == nil || m.ctx.Err() != nil {
		return
	}
	toolName := identity.RunCommand
	if anchor.kind == task.KindSubagent {
		toolName = identity.Spawn
	}
	notice := eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: m.sessionID,
		Scope:     eventstream.ScopeMain,
		ScopeID:   m.sessionID,
		Notice:    fmt.Sprintf("%s live output is unavailable for Task %s: %v", toolName, anchor.handle, err),
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Meta: map[string]any{"task_stream": map[string]any{
			"target_handle": anchor.handle, "unavailable": true,
		}},
	}
	select {
	case <-m.ctx.Done():
	case m.events <- notice:
	}
}

func (m *acpTaskStreamMux) signalBoundary(callID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	boundary := m.boundaries[strings.TrimSpace(callID)]
	m.mu.Unlock()
	if boundary == nil {
		return
	}
	select {
	case boundary <- struct{}{}:
	default:
	}
}

func (m *acpTaskStreamMux) parentBoundary(callID string) <-chan struct{} {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.boundaries[strings.TrimSpace(callID)]
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

// Seal prevents discovery of new terminal-backed Tasks while allowing
// already-started subscriptions to deliver until their Task stream ends.
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

func acpTaskStreamAnchorFromEnvelope(envelope eventstream.Envelope) (acpTaskStreamAnchor, bool) {
	if envelope.Kind != eventstream.KindSessionUpdate || (envelope.Scope != "" && envelope.Scope != eventstream.ScopeMain) {
		return acpTaskStreamAnchor{}, false
	}
	meta := eventstream.UpdateMeta(envelope.Update)
	toolName := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName)
	var input, output map[string]any
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
	case schema.ToolCallUpdate:
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
	default:
		return acpTaskStreamAnchor{}, false
	}
	kind := task.Kind("")
	switch identity.CanonicalOrSelf(toolName) {
	case identity.RunCommand:
		kind = task.KindCommand
	case identity.Spawn:
		kind = task.KindSubagent
	default:
		if identity.CanonicalOrSelf(display.MapString(output, "parent_tool")) == identity.Spawn &&
			strings.EqualFold(display.ToolTaskTargetKind(input, output, meta), "subagent") {
			kind = task.KindSubagent
		}
	}
	if kind == "" {
		return acpTaskStreamAnchor{}, false
	}
	handle := display.ToolTaskHandle(input, output, meta)
	callID := strings.TrimSpace(taskStreamToolCallID(envelope.Update))
	handle = strings.TrimSpace(handle)
	if kind == task.KindSubagent {
		if parentCall := strings.TrimSpace(display.MapString(output, "parent_call")); parentCall != "" && parentCall != callID {
			return acpTaskStreamAnchor{}, false
		}
	}
	anchor := acpTaskStreamAnchor{callID: callID, handle: handle, kind: kind}
	return anchor, callID != "" && handle != ""
}

func acpTaskStreamEnvelopeAllowed(anchor acpTaskStreamAnchor, envelope eventstream.Envelope) bool {
	switch anchor.kind {
	case task.KindCommand:
		return envelope.Scope == eventstream.ScopeMain && envelope.Kind == eventstream.KindSessionUpdate && envelopeHasTerminalDelivery(envelope)
	case task.KindSubagent:
		if envelope.Scope != eventstream.ScopeSubagent || envelope.ParentTool == nil ||
			strings.TrimSpace(envelope.ParentTool.ToolCallID) != anchor.callID ||
			identity.CanonicalOrSelf(envelope.ParentTool.ToolName) != identity.Spawn {
			return false
		}
		switch envelope.Kind {
		case eventstream.KindSessionUpdate:
			return envelope.Update != nil
		case eventstream.KindNotice:
			return strings.TrimSpace(envelope.Notice) != ""
		}
	}
	return false
}

func acpSubagentTaskLifecycleAllowed(anchor acpTaskStreamAnchor, envelope eventstream.Envelope) bool {
	return anchor.kind == task.KindSubagent && envelope.Scope == eventstream.ScopeSubagent &&
		envelope.ParentTool != nil && strings.TrimSpace(envelope.ParentTool.ToolCallID) == anchor.callID &&
		identity.CanonicalOrSelf(envelope.ParentTool.ToolName) == identity.Spawn &&
		envelope.Lifecycle != nil && eventstream.IsTerminalLifecycleState(envelope.Lifecycle.State)
}

func taskStreamParentToolName(kind task.Kind) string {
	switch kind {
	case task.KindSubagent:
		return identity.Spawn
	case task.KindCommand:
		return identity.RunCommand
	default:
		return ""
	}
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
