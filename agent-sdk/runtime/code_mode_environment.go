package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/google/uuid"
)

type codeModeResults struct {
	mu       sync.Mutex
	retained int
	handles  map[string]tool.ContentBlock
	replies  map[string]codemode.Reply
}

func newCodeModeResults() *codeModeResults {
	return &codeModeResults{handles: map[string]tool.ContentBlock{}, replies: map[string]codemode.Reply{}}
}

type codeModeEnvironment struct {
	generation string
	engine     codemode.Environment
	results    *codeModeResults
	busy       bool
	execution  string
	cancel     context.CancelFunc
}

type codeModeExecution struct {
	runtime     *Runtime
	environment *codeModeEnvironment
	state       codemode.ExecutionState
	retire      bool
	once        sync.Once
}

// Non-language test/embedding adapters retain the existing Executor contract.
// Actual persistent engines expose EnvironmentFactory; this wrapper adds no VM.
type scopedCodeModeExecutor struct{ codemode.Executor }

func (scopedCodeModeExecutor) Close() error { return nil }

func newCodeModeEnvironment(executor codemode.Executor) *codeModeEnvironment {
	var engine codemode.Environment
	if factory, ok := executor.(codemode.EnvironmentFactory); ok {
		engine = factory.NewEnvironment()
	} else {
		engine = scopedCodeModeExecutor{executor}
	}
	return &codeModeEnvironment{generation: uuid.NewString(), engine: engine, results: newCodeModeResults()}
}

func (r *Runtime) acquireCodeMode(ctx context.Context, ref session.SessionRef, executor codemode.Executor) (context.Context, *codeModeExecution, error) {
	r.codeModeMu.Lock()
	defer r.codeModeMu.Unlock()
	if r.codeModeEnvironments == nil {
		r.codeModeEnvironments = map[session.SessionRef]*codeModeEnvironment{}
	}
	key := session.NormalizeSessionRef(ref)
	env := r.codeModeEnvironments[key]
	if env == nil {
		env = newCodeModeEnvironment(executor)
		r.codeModeEnvironments[key] = env
	}
	if env.busy {
		return nil, nil, tool.NewError(tool.ErrorCodeInvalidInput, "JavaScript environment is busy; observe the original Task to completion before the next script, or use reset:true to interrupt the old script")
	}
	work, cancel := context.WithCancel(ctx)
	execution := uuid.NewString()
	env.busy = true
	env.execution = execution
	env.cancel = cancel
	return work, &codeModeExecution{runtime: r, environment: env, state: codemode.ExecutionState{Generation: env.generation, Execution: execution}}, nil
}

func (e *codeModeExecution) release() {
	if e == nil {
		return
	}
	// The original Task/Run owner closes its context after terminal persistence.
	// Releasing this slot must not cancel that still-finishing producer.
	e.once.Do(func() {
		e.runtime.codeModeMu.Lock()
		e.environment.busy = false
		e.environment.cancel = nil
		if e.retire {
			for key, current := range e.runtime.codeModeEnvironments {
				if current == e.environment {
					delete(e.runtime.codeModeEnvironments, key)
				}
			}
		}
		e.runtime.codeModeMu.Unlock()
		if e.retire {
			go e.environment.engine.Close()
		}
	})
}

func (r *Runtime) resetCodeMode(ref session.SessionRef, executor codemode.Executor) tool.Result {
	next := newCodeModeEnvironment(executor)
	r.codeModeMu.Lock()
	if r.codeModeEnvironments == nil {
		r.codeModeEnvironments = map[session.SessionRef]*codeModeEnvironment{}
	}
	key := session.NormalizeSessionRef(ref)
	old := r.codeModeEnvironments[key]
	r.codeModeEnvironments[key] = next
	var cancel context.CancelFunc
	if old != nil {
		cancel = old.cancel
	}
	r.codeModeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if old != nil {
		go func() { _ = old.engine.Close() }()
	}
	raw, _ := json.Marshal(codemode.ExecutionState{Generation: next.generation})
	return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}, Metadata: map[string]any{codemode.MetadataScriptCompletion: "completed"}}
}

// ReleaseCodeModeEnvironments releases disposable language state after the host
// has quiesced this Runtime's Session execution resources. It does not authorize
// effects, alter Task/tool facts, or replay interrupted scripts.
func (r *Runtime) ReleaseCodeModeEnvironments() error {
	r.codeModeMu.Lock()
	environments := r.codeModeEnvironments
	r.codeModeEnvironments = nil
	for _, env := range environments {
		if env.cancel != nil {
			env.cancel()
		}
	}
	r.codeModeMu.Unlock()
	var errs []error
	for _, env := range environments {
		errs = append(errs, env.engine.Close())
	}
	return errors.Join(errs...)
}
