package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeAdmissionKey struct{}
type codeModeValidationKey struct{}
type nestedToolEventSinkKey struct{}

const codeModeDrainGrace = 50 * time.Millisecond

type runtimeCodeModeTool struct {
	declaration *codemode.Tool
	sessions    session.Service
	ref         session.SessionRef
	targets     func() []tool.Tool
	validate    func(context.Context) (func(), error)
}

func (t runtimeCodeModeTool) Definition() tool.Definition { return t.declaration.Definition() }

func (t runtimeCodeModeTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	limits, err := t.declaration.EffectiveLimits()
	if err != nil {
		return tool.Result{}, err
	}
	if t.declaration.Executor == nil {
		return tool.Result{}, fmt.Errorf("code mode executor is unavailable")
	}
	source, err := t.declaration.ReadSource(call, limits.SourceBytes)
	if err != nil {
		return tool.Result{}, err
	}
	events, err := t.sessions.Events(ctx, session.EventsRequest{SessionRef: t.ref})
	if err != nil {
		return tool.Result{}, err
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	host := &codeModeHost{ctx: workCtx, cancel: cancel, owner: t, parent: call, limits: limits,
		targets: map[string]codeModeTarget{}, handles: map[string]model.Part{}, drained: make(chan struct{})}
	discovered := mcpdiscovery.FromEvents(events)
	for _, one := range t.targets() {
		if one == nil {
			continue
		}
		def := one.Definition()
		if def.Name == call.Name || tool.IsMCPCallDefinition(def) || tool.IsToolSearchDefinition(def) {
			continue
		}
		// Code Mode declarations are never recursively callable through Host.
		if def.Metadata[tool.MetadataToolKind] == codemode.ToolKind {
			continue
		}
		if tool.IsMCPDefinition(def) && discovered[def.Name] != tool.BindingForDefinition(def) {
			continue
		}
		if tool.IsMCPDefinition(def) && tool.BindingForDefinition(def).Identity == "" {
			continue
		}
		if !tool.AvailableForModel(one, call.RuntimeModel) {
			continue
		}
		if _, duplicate := host.targets[def.Name]; duplicate {
			return tool.Result{}, fmt.Errorf("duplicate Code Mode target %q", def.Name)
		}
		host.targets[def.Name] = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
	}
	type execution struct {
		output json.RawMessage
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		output, err := t.declaration.Executor.Execute(workCtx, source, limits, host)
		done <- execution{output: output, err: err}
	}()
	var completed execution
	select {
	case completed = <-done:
	case <-ctx.Done():
		host.stop()
		timer := time.NewTimer(codeModeDrainGrace)
		defer timer.Stop()
		select {
		case completed = <-done:
		case <-timer.C:
			return tool.Result{}, ctx.Err()
		}
	}
	// An executor cannot retain Host and dispatch after returning.
	host.stop()
	drain := time.NewTimer(codeModeDrainGrace)
	defer drain.Stop()
	select {
	case <-host.drained:
	case <-drain.C:
		return tool.Result{}, fmt.Errorf("code mode executor returned with pending host calls; original receipts retained")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.turnComplete && host.fatal == nil {
		// A trusted host disposition cannot be suppressed by executor control
		// flow or an invalid final value. No further effect or model call follows.
		completed.err = nil
		if len(completed.output) > limits.OutputBytes || !json.Valid(completed.output) {
			completed.output = []byte(`null`)
		}
	}
	if completed.err == nil && (len(completed.output) > limits.OutputBytes || !json.Valid(completed.output)) {
		completed.err = errors.Join(completed.err, fmt.Errorf("code mode output must be bounded JSON"))
	}
	result := tool.Result{ID: call.ID, Name: call.Name, IsError: host.isError && !host.turnComplete, TurnComplete: host.turnComplete,
		Metadata: map[string]any{"code_mode_source": map[string]any{"path": source.Path, "sha256": source.SHA256}}}
	if completed.err == nil {
		result.Content = append(result.Content, model.NewJSONPart(completed.output))
	}
	result.Content = append(result.Content, host.emitted...)
	return result, errors.Join(completed.err, host.fatal)
}

type codeModeTarget struct {
	tool       tool.Tool
	definition tool.Definition
	digest     [32]byte
}

func codeModeDefinitionDigest(def tool.Definition) [32]byte {
	raw, _ := json.Marshal(def)
	return sha256.Sum256(raw)
}

type codeModeHost struct {
	ctx          context.Context
	cancel       context.CancelFunc
	owner        runtimeCodeModeTool
	parent       tool.Call
	limits       codemode.Limits
	targets      map[string]codeModeTarget
	mu           sync.Mutex
	closed       bool
	drained      chan struct{}
	drainClosed  bool
	active       int
	unsafeActive bool
	calls        int
	retained     int
	isError      bool
	turnComplete bool
	fatal        error
	handles      map[string]model.Part
	emitted      []model.Part
}

func (h *codeModeHost) stop() {
	h.mu.Lock()
	h.closed = true
	h.cancel()
	h.closeDrainLocked()
	h.mu.Unlock()
}

func (h *codeModeHost) closeDrainLocked() {
	if h.closed && h.active == 0 && !h.drainClosed {
		close(h.drained)
		h.drainClosed = true
	}
}

func (h *codeModeHost) current(ctx context.Context, name string, target codeModeTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.owner.validate != nil {
		release, err := h.owner.validate(ctx)
		if err != nil {
			return err
		}
		if release != nil {
			release()
		}
	}
	for _, one := range h.owner.targets() {
		if one != nil && one.Definition().Name == name && codeModeDefinitionDigest(one.Definition()) == target.digest {
			return nil
		}
	}
	return tool.NewError(tool.ErrorCodeStaleRevision, "Code Mode source/schema changed; discover and execute again")
}

func (h *codeModeHost) Call(requestCtx context.Context, name string, arguments json.RawMessage) (codemode.Reply, error) {
	if err := requestCtx.Err(); err != nil {
		return codemode.Reply{}, err
	}
	h.mu.Lock()
	if h.closed || h.ctx.Err() != nil {
		h.mu.Unlock()
		return codemode.Reply{}, fmt.Errorf("code mode host admission is closed")
	}
	if h.calls >= h.limits.Calls {
		h.mu.Unlock()
		return codemode.Reply{}, fmt.Errorf("code mode call budget exceeded")
	}
	// Count requests, including rejected names and malformed arguments, before
	// schema compilation or other potentially expensive host work.
	h.calls++
	ordinal := h.calls
	h.mu.Unlock()
	if len(arguments) > h.limits.ArgumentBytes {
		return codemode.Reply{}, fmt.Errorf("code mode arguments exceed budget")
	}
	if raw := bytes.TrimSpace(arguments); len(raw) == 0 || raw[0] != '{' {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeInvalidInput, "host arguments must be a JSON object")
	}
	target, ok := h.targets[name]
	if !ok {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeNotFound, "target is not admitted; MCP tools require prior canonical tool_search")
	}
	if err := tool.ValidateMCPArguments(target.definition, arguments); err != nil {
		return codemode.Reply{}, err
	}
	h.mu.Lock()
	if h.closed || h.ctx.Err() != nil {
		h.mu.Unlock()
		return codemode.Reply{}, fmt.Errorf("code mode host admission is closed")
	}
	safe := target.definition.Capabilities.ParallelSafe
	if h.unsafeActive || (h.active > 0 && !safe) {
		h.mu.Unlock()
		return codemode.Reply{}, fmt.Errorf("concurrent host calls require explicit ParallelSafe targets")
	}
	callID := fmt.Sprintf("%s/%s/host-%d", h.parent.Execution.TurnID, h.parent.Execution.ItemID, ordinal)
	h.active++
	h.unsafeActive = !safe
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.active--
		if !safe {
			h.unsafeActive = false
		}
		h.closeDrainLocked()
		h.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(requestCtx, cancel)
	defer stop()
	ctx = context.WithValue(ctx, codeModeAdmissionKey{}, func(ctx context.Context) error { return h.current(ctx, name, target) })
	if err := h.current(ctx, name, target); err != nil {
		return codemode.Reply{}, err
	}
	call := tool.Call{ID: callID, Name: name, Input: append(json.RawMessage(nil), arguments...),
		Parent:       &tool.ParentCall{ID: h.parent.ID, Name: h.parent.Name, Execution: h.parent.Execution},
		RuntimeModel: h.parent.RuntimeModel, RuntimeReasoning: h.parent.RuntimeReasoning, RuntimeServiceTier: h.parent.RuntimeServiceTier}
	if tool.IsMCPDefinition(target.definition) {
		binding := tool.BindingForDefinition(target.definition)
		call.TargetBinding = &binding
	}
	if err := h.display(ctx, call, "started"); err != nil {
		h.fail(err)
		return codemode.Reply{}, err
	}
	result, callErr := target.tool.Call(ctx, call)
	outcome := codeModeOutcome(result, callErr)
	status := "completed"
	if callErr != nil || result.IsError {
		status = "failed"
	}
	if err := h.display(ctx, call, status); err != nil {
		h.fail(err)
		callErr = errors.Join(callErr, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.isError = h.isError || result.IsError || callErr != nil
	if result.TurnComplete && !result.IsError {
		h.turnComplete = true
		h.closed = true
	}
	if callErr != nil {
		// A persistence failure or transport error must not be swallowed by an executor.
		h.fatal = errors.Join(h.fatal, callErr)
		h.closed = true
		h.cancel()
		return codemode.Reply{CallID: callID, IsError: true, Outcome: outcome}, callErr
	}
	return h.project(callID, result, outcome)
}

func codeModeOutcome(result tool.Result, err error) string {
	journal, _ := result.Metadata[tool.MetadataExecutionJournal].(map[string]any)
	record, _ := journal["tool_execution"].(map[string]any)
	if record["status"] == string(session.ToolExecutionUnknownOutcome) {
		return codemode.OutcomeUnknown
	}
	if err != nil || result.IsError {
		return codemode.OutcomeFailed
	}
	return codemode.OutcomeSucceeded
}

func (h *codeModeHost) fail(err error) {
	h.mu.Lock()
	h.fatal = errors.Join(h.fatal, err)
	h.closed = true
	h.cancel()
	h.closeDrainLocked()
	h.mu.Unlock()
}

func (h *codeModeHost) display(ctx context.Context, call tool.Call, status string) error {
	var input map[string]any
	decoder := json.NewDecoder(bytes.NewReader(call.Input))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	kind := session.EventTypeToolResult
	if status == "started" {
		kind = session.EventTypeToolCall
	}
	event := &session.Event{Type: kind, Visibility: session.VisibilityMirror,
		IdempotencyKey: "code-mode:" + call.ID + ":" + status,
		Scope:          &session.EventScope{TurnID: h.parent.Execution.TurnID},
		Actor:          session.ActorRef{Kind: session.ActorKindTool, ID: call.ID, Name: call.Name},
		Tool: &session.EventTool{ID: call.ID, Name: call.Name, Status: status, Input: input,
			Parent: &session.EventParentTool{CallID: h.parent.ID, Name: h.parent.Name}}}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
	defer cancel()
	persisted, err := h.owner.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: h.owner.ref, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: event})
	if err == nil {
		if sink, ok := h.ctx.Value(nestedToolEventSinkKey{}).(func(*session.Event)); ok {
			sink(persisted)
		}
	}
	return err
}
