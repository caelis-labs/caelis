package runtime

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

type codeModeAdmissionKey struct{}
type nestedToolEventSinkKey struct{}

var errCodeModeHostClosed = errors.New("code mode host admission is closed")

type runtimeCodeModeTool struct {
	producingModel model.LLM
	runtime        *Runtime
	execution      *codeModeExecution
	declaration    *codemode.Tool
	tasks          *taskRuntime
	active         session.Session
	sessions       session.Reader
	ref            session.SessionRef
	availableTools func() []tool.Tool
	lookup         func(context.Context, string) (tool.Tool, bool, error)
	checkBinding   func(context.Context, tool.Binding) error
	validate       func(context.Context) (func(), error)
}

func (t runtimeCodeModeTool) Definition() tool.Definition { return t.declaration.Definition() }

func (runtimeCodeModeTool) RuntimeNativeSchedule(toolbinding.Token, context.Context, tool.Call) toolbinding.NativeSchedule {
	return toolbinding.NativeSchedule{Ordered: true}
}

func (t runtimeCodeModeTool) RuntimeCall(_ toolbinding.Token, ctx context.Context, call tool.Call, dispatch toolbinding.Dispatch) (tool.Result, error) {
	t.producingModel = dispatch.Model
	return t.Call(ctx, call)
}

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
		toolbinding.Observed(ctx).ScriptCompletion = "completed"
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
	host := &codeModeHost{ctx: workCtx, receiptCtx: ctx, cancel: cancel, owner: t, parent: call, limits: limits,
		targets: map[string]codeModeTarget{}, codeModeResults: newCodeModeResults(), drained: make(chan struct{})}
	executor := t.declaration.Executor
	if t.execution != nil {
		host.origins = t.execution.environment.origins
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
	if completed.err == nil && !validCodeModeOutput(completed.output, limits.OutputBytes) {
		completed.err = errors.Join(completed.err, fmt.Errorf("code mode output must be bounded JSON"))
	}
	var result tool.Result
	observation := toolbinding.Observed(ctx)
	observation.Source = &source

	if completed.err == nil {
		result.Content, completed.err = host.returnedParts(completed.output)
	}
	var payload map[string]any
	var unknown bool
	if len(host.failures) > 0 {
		for _, failure := range host.failures {
			if failure.Outcome == codemode.OutcomeUnknown {
				unknown = true
				break
			}
		}
		payload = host.failurePayload(completed.err != nil)
	}
	observation.ScriptCompletion = "completed"
	if completed.err != nil || host.fatal != nil {
		observation.ScriptCompletion = "failed"
		// Normalize once before foreground delivery or Task persistence. Keep
		// private Host causes out of the public diagnostic and error string.
		publicErr := completed.err
		if unknown {
			publicErr = codemode.PublicHostError(call.Name, tool.NewError(errorcode.UnknownOutcome, "tool outcome is unknown"))
		} else if host.fatal != nil {
			publicErr = codemode.PublicHostError(call.Name, host.fatal)
		}
		diagnostic := tool.ErrorPayload(publicErr)
		code, _ := diagnostic["error_code"].(string)
		for key, value := range payload {
			diagnostic[key] = value
		}
		appendCodeModePayload(&result, diagnostic)
		// Outcome classification follows the normalized disposition, including
		// an unknown effect among concurrently failed calls.
		return result, &codemode.ExecutionError{Message: publicErr.Error(), Code: tool.ErrorCode(code), Err: errors.Join(host.fatal, completed.err, host.firstCallError)}
	}
	if payload != nil {
		appendCodeModePayload(&result, payload)
	}
	if host.isError {
		if unknown {
			const message = "tool outcome is unknown; do not replay"
			return result, tool.WrapError(errorcode.UnknownOutcome, tool.FailedReply(message), message)
		}
		return result, tool.FailedReply("one or more script tool calls failed")
	}
	return result, nil
}

// Keep a script’s standalone JSON unchanged; diagnostics add an envelope only
// when there is additional classification to report.
func appendCodeModePayload(result *tool.Result, payload map[string]any) {
	for i, block := range result.Content {
		if structured, ok := block.(tool.StructuredContent); ok {
			payload["result"] = structured.Value
			raw, _ := json.Marshal(payload)
			result.Content[i] = tool.StructuredContent{Value: raw}
			return
		}
	}
	raw, _ := json.Marshal(payload)
	result.Content = append(result.Content, tool.StructuredContent{Value: raw})
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
	origins        cipher.AEAD
	failureBytes   int
	receiptCtx     context.Context
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

	fatal error
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

// admit checks whether this script can dispatch or hand off a child call.
// Lock order is snapshot guard, then host.mu; no approval wait holds either.
func (h *codeModeHost) admit(ctx context.Context, validateTarget func(context.Context) (func(), error)) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release, validationErr := validateTarget(ctx)
	h.mu.Lock()
	switch {
	case h.closed:
		validationErr = errCodeModeHostClosed
	case h.ctx.Err() != nil:
		validationErr = h.ctx.Err()
	case ctx.Err() != nil:
		validationErr = ctx.Err()
	}
	if validationErr != nil {
		h.mu.Unlock()
		if release != nil {
			release()
		}
		return nil, validationErr
	}
	return func() { h.mu.Unlock(); release() }, nil
}

