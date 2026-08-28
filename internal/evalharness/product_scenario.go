package evalharness

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// ProductScenario is one deterministic product behavior exercised through
// production-owned entry points. The harness records observations but never
// recreates Runtime, Control, projection, or Surface semantics.
type ProductScenario struct {
	Name   string
	Run    func(context.Context) (ProductObservation, error)
	Checks []ProductCheck
}

// ProductObservation keeps the artifacts needed to prove one behavior at its
// semantic boundary and at the user-visible Surface boundary.
type ProductObservation struct {
	RuntimeEvents []*session.Event
	Envelopes     []eventstream.Envelope
	Frames        []ProductFrame
}

// ProductFrame is one named, normalized full-screen observation captured at a
// meaningful point in a product scenario.
type ProductFrame struct {
	Name    string
	Content string
}

// ProductCheck verifies one independently named acceptance condition.
type ProductCheck struct {
	Name   string
	Verify func(ProductObservation) error
}

// RuntimeEventFact is the compact whole-event semantic signature used by
// product scenarios. Display text is intentionally excluded from lifecycle
// identity; user-visible wording belongs to frame checks.
type RuntimeEventFact struct {
	Type            session.EventType
	Visibility      session.Visibility
	NoticeKind      string
	LifecycleStatus string
}

// EnvelopeFact is the compact whole-Envelope semantic signature used by
// product scenarios.
type EnvelopeFact struct {
	Kind           eventstream.Kind
	UpdateType     string
	NoticeKind     eventstream.NoticeKind
	LifecycleState string
	Delivery       eventstream.DeliveryMode
}

// FrameExpectation verifies the complete named frame using stable user-visible
// fragments rather than renderer escape sequences.
type FrameExpectation struct {
	Name            string
	ContainsInOrder []string
	Excludes        []string
	Counts          map[string]int
}

