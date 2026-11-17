package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeAdmissionKey struct{}
type codeModeValidationKey struct{}
type nestedToolEventSinkKey struct{}

var errCodeModeComplete = errors.New("code mode host admission is closed by TurnComplete")

type runtimeCodeModeTool struct {
	runtime        *Runtime
	execution      *codeModeExecution
	declaration    *codemode.Tool
	tasks          *taskRuntime
	active         session.Session
	sessions       session.Service
	ref            session.SessionRef
	availableTools func() []tool.Tool
	lookup         func(context.Context, string) (tool.Tool, bool, error)
	checkBinding   func(context.Context, tool.Binding) error
	validate       func(context.Context) (func(), error)
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
	var input struct {
		Reset *bool           `json:"reset"`
		Code  json.RawMessage `json:"code"`
		File  json.RawMessage `json:"file"`
	}
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return tool.Result{}, err
	}
	if input.Reset != nil {
		if !*input.Reset || len(input.Code) != 0 || len(input.File) != 0 {
			return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "reset:true is mutually exclusive with code and file")
		}
		if t.runtime == nil {
			return tool.Result{}, fmt.Errorf("js_exec reset requires Runtime Session binding")
		}
		return t.runtime.resetCodeMode(t.ref, t.declaration.Executor), nil
	}
	source, err := t.declaration.ReadSource(call, limits.SourceBytes)
	if err != nil {
		return tool.Result{}, tool.WrapError(tool.ErrorCodeInvalidInput, err, "js_exec source: "+err.Error())
	}
	if t.runtime != nil {
		ctx, t.execution, err = t.runtime.acquireCodeMode(ctx, t.ref, t.declaration.Executor)
		if err != nil {
			return tool.Result{}, err
		}
	}
	if t.tasks != nil && t.declaration.Name == codemode.ToolName {
		return t.tasks.startCodeMode(ctx, t, call, source, limits)
	}
	defer t.execution.release()
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
		targets: map[string]codeModeTarget{}, codeModeResults: newCodeModeResults(), drained: make(chan struct{})}
	executor := t.declaration.Executor
	if t.execution != nil {
		host.codeModeResults = t.execution.environment.results
		executor = t.execution.environment.engine
	}
	if err := host.prepareTargets(ctx, events); err != nil {
		return tool.Result{}, err
	}
	type execution struct {
		output codemode.Output
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		output, err := executor.Execute(workCtx, source, limits, host)
		done <- execution{output: output, err: err}
	}()
	var completed execution
	select {
	case completed = <-done:
	case <-ctx.Done():
		host.stop()
		completed = <-done
	}
	if t.execution != nil && (ctx.Err() != nil || (completed.err != nil && errorcode.CodeOf(completed.err) != codemode.ErrorCodeJavaScript)) {
		t.execution.retire = true
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
		if !validCodeModeOutput(completed.output, limits.OutputBytes) {
			completed.output = codemode.Output{Value: []byte(`null`)}
		}
	}
	if completed.err == nil && !validCodeModeOutput(completed.output, limits.OutputBytes) {
		completed.err = errors.Join(completed.err, fmt.Errorf("code mode output must be bounded JSON"))
	}
	result := tool.Result{ID: call.ID, Name: call.Name, IsError: host.isError && !host.turnComplete, TurnComplete: host.turnComplete,
		Metadata: map[string]any{"code_mode_source": map[string]any{"path": source.Path, "sha256": source.SHA256, "code": source.Code}}}
	if completed.err == nil {
		result.Content, completed.err = host.returnedParts(completed.output)
	}
	if len(host.failures) > 0 {
		result.Metadata["code_mode_outcome"] = codemode.OutcomeFailed
		for _, failure := range host.failures {
			if failure.Outcome == codemode.OutcomeUnknown {
				result.Metadata["code_mode_outcome"] = codemode.OutcomeUnknown
			}
		}
		result.Content = append(result.Content, host.failureParts(completed.err != nil))
	}
	result.Metadata[codemode.MetadataScriptCompletion] = "completed"
	if completed.err != nil || host.fatal != nil {
		result.Metadata[codemode.MetadataScriptCompletion] = "failed"
	}

	if completed.err != nil || host.fatal != nil {
		// Normalize once before foreground delivery or Task persistence. Keep
		// private Host causes out of the public diagnostic and error string.
		publicErr := completed.err
		if result.Metadata["code_mode_outcome"] == codemode.OutcomeUnknown {
			publicErr = codemode.PublicHostError(call.Name, tool.NewError(errorcode.UnknownOutcome, "tool outcome is unknown"))
		} else if host.fatal != nil {
			publicErr = codemode.PublicHostError(call.Name, host.fatal)
		}
		payload := tool.ErrorPayload(publicErr)
		code, _ := payload["error_code"].(string)
		raw, _ := json.Marshal(payload)
		result.Content = append(result.Content, model.NewJSONPart(raw))
		result.IsError = true
		// Outcome classification follows the normalized disposition, including
		// an unknown effect among concurrently failed calls.
		return result, &codemode.ExecutionError{Message: publicErr.Error(), Code: tool.ErrorCode(code), Err: errors.Join(host.fatal, completed.err, host.firstCallError)}
	}
	return result, nil
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
	bindings       map[string]tool.Binding
	failures       []codeModeFailure
	firstCallError error
	mu             sync.Mutex
	closed         bool
	drained        chan struct{}
	drainClosed    bool
	pending        int
	calls          int
	isError        bool
	turnComplete   bool
	fatal          error
	*codeModeResults
}

