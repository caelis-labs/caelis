package tuiapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/bot"
)

func TestBotLiveTerminalStatus(t *testing.T) {
	for _, test := range []struct {
		state, status, text string
	}{
		{eventstream.LifecycleStateCompleted, eventstream.LifecycleStateCompleted, "last reply completed"},
		{eventstream.LifecycleStateFailed, eventstream.LifecycleStateFailed, "last reply failed"},
		{eventstream.LifecycleStateCancelled, eventstream.LifecycleStateInterrupted, "last reply interrupted"},
		{eventstream.LifecycleStateInterrupted, eventstream.LifecycleStateInterrupted, "last reply interrupted"},
		{eventstream.LifecycleStateUnknown, eventstream.LifecycleStateUnknown, botOutcomeUnknownText},
	} {
		t.Run(test.state, func(t *testing.T) {
			m := newBotLifecycleTestModel(t)
			m.bot.runStatus = eventstream.LifecycleStateUnknown
			m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
			if m.botRunStatus() != eventstream.LifecycleStateUnknown || m.botConversationText() != "replying" {
				t.Fatalf("provisional reply changed canonical state: %q, %q", m.botRunStatus(), m.botConversationText())
			}
			env := eventstream.TurnLifecycle("handle", "run", "turn", test.state, "", "", time.Now())
			env.SessionID = m.currentSessionID
			m.Update(sessionViewMessage{generation: m.viewGeneration, message: env})
			m.drainPendingRenderEvents(time.Now())
			if m.turnRunning() || m.botRunStatus() != test.status {
				t.Fatalf("terminal state: running=%v status=%q, want %q", m.turnRunning(), m.botRunStatus(), test.status)
			}
			assertBotLifecycleStatus(t, m, test.text)

			// Provisional UI activity does not supersede the last reply.
			m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
			if m.botRunStatus() != test.status || m.botConversationText() != "replying" {
				t.Fatalf("provisional reply changed canonical state: %q, %q", m.botRunStatus(), m.botConversationText())
			}
		})
	}
}

func TestBotProvisionalWorkPreservesReplyStatus(t *testing.T) {
	for _, prior := range []string{eventstream.LifecycleStateCompleted, eventstream.LifecycleStateUnknown} {
		t.Run(prior, func(t *testing.T) {
			for _, test := range []struct {
				name, line     string
				result         TaskResultMsg
				cancelDispatch bool
			}{
				{name: "connect completed", line: "/connect"},
				{name: "connect failed", line: "/connect", result: TaskResultMsg{Err: errors.New("connect failed")}},
				{name: "disconnect completed", line: "/disconnect"},
				{name: "disconnect failed", line: "/disconnect", result: TaskResultMsg{Err: errors.New("disconnect failed")}},
				{name: "input rejected before admission", line: "hello", result: TaskResultMsg{Err: appserver.NewOutcomeError(appserver.OutcomeRejected, errors.New("input rejected"))}},
				{name: "input cancelled before admission", line: "hello", result: TaskResultMsg{Interrupted: true}},
				{name: "input cancelled before dispatch", line: "hello", cancelDispatch: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					m := newBotLifecycleTestModel(t)
					m.bot.runStatus = prior
					want := m.botConversationText()
					calls := 0
					m.cfg.ExecuteLine = func(submission Submission) TaskResultMsg {
						calls++
						if submission.Text != test.line {
							t.Fatalf("submitted %q, want %q", submission.Text, test.line)
						}
						return test.result
					}
					_, cmd := m.submitInteractiveLine(test.line, test.line, nil)
					if !m.turnRunning() || m.botRunStatus() != prior {
						t.Fatalf("provisional submission: running=%v status=%q, want previous %q", m.turnRunning(), m.botRunStatus(), prior)
					}
					dispatch := submissionDispatchMessageForTest(t, cmd)
					if test.cancelDispatch {
						m.requestRunningInterrupt()
					}
					_, execute := m.Update(dispatch)
					if test.cancelDispatch {
						if execute != nil || calls != 0 {
							t.Fatal("cancelled provisional submission was dispatched")
						}
					} else {
						if execute == nil {
							t.Fatal("submission scheduled no dispatch")
						}
						m.Update(execute())
						if calls != 1 {
							t.Fatalf("submission calls = %d, want 1", calls)
						}
					}
					m.drainPendingRenderEvents(time.Now())
					if m.turnRunning() || m.botRunStatus() != prior {
						t.Fatalf("provisional result: running=%v status=%q, want previous %q", m.turnRunning(), m.botRunStatus(), prior)
					}
					assertBotLifecycleStatus(t, m, want)
				})
			}
		})
	}
}

func TestBotCanonicalRunningSupersedesUnknown(t *testing.T) {
	for _, provisional := range []bool{false, true} {
		name := "observed reply"
		if provisional {
			name = "local reply"
		}
		t.Run(name, func(t *testing.T) {
			m := newBotLifecycleTestModel(t)
			m.bot.runStatus = eventstream.LifecycleStateUnknown
			if provisional {
				m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
			}
			env := eventstream.TurnLifecycle("handle", "run", "turn", eventstream.LifecycleStateRunning, "", "", time.Now())
			env.SessionID = m.currentSessionID
			m.Update(sessionViewMessage{generation: m.viewGeneration, message: env})
			m.drainPendingRenderEvents(time.Now())
			if !m.turnRunning() || !m.liveTurn.observed || m.botRunStatus() != eventstream.LifecycleStateRunning {
				t.Fatalf("canonical admission: running=%v observed=%v status=%q", m.turnRunning(), m.liveTurn.observed, m.botRunStatus())
			}
			assertBotLifecycleStatus(t, m, "replying")
		})
	}
}