// EvaluateProductScenario runs one scenario and all of its acceptance checks.
func EvaluateProductScenario(ctx context.Context, scenario ProductScenario) (ProductObservation, error) {
	name := strings.TrimSpace(scenario.Name)
	if name == "" {
		return ProductObservation{}, errors.New("evalharness: product scenario name is required")
	}
	if scenario.Run == nil {
		return ProductObservation{}, fmt.Errorf("evalharness: product scenario %q has no runner", name)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observation, err := scenario.Run(ctx)
	if err != nil {
		return observation, fmt.Errorf("evalharness: run product scenario %q: %w", name, err)
	}
	var checkErrors []error
	for index, check := range scenario.Checks {
		checkName := strings.TrimSpace(check.Name)
		if checkName == "" {
			checkName = fmt.Sprintf("check-%d", index+1)
		}
		if check.Verify == nil {
			checkErrors = append(checkErrors, fmt.Errorf("%s: verifier is required", checkName))
			continue
		}
		if checkErr := check.Verify(observation); checkErr != nil {
			checkErrors = append(checkErrors, fmt.Errorf("%s: %w", checkName, checkErr))
		}
	}
	if err := errors.Join(checkErrors...); err != nil {
		return observation, fmt.Errorf("evalharness: product scenario %q failed: %w", name, err)
	}
	return observation, nil
}

// CheckRuntimeEventFacts requires an exact semantic event sequence.
func CheckRuntimeEventFacts(want ...RuntimeEventFact) ProductCheck {
	want = slices.Clone(want)
	return ProductCheck{
		Name: "runtime event sequence",
		Verify: func(observation ProductObservation) error {
			got := make([]RuntimeEventFact, 0, len(observation.RuntimeEvents))
			for _, event := range observation.RuntimeEvents {
				got = append(got, runtimeEventFact(event))
			}
			if !slices.Equal(got, want) {
				return fmt.Errorf("facts = %#v, want %#v", got, want)
			}
			return nil
		},
	}
}

// CheckEnvelopeFacts requires an exact semantic Envelope sequence.
func CheckEnvelopeFacts(want ...EnvelopeFact) ProductCheck {
	want = slices.Clone(want)
	return ProductCheck{
		Name: "ACP envelope sequence",
		Verify: func(observation ProductObservation) error {
			got := make([]EnvelopeFact, 0, len(observation.Envelopes))
			for _, envelope := range observation.Envelopes {
				got = append(got, envelopeFact(envelope))
			}
			if !slices.Equal(got, want) {
				return fmt.Errorf("facts = %#v, want %#v", got, want)
			}
			return nil
		},
	}
}

// CheckFrame verifies one named normalized full-screen frame.
func CheckFrame(want FrameExpectation) ProductCheck {
	want.ContainsInOrder = slices.Clone(want.ContainsInOrder)
	want.Excludes = slices.Clone(want.Excludes)
	want.Counts = cloneStringIntMap(want.Counts)
	return ProductCheck{
		Name: "frame " + strings.TrimSpace(want.Name),
		Verify: func(observation ProductObservation) error {
			frame, ok := findProductFrame(observation.Frames, want.Name)
			if !ok {
				return fmt.Errorf("frame %q was not captured; available=%v", want.Name, productFrameNames(observation.Frames))
			}
			cursor := 0
			for _, fragment := range want.ContainsInOrder {
				index := strings.Index(frame[cursor:], fragment)
				if index < 0 {
					return fmt.Errorf("missing %q after byte %d\nframe:\n%s", fragment, cursor, frame)
				}
				cursor += index + len(fragment)
			}
			for _, fragment := range want.Excludes {
				if strings.Contains(frame, fragment) {
					return fmt.Errorf("unexpected %q\nframe:\n%s", fragment, frame)
				}
			}
			keys := make([]string, 0, len(want.Counts))
			for fragment := range want.Counts {
				keys = append(keys, fragment)
			}
			sort.Strings(keys)
			for _, fragment := range keys {
				if got := strings.Count(frame, fragment); got != want.Counts[fragment] {
					return fmt.Errorf("count(%q) = %d, want %d\nframe:\n%s", fragment, got, want.Counts[fragment], frame)
				}
			}
			return nil
		},
	}
}

func runtimeEventFact(event *session.Event) RuntimeEventFact {
	if event == nil {
		return RuntimeEventFact{}
	}
	visibility := event.Visibility
	if visibility == "" {
		visibility = session.VisibilityCanonical
	}
	fact := RuntimeEventFact{
		Type:       session.EventTypeOf(event),
		Visibility: visibility,
	}
	if notice, ok := session.NoticeOf(event); ok {
		fact.NoticeKind = notice.Kind
	}
	if event.Lifecycle != nil {
		fact.LifecycleStatus = strings.TrimSpace(event.Lifecycle.Status)
	}
	return fact
}

func envelopeFact(envelope eventstream.Envelope) EnvelopeFact {
	fact := EnvelopeFact{
		Kind:       envelope.Kind,
		UpdateType: eventstream.UpdateType(envelope.Update),
		NoticeKind: envelope.NoticeKind,
	}
	if envelope.Lifecycle != nil {
		fact.LifecycleState = strings.TrimSpace(envelope.Lifecycle.State)
	}
	if envelope.Delivery != nil {
		fact.Delivery = envelope.Delivery.Mode
	}
	return fact
}

func findProductFrame(frames []ProductFrame, name string) (string, bool) {
	name = strings.TrimSpace(name)
	for _, frame := range frames {
		if strings.TrimSpace(frame.Name) == name {
			return NormalizeFrame(frame.Content), true
		}
	}
	return "", false
}

func productFrameNames(frames []ProductFrame) []string {
	names := make([]string, 0, len(frames))
	for _, frame := range frames {
		names = append(names, strings.TrimSpace(frame.Name))
	}
	return names
}

func cloneStringIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
