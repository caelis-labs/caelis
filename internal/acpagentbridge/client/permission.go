package client

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
)

func validatePermissionRequest(req RequestPermissionRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(string(req.SessionId)) == "" {
		return fmt.Errorf("sessionId is required")
	}
	if strings.TrimSpace(string(req.ToolCall.ToolCallId)) == "" {
		return fmt.Errorf("toolCall.toolCallId is required")
	}
	options := make([]approval.Option, 0, len(req.Options))
	for _, option := range req.Options {
		options = append(options, approval.Option{
			ID:   string(option.OptionId),
			Name: option.Name,
			Kind: string(option.Kind),
		})
	}
	return approval.ValidateACPOptions(options)
}
