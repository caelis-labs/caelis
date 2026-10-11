package client

import (
	"encoding/json"
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

// Notice is the normalized presentation of a standard ACP v1 session notice.
// It carries no model context, replay history, or approval authority.
type Notice struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Severity      string         `json:"severity"`
	Title         string         `json:"title"`
	Description   string         `json:"description,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

func decodeNotice(raw json.RawMessage) (Notice, error) {
	var fields acpsdk.SessionUpdateNotice
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Notice{}, err
	}
	if strings.TrimSpace(fields.Title) == "" {
		return Notice{}, fmt.Errorf("invalid ACP notice: severity and non-empty title required")
	}
	notice := Notice{SessionUpdate: fields.SessionUpdate, Severity: string(fields.Severity), Title: fields.Title}
	if fields.Description != nil {
		notice.Description = *fields.Description
	}
	if fields.Meta != nil {
		meta, err := json.Marshal(fields.Meta)
		if err != nil {
			return Notice{}, err
		}
		if err := json.Unmarshal(meta, &notice.Meta); err != nil {
			return Notice{}, err
		}
	}
	return notice, nil
}
