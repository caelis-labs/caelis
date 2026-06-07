package runner

import (
	"fmt"
	"sync"

	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

type toolObserverBridge struct {
	mu         sync.Mutex
	sessionRef session.Ref
	runID      string
	events     []session.Event
}

func newToolObserverBridge(ref session.Ref, runID string) *toolObserverBridge {
	return &toolObserverBridge{sessionRef: ref, runID: runID}
}

func (o *toolObserverBridge) BeforeTool(call tool.Call) {
	o.appendNotice("tool_start", call, tool.Result{}, nil)
}

func (o *toolObserverBridge) AfterTool(call tool.Call, result tool.Result, err error) {
	o.appendNotice("tool_end", call, result, err)
}

func (o *toolObserverBridge) Drain() []session.Event {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := append([]session.Event(nil), o.events...)
	o.events = nil
	return out
}

func (o *toolObserverBridge) appendNotice(phase string, call tool.Call, result tool.Result, err error) {
	if o == nil {
		return
	}
	details := map[string]any{
		"phase":     phase,
		"tool_name": call.Name,
		"call_id":   call.CallID,
	}
	if result.IsError {
		details["is_error"] = true
	}
	if err != nil {
		details["error"] = err.Error()
	}
	evt := session.Event{
		SessionRef: o.sessionRef,
		RunID:      o.runID,
		Kind:       session.EventKindNotice,
		Visibility: session.VisibilityUIOnly,
		NoticePayload: &session.NoticePayload{
			Level:   "info",
			Text:    fmt.Sprintf("%s %s", phase, call.Name),
			Details: details,
		},
	}
	o.mu.Lock()
	o.events = append(o.events, evt)
	o.mu.Unlock()
}

func drainObserverBridge(obs *toolObserverBridge, yield func(session.Event, error) bool) bool {
	for _, evt := range obs.Drain() {
		if !yield(evt, nil) {
			return false
		}
	}
	return true
}