func (h *codeModeHost) Call(ctx context.Context, name string, args json.RawMessage) (codemode.Reply, error) {
	return h.call(ctx, name, args, false)
}

func (h *codeModeHost) call(requestCtx context.Context, name string, arguments json.RawMessage, discovery bool) (returned codemode.Reply, callError error) {
	defer func() {
		callError = withMCPDiscoveryEntryHint(callError, "tools.search({name})")
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
				h.retainFailureReply(&failure, returned)
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
		return codemode.Reply{}, tool.NewError(errorcode.ResourceExhausted, "code mode call budget exceeded")
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
	parentExecution := tool.InvocationFromContext(h.ctx)
	callID := fmt.Sprintf("%s/%s/host-%d", parentExecution.TurnID, parentExecution.ItemID, ordinal)
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(requestCtx, cancel)
	defer stop()
	validateTarget := h.owner.targetAdmission(name, target)
	admission := func(ctx context.Context) (func(), error) { return h.admit(ctx, validateTarget) }
	// This eligibility check is advisory; final dispatch repeats it after approval
	// and retains the guard through its final dispatch admission.
	release, err := admission(ctx)
	if err != nil {
		return codemode.Reply{}, err
	}
	release()
	call := tool.Call{ID: callID, Name: name, Input: append(json.RawMessage(nil), arguments...)}
	parent := tool.ParentCall{Kind: codemode.ToolKind, ID: h.parent.ID, Name: h.parent.Name, ItemID: parentExecution.ItemID}
	ctx = context.WithValue(ctx, codeModeAdmissionKey{}, &codeModeAdmissionBinding{
		callID: call.ID, name: call.Name, parent: parent, validate: admission, validateTarget: validateTarget,
	})
	var targetBinding *tool.Binding
	if mcp.IsDefinition(target.definition) {
		binding := mcp.BindingForDefinition(target.definition)
		targetBinding = &binding
	}
	ctx = toolbinding.WithOccurrence(ctx, toolbinding.Allocate(target.tool, call), &parent, targetBinding)
	call, synthetic, prepareErr := toolbinding.PrepareArguments(ctx, target.tool, call)
	if prepareErr != nil && synthetic == nil {
		return codemode.Reply{}, prepareErr
	}
	ctx = toolbinding.WithPrepared(ctx, call, synthetic, prepareErr)
	if _, err := h.appendToolFact(ctx, call, nil, nil, target); err != nil {
		h.fail(err)
		return codemode.Reply{}, err
	}
	ctx, _ = toolbinding.Observe(ctx)
	var report func(toolbinding.Progress)
	if publish, ok := ctx.Value(nestedToolEventSinkKey{}).(func(*session.Event)); ok && ownsNestedCommandPresentation(ctx, call, target.tool) {
		report = (nestedCommandObserver{publish: publish, fact: runtimeToolFact(ctx, call, "", nil), target: target.tool}).ObserveToolResult
	}
	callCtx, stopProgress := context.WithCancel(toolbinding.WithProgress(ctx, report))
	defer stopProgress()
	result, callErr := toolbinding.Call(callCtx, target.tool, call, toolbinding.Dispatch{Model: h.owner.producingModel})
	stopProgress()
	outcome := codeModeOutcome(callErr)
	settle, stopSettlement := toolbinding.ResultSettlementContext(ctx)
	referenced, persistErr := toolbinding.StageResultAttachments(settle, result)
	stopSettlement()
	var receipt *session.Event
	if persistErr == nil {
		receipt, persistErr = h.appendToolFact(ctx, call, &referenced, callErr, target)
	}
	if persistErr != nil {
		h.fail(persistErr)
		callErr = errors.Join(callErr, persistErr)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.isError = h.isError || callErr != nil
	if outcome == codemode.OutcomeUnknown && callErr == nil {
		callErr = errorcode.New(errorcode.UnknownOutcome, "tool outcome is unknown; do not replay")
	}

	if callErr != nil {
		// A published failed receipt settles an ordinary tool failure. The
		// producer remains admitted; the failure flag/receipt is never erased.
		if h.ctx.Err() == nil && persistErr == nil && (settledCodeModeToolError(callErr) || discovery && errorcode.Is(callErr, errorcode.ResourceExhausted)) {
			reply, projectErr := h.project(callID, referenced, outcome, target.definition, receipt)
			if projectErr == nil {
				reply.IsError = true
				if tool.HasFailedReply(callErr) && errorcode.CodeOf(callErr) == errorcode.Unknown {
					return reply, nil
				}
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
	return h.project(callID, referenced, outcome, target.definition, receipt)
}

func codeModeOutcome(err error) string {
	err = hookrun.EffectError(err)
	if errorcode.Is(err, errorcode.UnknownOutcome) || errorcode.Is(err, errorcode.Cancelled) || errors.Is(err, context.Canceled) {
		return codemode.OutcomeUnknown
	}
	if err != nil {
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

func (h *codeModeHost) appendToolFact(ctx context.Context, call tool.Call, result *tool.Result, callErr error, target codeModeTarget) (*session.Event, error) {
	return h.owner.runtime.appendToolFact(ctx, h.owner.ref, tool.InvocationFromContext(h.ctx).TurnID, call, result, callErr, toolFactTarget{definition: target.definition, source: target.tool})
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
	if tool.HasFailedReply(err) {
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