func (h *codeModeHost) ExecutionState() codemode.ExecutionState {
	if h.owner.execution != nil {
		return h.owner.execution.state
	}
	return codemode.ExecutionState{}
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
		validationErr = tool.NewError(tool.ErrorCodeStaleRevision, "Code Mode source/schema changed")
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
		callError = mcpdiscovery.WithEntryHint(callError, "tools.search(name)")
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
			failure := codeModeFailure{Tool: name, CallID: returned.CallID, Outcome: outcome}
			if code := errorcode.CodeOf(callError); code != errorcode.Unknown {
				failure.Code = string(code)
			}
			if returned.CallID != "" {
				reply := returned
				failure.Reply = &reply
			}
			h.failures = append(h.failures, failure)
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
	if raw := bytes.TrimSpace(arguments); len(raw) == 0 || raw[0] != '{' {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeInvalidInput, "host arguments must be a JSON object")
	}
	target, err := h.resolveTarget(requestCtx, name, discovery)
	if err != nil {
		return codemode.Reply{}, err
	}

	h.mu.Lock()
	if h.closed || h.ctx.Err() != nil {
		h.mu.Unlock()
		return codemode.Reply{}, fmt.Errorf("code mode host admission is closed")
	}
	callID := fmt.Sprintf("%s/%s/host-%d", h.parent.Execution.TurnID, h.parent.Execution.ItemID, ordinal)
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(h.ctx)
	if discovery {
		ctx = context.WithValue(ctx, codeModeSearchKey{}, requestCtx.Value(codeModeSearchKey{}))
	}
	defer cancel()
	stop := context.AfterFunc(requestCtx, cancel)
	defer stop()
	admission := func(ctx context.Context) (func(), error) { return h.admit(ctx, name, target) }
	// This eligibility check is advisory; final dispatch repeats it after approval
	// and retains the guard through its final dispatch admission.
	release, err := admission(ctx)
	if err != nil {
		return codemode.Reply{}, err
	}
	release()
	call := tool.Call{ID: callID, Name: name, Input: append(json.RawMessage(nil), arguments...),
		Parent:       &tool.ParentCall{Kind: codemode.ToolKind, ID: h.parent.ID, Name: h.parent.Name, Execution: h.parent.Execution},
		RuntimeModel: h.parent.RuntimeModel, RuntimeReasoning: h.parent.RuntimeReasoning, RuntimeServiceTier: h.parent.RuntimeServiceTier}
	ctx = context.WithValue(ctx, codeModeAdmissionKey{}, &codeModeAdmissionBinding{callID: call.ID, name: call.Name, parent: *call.Parent, validate: admission})
	if tool.IsMCPDefinition(target.definition) {
		binding := tool.BindingForDefinition(target.definition)
		call.TargetBinding = &binding
	}
	call, synthetic, prepareErr := toolbinding.PrepareEffective(ctx, target.tool, call)
	if prepareErr != nil {
		return codemode.Reply{}, prepareErr
	}
	ctx = toolbinding.WithPrepared(ctx, call, synthetic)
	if err := h.appendToolFact(ctx, call, nil, nil, target.definition); err != nil {
		h.fail(err)
		return codemode.Reply{}, err
	}
	result, callErr := target.tool.Call(ctx, call)
	outcome := codeModeOutcome(result, callErr)
	persistErr := h.appendToolFact(ctx, call, &result, callErr, target.definition)
	if persistErr != nil {
		h.fail(persistErr)
		callErr = errors.Join(callErr, persistErr)
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
	if outcome == codemode.OutcomeUnknown && callErr == nil {
		callErr = errorcode.New(errorcode.UnknownOutcome, "tool outcome is unknown; do not replay")
	}
	if callErr != nil {
		if errors.Is(callErr, errCodeModeComplete) {
			// A pending approval lost admission before claiming an effect. This
			// expected rejection must not cancel the completing call's media Emit.
			return codemode.Reply{CallID: callID, IsError: true, Outcome: outcome}, callErr
		}
		// A published failed receipt settles an ordinary tool failure. The
		// producer remains admitted; the failure flag/receipt is never erased.
		if h.ctx.Err() == nil && persistErr == nil && settledCodeModeToolError(callErr) {
			reply, projectErr := h.project(callID, result, outcome, target.definition)
			if projectErr == nil {
				reply.IsError = true
				return reply, callErr
			}
			callErr = errors.Join(callErr, projectErr)
		}
		// Persistence, transport and authority failures still stop the producer.
		h.fatal = errors.Join(h.fatal, callErr)
		h.closed = true
		h.cancel()
		return codemode.Reply{CallID: callID, IsError: true, Outcome: outcome}, callErr
	}
	return h.project(callID, result, outcome, target.definition)
}

func codeModeOutcome(result tool.Result, err error) string {
	result, err = hookrun.EffectResult(result, err)
	if errorcode.Is(err, errorcode.UnknownOutcome) || errorcode.Is(err, errorcode.Cancelled) || errors.Is(err, context.Canceled) {
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

func (h *codeModeHost) appendToolFact(ctx context.Context, call tool.Call, result *tool.Result, callErr error, definition tool.Definition) error {
	return h.owner.runtime.appendToolFact(ctx, h.owner.ref, h.parent.Execution.TurnID, call, result, callErr, definition)
}

// Only the ToolResult owner publishes this result after durable persistence
// delivery. An unclassified/transport/authority error never grants continuation.
func settledCodeModeToolError(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !settledCodeModeToolError(child) {
				return false
			}
		}
		return true
	}
	switch errorcode.CodeOf(err) {
	case tool.ErrorCodeInvalidInput, errorcode.InvalidArgument, tool.ErrorCodeNotFound, tool.ErrorCodeOldTextNotFound, tool.ErrorCodeTooManyMatches, tool.ErrorCodeUnexpectedMatchCount:
		return true
	case errorcode.Unknown:
		return errors.Is(err, fs.ErrNotExist)
	}
	return false
}
