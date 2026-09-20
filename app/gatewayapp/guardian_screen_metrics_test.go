package gatewayapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// guardianScreenMetrics validates the classifier state that
// guardianJudgmentRequest hands to the Jev screening callback and reports the
// user-message count the Session replay logs. A state whose shape no longer
// matches production must surface a diagnostic instead of a silent zero.
func guardianScreenMetrics(request judgment.Request) (int, error) {
	state, ok := request.State.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("classifier state has type %T, want map[string]any", request.State)
	}
	messages, ok := state["user_messages"].([]string)
	if !ok {
		return 0, fmt.Errorf("classifier state user_messages has type %T, want []string", state["user_messages"])
	}
	if _, ok := state["action"].(map[string]any); !ok {
		return 0, fmt.Errorf("classifier state action has type %T, want map[string]any", state["action"])
	}
	return len(messages), nil
}

// The replay callback logs this count for each approval, so it must measure the
// production screening state and ignore tool history exactly as the classifier
// does. This is an ordinary offline guard, not a live Jev request.
func TestGuardianScreenMetricsCountsCanonicalUserMessages(t *testing.T) {
	req := guardianWindowRequest(t, "metrics")
	events := []*session.Event{
		guardianProjectEvent(guardianSource(1, session.EventTypeUser, "Fix the parser. Do not push.")),
		guardianProjectEvent(guardianSource(2, session.EventTypeToolCall, "go test ./parser")),
		guardianProjectEvent(guardianSource(3, session.EventTypeUser, "Correction: do not run tests now.")),
	}
	request, err := guardianJudgmentRequest(req, events)
	if err != nil {
		t.Fatal(err)
	}
	count, err := guardianScreenMetrics(request)
	if err != nil {
		t.Fatalf("valid classifier state rejected: %v", err)
	}
	if count != 2 {
		t.Fatalf("user message count = %d, want the two canonical user messages", count)
	}

	// An approval with no user history is a legitimate zero, not schema drift.
	empty, err := guardianJudgmentRequest(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := guardianScreenMetrics(empty); err != nil || count != 0 {
		t.Fatalf("empty user history = (%d, %v), want (0, nil)", count, err)
	}
}

func TestGuardianScreenMetricsDiagnosesSchemaDrift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state any
		want  string
	}{
		{name: "nil_state", state: nil, want: "map[string]any"},
		{name: "untyped_state", state: "legacy", want: "map[string]any"},
		{name: "missing_user_messages", state: map[string]any{"action": map[string]any{"tool": "RunCommand"}}, want: "user_messages"},
		{name: "untyped_user_messages", state: map[string]any{"user_messages": []any{"a"}, "action": map[string]any{}}, want: "user_messages"},
		{name: "missing_action", state: map[string]any{"user_messages": []string{"a"}}, want: "action"},
		{name: "untyped_action", state: map[string]any{"user_messages": []string{"a"}, "action": "RunCommand"}, want: "action"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, err := guardianScreenMetrics(judgment.Request{State: tc.state})
			if err == nil {
				t.Fatalf("schema drift accepted as count %d", count)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("diagnostic %q does not name %q", err.Error(), tc.want)
			}
		})
	}
}
