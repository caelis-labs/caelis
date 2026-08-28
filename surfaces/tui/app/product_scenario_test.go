package tuiapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/internal/evalharness"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func TestProductScenarioContextCompactionRuntimeToPhysicalTUI(t *testing.T) {
	for _, test := range []struct {
		name          string
		outcome       evalharness.ContextCompactionOutcome
		runtimeFacts  []evalharness.RuntimeEventFact
		envelopeFacts []evalharness.EnvelopeFact
		terminalFrame evalharness.FrameExpectation
	}{
		{
			name:    "success",
			outcome: evalharness.ContextCompactionSuccess,
			runtimeFacts: []evalharness.RuntimeEventFact{
				{Type: session.EventTypeLifecycle, Visibility: session.VisibilityUIOnly, LifecycleStatus: session.LifecycleStatusContextCompacting},
				{Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical},
				{Type: session.EventTypeNotice, Visibility: session.VisibilityUIOnly, NoticeKind: session.EventNoticeKindCompact},
			},
			envelopeFacts: []evalharness.EnvelopeFact{
				{Kind: eventstream.KindLifecycle, LifecycleState: session.LifecycleStatusContextCompacting, Delivery: eventstream.DeliveryTransient},
				{Kind: eventstream.KindSessionUpdate, UpdateType: schema.UpdateCompact, Delivery: eventstream.DeliveryCanonical},
				{Kind: eventstream.KindNotice, NoticeKind: eventstream.NoticeKindCompact, Delivery: eventstream.DeliveryTransient},
			},
			terminalFrame: evalharness.FrameExpectation{
				Name:            "terminal",
				ContainsInOrder: []string{transcript.CompactNoticeLabel, ">"},
				Excludes:        []string{"Compacting context", session.LifecycleStatusContextCompacting, "CONTEXT CHECKPOINT"},
				Counts:          map[string]int{transcript.CompactNoticeLabel: 1},
			},
		},
		{
			name:    "failure",
			outcome: evalharness.ContextCompactionFailure,
			runtimeFacts: []evalharness.RuntimeEventFact{
				{Type: session.EventTypeLifecycle, Visibility: session.VisibilityUIOnly, LifecycleStatus: session.LifecycleStatusContextCompacting},
				{Type: session.EventTypeNotice, Visibility: session.VisibilityUIOnly, NoticeKind: session.EventNoticeKindCompactFailed},
			},
			envelopeFacts: []evalharness.EnvelopeFact{
				{Kind: eventstream.KindLifecycle, LifecycleState: session.LifecycleStatusContextCompacting, Delivery: eventstream.DeliveryTransient},
				{Kind: eventstream.KindNotice, NoticeKind: eventstream.NoticeKindCompactFailed, Delivery: eventstream.DeliveryTransient},
			},
			terminalFrame: evalharness.FrameExpectation{
				Name:            "terminal",
				ContainsInOrder: []string{"Context Compact failed: provider unavailable", ">"},
				Excludes:        []string{"Compacting context", session.LifecycleStatusContextCompacting},
				Counts:          map[string]int{"Context Compact failed: provider unavailable": 1},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scenario := evalharness.ProductScenario{
				Name: "context compaction " + test.name,
				Run: func(ctx context.Context) (evalharness.ProductObservation, error) {
					return runCompactionProductScenario(ctx, t, test.outcome)
				},
				Checks: []evalharness.ProductCheck{
					evalharness.CheckRuntimeEventFacts(test.runtimeFacts...),
					evalharness.CheckEnvelopeFacts(test.envelopeFacts...),
					evalharness.CheckFrame(evalharness.FrameExpectation{
						Name:            "compacting",
						ContainsInOrder: []string{"Compacting context", ">"},
						Excludes:        []string{"Thinking", session.LifecycleStatusContextCompacting},
					}),
					evalharness.CheckFrame(test.terminalFrame),
				},
			}
			if _, err := evalharness.EvaluateProductScenario(t.Context(), scenario); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runCompactionProductScenario(
	ctx context.Context,
	t *testing.T,
	outcome evalharness.ContextCompactionOutcome,
) (evalharness.ProductObservation, error) {
	t.Helper()

	const (
		width  = 96
		height = 24
	)
	runtimeRun, err := evalharness.RunContextCompaction(ctx, outcome)
	if err != nil {
		return evalharness.ProductObservation{}, err
	}
	activity := slices.IndexFunc(runtimeRun.LiveEvents, func(event *session.Event) bool {
		return event != nil && event.Lifecycle != nil &&
			event.Lifecycle.Status == session.LifecycleStatusContextCompacting
	})
	if activity < 0 {
		return evalharness.ProductObservation{}, fmt.Errorf("Runtime emitted no context compacting activity: %#v", runtimeRun.LiveEvents)
	}
	noticeKind := session.EventNoticeKindCompact
	if outcome == evalharness.ContextCompactionFailure {
		noticeKind = session.EventNoticeKindCompactFailed
	}
	notice := slices.IndexFunc(runtimeRun.LiveEvents, func(event *session.Event) bool {
		payload, ok := session.NoticeOf(event)
		return ok && payload.Kind == noticeKind
	})
	if notice < 0 {
		return evalharness.ProductObservation{}, fmt.Errorf("Runtime emitted no %s notice: %#v", noticeKind, runtimeRun.LiveEvents)
	}
	compact := slices.IndexFunc(runtimeRun.DurableEvents, func(event *session.Event) bool {
		return session.EventTypeOf(event) == session.EventTypeCompact
	})
	if outcome == evalharness.ContextCompactionSuccess && compact < 0 {
		return evalharness.ProductObservation{}, fmt.Errorf("Runtime persisted no context checkpoint: %#v", runtimeRun.DurableEvents)
	}
	if outcome == evalharness.ContextCompactionFailure && compact >= 0 {
		return evalharness.ProductObservation{}, fmt.Errorf("failed Runtime compaction persisted a checkpoint: %#v", runtimeRun.DurableEvents[compact])
	}

	started := make(chan struct{}, 1)
	model := NewModel(Config{
		AppName:     "CAELIS",
		Version:     "acceptance",
		Workspace:   "/tmp/product-scenario",
		ModelAlias:  "scripted/product-scenario",
		NoColor:     true,
		NoAnimation: true,
		OnStart: func() {
			select {
			case started <- struct{}{}:
			default:
			}
		},
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))

	terminal := vt.NewSafeEmulator(width, height)
	t.Cleanup(func() { _ = terminal.Close() })
	terminal.RegisterCsiHandler(ansi.Command('?', '$', 'p'), func(ansi.Params) bool { return true })
	terminal.RegisterOscHandler(10, func([]byte) bool { return true })
	terminal.RegisterOscHandler(11, func([]byte) bool { return true })
	terminal.RegisterOscHandler(12, func([]byte) bool { return true })

	program := tea.NewProgram(
		model,
		tea.WithInput(nil),
		tea.WithOutput(terminal),
		tea.WithWindowSize(width, height),
		tea.WithFPS(120),
		tea.WithoutSignalHandler(),
	)
	var (
		runErr error
		done   = make(chan struct{})
	)
	go func() {
		defer close(done)
		_, runErr = program.Run()
	}()
	stop := func() error {
		program.Quit()
		select {
		case <-done:
			return runErr
		case <-time.After(3 * time.Second):
			program.Kill()
			return fmt.Errorf("physical TUI did not stop")
		}
	}
	defer func() {
		select {
		case <-done:
		default:
			program.Kill()
		}
	}()

	select {
	case <-ctx.Done():
		return evalharness.ProductObservation{}, context.Cause(ctx)
	case <-started:
	case <-time.After(2 * time.Second):
		return evalharness.ProductObservation{}, fmt.Errorf("physical TUI did not start")
	}

	observation := evalharness.ProductObservation{}
	project := func(event *session.Event) error {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		default:
		}
		observation.RuntimeEvents = append(observation.RuntimeEvents, session.CloneEvent(event))
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: event.SessionID},
			event,
			acpprojector.SessionEventTransport{RunID: "run-product-compaction", TurnID: productEventTurnID(event)},
		)
		envelopes := acpprojector.ProjectSessionEventEnvelope(base, event)
		if len(envelopes) == 0 {
			return fmt.Errorf("project event %q: no ACP envelopes", session.EventTypeOf(event))
		}
		for _, envelope := range envelopes {
			observation.Envelopes = append(observation.Envelopes, envelope)
			program.Send(envelope)
		}
		return nil
	}

	if err := project(runtimeRun.LiveEvents[activity]); err != nil {
		return observation, err
	}
	compacting, err := waitForProductScreen(ctx, terminal, func(screen string) bool {
		return strings.Contains(screen, "Compacting context")
	})
	if err != nil {
		return observation, err
	}
	observation.Frames = append(observation.Frames, evalharness.ProductFrame{Name: "compacting", Content: compacting})

	switch outcome {
	case evalharness.ContextCompactionSuccess:
		if err := project(runtimeRun.DurableEvents[compact]); err != nil {
			return observation, err
		}
		if err := project(runtimeRun.LiveEvents[notice]); err != nil {
			return observation, err
		}
	case evalharness.ContextCompactionFailure:
		if err := project(runtimeRun.LiveEvents[notice]); err != nil {
			return observation, err
		}
	default:
		return observation, fmt.Errorf("unknown compaction product outcome %q", outcome)
	}

	terminalFrame, err := waitForProductScreen(ctx, terminal, func(screen string) bool {
		return !strings.Contains(screen, "Compacting context") &&
			strings.Contains(screen, map[evalharness.ContextCompactionOutcome]string{
				evalharness.ContextCompactionSuccess: transcript.CompactNoticeLabel,
				evalharness.ContextCompactionFailure: "Context Compact failed: provider unavailable",
			}[outcome])
	})
	if err != nil {
		return observation, err
	}
	observation.Frames = append(observation.Frames, evalharness.ProductFrame{Name: "terminal", Content: terminalFrame})
	if err := stop(); err != nil {
		return observation, fmt.Errorf("run physical TUI: %w", err)
	}
	return observation, nil
}

func productEventTurnID(event *session.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}

func waitForProductScreen(
	ctx context.Context,
	terminal *vt.SafeEmulator,
	ready func(string) bool,
) (string, error) {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		screen := evalharness.NormalizeFrame(terminal.Render())
		if ready(screen) {
			return screen, nil
		}
		select {
		case <-ctx.Done():
			return screen, context.Cause(ctx)
		case <-deadline.C:
			return screen, fmt.Errorf("physical TUI did not reach expected screen\n%s", screen)
		case <-ticker.C:
		}
	}
}
