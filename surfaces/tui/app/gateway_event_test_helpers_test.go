package tuiapp

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
)

func gatewayEventMsg(env kernel.EventEnvelope) tea.Msg {
	if env.Err != nil {
		return TaskResultMsg{Err: env.Err, Interrupted: testGatewayUserInterruptError(env.Err)}
	}
	if msg, ok := gatewayApprovalReviewHintMsg(env.Event); ok && msg.Pending {
		return msg
	}
	return TranscriptEventsMsg{Events: ProjectGatewayEventToTranscriptEvents(env.Event)}
}

func testGatewayUserInterruptError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return text == "context canceled" || strings.Contains(text, "context canceled")
}
