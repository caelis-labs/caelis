package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeAdmissionKey struct{}
type codeModeValidationKey struct{}
type nestedToolEventSinkKey struct{}

var errCodeModeComplete = errors.New("code mode host admission is closed by TurnComplete")

type runtimeCodeModeTool struct {
	declaration   *codemode.Tool
	tasks         *taskRuntime
	active        session.Session
	sessions      session.Service
	ref           session.SessionRef
	nativeTargets func() []tool.Tool
	lookup        func(context.Context, string) (tool.Tool, bool, error)
	checkBinding  func(context.Context, tool.Binding) error
	validate      func(context.Context) (func(), error)
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
	if t.tasks != nil && t.declaration.Name == codemode.ToolName {
		return t.tasks.startCodeMode(ctx, t, call, source, limits)
	}
	return t.execute(ctx, call, source, limits)
}

func (t runtimeCodeModeTool) execute(ctx context.Context, call tool.Call, source codemode.Source, limits codemode.Limits) (tool.Result, error) {
	events, err := t.sessions.Events(ctx, session.EventsRequest{SessionRef: t.ref, IncludeTransient: true})
	if err != nil {
		return tool.Result{}, err
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	host := &codeModeHost{ctx: workCtx, cancel: cancel, owner: t, parent: call, limits: limits,
		targets: map[string]codeModeTarget{}, handles: map[string]model.Part{}, drained: make(chan struct{})}
	discovered := mcpdiscovery.FromEvents(events)
	host.discovered = discovered
	candidates := append([]tool.Tool(nil), t.nativeTargets()...)
	// Lookup prefers the static registration. Restoring its discovered binding
	// must not append that same registration again; duplicates within the
	// assembled static set still reach the ordinary rejection below.
	staticNames := map[string]bool{}
	for _, one := range candidates {
		if one != nil {
			staticNames[one.Definition().Name] = true
		}
	}
	for name := range discovered {
		if staticNames[name] {
			continue
		}
		one, found, lookupErr := t.lookup(ctx, name)
		if lookupErr == nil && found {
			candidates = append(candidates, one)
		}
	}
	for _, one := range candidates {
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
		if !def.AvailableForModel(call.RuntimeModel) {
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
		completed = <-done
	}
	// An executor cannot retain Host and dispatch after returning.
	host.stop()
	// Foreground cancellation may stop waiting, but Runner completion must retain
	// the existing Run owner until the adapter and every Host producer have exited.
	<-host.drained
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
		result.Content, completed.err = host.returnedParts(completed.output)
	}
	if len(host.failures) > 0 {
		result.Metadata["code_mode_outcome"] = codemode.OutcomeFailed
		for _, failure := range host.failures {
			if failure["outcome"] == codemode.OutcomeUnknown {
				result.Metadata["code_mode_outcome"] = codemode.OutcomeUnknown
			}
		}
		raw, _ := json.Marshal(map[string]any{"errors": host.failures, "instruction": "Do not replay unknown effects. Reload stale schemas/bindings with tools.search(name)."})
		result.Content = append(result.Content, model.NewJSONPart(raw))
	}
	if completed.err != nil {
		return result, errors.Join(completed.err, host.fatal, host.firstCallError)
	}
	return result, host.fatal
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
	ctx            context.Context
	cancel         context.CancelFunc
	owner          runtimeCodeModeTool
	parent         tool.Call
	limits         codemode.Limits
	targets        map[string]codeModeTarget
	discovered     map[string]tool.Binding
	failures       []map[string]string
	firstCallError error
	mu             sync.Mutex
	closed         bool
	drained        chan struct{}
	drainClosed    bool
	active         int
	pending        int
	unsafeActive   bool
	calls          int
	retained       int
	isError        bool
	turnComplete   bool
	fatal          error
	handles        map[string]model.Part
}

func (h *codeModeHost) stop() {
	h.mu.Lock()
	h.closed = true
	h.cancel()
	h.closeDrainLocked()
	h.mu.Unlock()
}

func (h *codeModeHost) closeDrainLocked() {
	if h.closed && h.pending == 0 && !h.drainClosed {
		close(h.drained)
		h.drainClosed = true
	}
}

// admit protects the final source comparison and existing journal's durable
// execution claim. The caller releases before invoking external tool I/O.
// Lock order is snapshot guard, then host.mu; no approval wait holds either.
func (h *codeModeHost) admit(ctx context.Context, name string, target codeModeTarget) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope := taskScopeFromContext(h.ctx); scope != nil {
		scope.mu.Lock()
		closed := scope.codeModeClosed
		scope.mu.Unlock()
		if closed {
			return nil, errCodeModeComplete
		}
	}
	release := func() {}
	var validationErr error
	if h.owner.checkBinding != nil && tool.IsMCPDefinition(target.definition) {
		if err := h.owner.checkBinding(ctx, tool.BindingForDefinition(target.definition)); err != nil {
			return nil, err
		}
	}
	if h.owner.validate != nil {
		guard, err := h.owner.validate(ctx)
		validationErr = err
		if guard != nil {
			release = guard
		}
	}
	if validationErr == nil {
		validationErr = tool.NewError(tool.ErrorCodeStaleRevision, "Code Mode source/schema changed; reload with tools.search(name) before calling")
		one, found, lookupErr := h.owner.lookup(ctx, name)
		if lookupErr != nil {
			validationErr = lookupErr
		} else if found && one != nil {
			def := one.Definition()
			if def.Name == name && codeModeDefinitionDigest(def) == target.digest {
				validationErr = nil
			}
		}
	}
	h.mu.Lock()
	switch {
	case h.turnComplete:
		validationErr = errCodeModeComplete
	case h.closed:
		validationErr = fmt.Errorf("code mode host admission is closed")
	case h.ctx.Err() != nil:
		validationErr = h.ctx.Err()
	case ctx.Err() != nil:
		validationErr = ctx.Err()
	}
	if validationErr != nil {
		h.mu.Unlock()
		release()
		return nil, validationErr
	}
	return func() { h.mu.Unlock(); release() }, nil
}