func TestBotLiveTerminalRejectsOtherScopeSessionAndGeneration(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*eventstream.Envelope, *uint64)
	}{
		{"other session", func(env *eventstream.Envelope, _ *uint64) { env.SessionID = "other-session" }},
		{"child", func(env *eventstream.Envelope, _ *uint64) { env.Scope = eventstream.ScopeSubagent }},
		{"participant", func(env *eventstream.Envelope, _ *uint64) { env.Scope = eventstream.ScopeParticipant }},
		{"approval", func(env *eventstream.Envelope, _ *uint64) { env.ApprovalRequestID = "approval" }},
		{"old view", func(_ *eventstream.Envelope, generation *uint64) { *generation-- }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newBotLifecycleTestModel(t)
			m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
			env := eventstream.TurnFailed("handle", "run", "turn", "unrelated failure", time.Now())
			env.SessionID = m.currentSessionID
			generation := m.viewGeneration
			test.change(&env, &generation)
			m.Update(sessionViewMessage{generation: generation, message: env})
			m.drainPendingRenderEvents(time.Now())
			if !m.turnRunning() || m.botRunStatus() != "" {
				t.Fatalf("unrelated terminal affected Bot: running=%v status=%q", m.turnRunning(), m.botRunStatus())
			}
		})
	}
}

func TestBotTerminalOwnerRequiresActiveConversation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Model, *eventstream.Envelope)
	}{
		{"unselected", func(m *Model, _ *eventstream.Envelope) { m.bot.hasActive = false }},
		{"other current session", func(m *Model, _ *eventstream.Envelope) { m.currentSessionID = "other-session" }},
		{"other Bot session", func(m *Model, _ *eventstream.Envelope) { m.bot.active.SessionID = "other-session" }},
		{"other terminal session", func(_ *Model, env *eventstream.Envelope) { env.SessionID = "other-session" }},
		{"anonymous terminal", func(_ *Model, env *eventstream.Envelope) { env.SessionID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newBotLifecycleTestModel(t)
			m.bot.runStatus = eventstream.LifecycleStateCompleted
			env := eventstream.TurnFailed("handle", "run", "turn", "unrelated failure", time.Now())
			env.SessionID = m.currentSessionID
			test.change(m, &env)
			m.finishLiveTurnFromEnvelope(env)
			if m.botRunStatus() != eventstream.LifecycleStateCompleted {
				t.Fatalf("unrelated terminal overwrote active Bot status: %q", m.botRunStatus())
			}
		})
	}
}

func TestBotReconnectStatusMatchesLiveTerminal(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		name := "reattach"
		if recovery {
			name = "same-session recovery"
		}
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				status, text string
				active       bool
			}{
				{eventstream.LifecycleStateCompleted, "last reply completed", false},
				{eventstream.LifecycleStateFailed, "last reply failed", false},
				{eventstream.LifecycleStateInterrupted, "last reply interrupted", false},
				{eventstream.LifecycleStateCancelled, "last reply interrupted", false},
				{eventstream.LifecycleStateUnknown, botOutcomeUnknownText, false},
				{"", "idle", false},
				{eventstream.LifecycleStateRunning, "replying", true},
			} {
				t.Run(test.text+"/"+test.status, func(t *testing.T) {
					m := newBotLifecycleTestModel(t)
					m.bot.runStatus = eventstream.LifecycleStateFailed
					oldGeneration := m.viewGeneration
					m.Update(sessionViewStartMsg{
						generation: oldGeneration + 1, recovery: recovery,
						state: appserver.SessionState{SessionID: m.currentSessionID, Run: appserver.RunState{
							Status: test.status, Active: test.active, StartedAt: time.Now(),
						}},
					})
					if m.botRunStatus() != eventstream.LifecycleStateFailed {
						t.Fatal("history replaced Bot status before its sync boundary")
					}
					m.Update(sessionViewMessage{generation: m.viewGeneration, message: sessionHistoryReadyMsg{}})
					stale := eventstream.TurnFailed("old-handle", "old-run", "old-turn", "old failure", time.Now())
					stale.SessionID = m.currentSessionID
					m.Update(sessionViewMessage{generation: oldGeneration, message: stale})
					m.drainPendingRenderEvents(time.Now())
					if m.turnRunning() != test.active {
						t.Fatalf("recovered running=%v, want %v", m.turnRunning(), test.active)
					}
					if test.active && m.botRunStatus() != eventstream.LifecycleStateRunning {
						t.Fatalf("recovered active status = %q, want running", m.botRunStatus())
					}
					assertBotLifecycleStatus(t, m, test.text)
				})
			}
		})
	}
}

func newBotLifecycleTestModel(t *testing.T) *Model {
	t.Helper()
	m := newBotTestModel(t, 100, 30, nil, nil)
	m.bot.active = bot.Bot{ID: "bot", SessionID: "bot-session", Config: bot.Config{Name: "Ada", Model: "model"}}
	m.bot.hasActive = true
	m.currentSessionID = m.bot.active.SessionID
	m.viewGeneration = 1
	return m
}

func assertBotLifecycleStatus(t *testing.T, m *Model, want string) {
	t.Helper()
	if got := m.botConversationText(); got != want {
		t.Fatalf("conversation status = %q, want %q", got, want)
	}
	_, _, handled := m.submitBotLine("/status", "/status", nil)
	if !handled {
		t.Fatal("Bot /status was not handled")
	}
	m.syncViewportContent()
	frame := ansi.Strip(strings.Join(viewFrameLines(m), "\n"))
	if !strings.Contains(frame, want) {
		t.Fatalf("rendered /status omitted %q:\n%s", want, frame)
	}
}