func (h *codeModeHost) Call(ctx context.Context, name string, args json.RawMessage) (codemode.Reply, error) {
	return h.call(ctx, name, args, false)
}

func (h *codeModeHost) call(requestCtx context.Context, name string, arguments json.RawMessage, discovery bool) (returned codemode.Reply, callError error) {
	defer func() {
		if callError != nil || returned.IsError || returned.Outcome == codemode.OutcomeUnknown {
			h.mu.Lock()
			h.isError = true
			if callError != nil && h.firstCallError == nil {
				h.firstCallError = callError
			}
			outcome := returned.Outcome
			if outcome == "" {
				outcome = codemode.OutcomeFailed
			}
			h.failures = append(h.failures, map[string]string{"tool": name, "outcome": outcome, "code": string(errorcode.CodeOf(callError))})
			h.mu.Unlock()
		}
	}()

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
	h.pending++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.pending--
		h.closeDrainLocked()
		h.mu.Unlock()
	}()
	if len(arguments) > h.limits.ArgumentBytes {
		return codemode.Reply{}, fmt.Errorf("code mode arguments exceed budget")
	}
	if raw := bytes.TrimSpace(arguments); len(raw) == 0 || raw[0] != '{' {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeInvalidInput, "host arguments must be a JSON object")
	}
	h.mu.Lock()
	target, ok := h.targets[name]
	oldBinding, known := h.discovered[name]
	h.mu.Unlock()
	if discovery {
		one, found, lookupErr := h.owner.lookup(requestCtx, tool.ToolSearchToolName)
		if lookupErr != nil {
			return codemode.Reply{}, lookupErr
		}
		if found && one != nil {
			def := one.Definition()
			if tool.IsToolSearchDefinition(def) {
				target = codeModeTarget{tool: one, definition: def, digest: codeModeDefinitionDigest(def)}
				ok = true
			}
		}
	}
	if !ok {
		if known {
			if h.owner.checkBinding != nil {
				if err := h.owner.checkBinding(requestCtx, oldBinding); err != nil {
					return codemode.Reply{}, err
				}
			}
			one, found, lookupErr := h.owner.lookup(requestCtx, name)
			if lookupErr != nil {
				return codemode.Reply{}, lookupErr
			}
			if found && one != nil {
				def := one.Definition()
				if def.Name == name && oldBinding != tool.BindingForDefinition(def) {
					return codemode.Reply{}, tool.NewError(tool.ErrorCodeStaleRevision, "tool directory refreshed; use tools.search(name) to reload its schema/binding before calling")
				}
			}

			return codemode.Reply{}, tool.NewError(errorcode.Unavailable, "discovered tool is currently unavailable; check connection or access before rediscovery")
		}
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeNotFound, "tool has not been discovered; use tools.search(name) first")
	}
	if !codeModeCanCall(target.definition) {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodePermissionDenied, "native shell/process execution is prohibited from JavaScript")
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
	if discovery {
		ctx = context.WithValue(ctx, codeModeSearchKey{}, requestCtx.Value(codeModeSearchKey{}))
	}
	defer cancel()
	stop := context.AfterFunc(requestCtx, cancel)
	defer stop()
	admission := func(ctx context.Context) (func(), error) { return h.admit(ctx, name, target) }
	// This eligibility check is advisory; the journal repeats it after approval
	// and retains the guard through its durable started claim.
	release, err := admission(ctx)
	if err != nil {
		return codemode.Reply{}, err
	}
	release()
	if err := tool.ValidateMCPArguments(target.definition, arguments); err != nil {
		return codemode.Reply{}, err
	}
	call := tool.Call{ID: callID, Name: name, Input: append(json.RawMessage(nil), arguments...),
		Parent:       &tool.ParentCall{Kind: codemode.ToolKind, ID: h.parent.ID, Name: h.parent.Name, Execution: h.parent.Execution},
		RuntimeModel: h.parent.RuntimeModel, RuntimeReasoning: h.parent.RuntimeReasoning, RuntimeServiceTier: h.parent.RuntimeServiceTier}
	ctx = context.WithValue(ctx, codeModeAdmissionKey{}, &codeModeAdmissionBinding{callID: call.ID, name: call.Name, parent: *call.Parent, validate: admission})
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
		if scope := taskScopeFromContext(h.ctx); scope != nil {
			scope.completeCodeMode(h.ctx)
		}
	}
	if callErr != nil {
		if errors.Is(callErr, errCodeModeComplete) {
			// A pending approval lost admission before claiming an effect. This
			// expected rejection must not cancel the completing call's media Emit.
			return codemode.Reply{CallID: callID, IsError: true, Outcome: outcome}, callErr
		}
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
