package cli

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

const (
	headlessOutputSchemaVersion = "caelis.headless/v1"
	headlessOutputTypeEnvelope  = "envelope"
	headlessOutputTypeResult    = "result"
	headlessOutputTypeError     = "error"
)

type headlessEnvelopeOutput struct {
	SchemaVersion string               `json:"schema_version"`
	Type          string               `json:"type"`
	SessionID     string               `json:"session_id"`
	Turn          appserver.TurnTarget `json:"turn"`
	Envelope      json.RawMessage      `json:"envelope"`
}

type headlessErrorOutput struct {
	SchemaVersion string `json:"schema_version"`
	Type          string `json:"type"`
	SessionID     string `json:"session_id,omitempty"`
	Message       string `json:"message"`
}

func writeHeadlessEnvelope(w io.Writer, envelope eventstream.Envelope) error {
	raw, err := wirev1.MarshalEnvelope(envelope)
	if err != nil {
		return err
	}
	return writeHeadlessJSON(w, headlessEnvelopeOutput{
		SchemaVersion: headlessOutputSchemaVersion,
		Type:          headlessOutputTypeEnvelope,
		SessionID:     strings.TrimSpace(envelope.SessionID),
		Turn: appserver.TurnTarget{
			HandleID: strings.TrimSpace(envelope.HandleID),
			RunID:    strings.TrimSpace(envelope.RunID),
			TurnID:   strings.TrimSpace(envelope.TurnID),
		},
		Envelope: raw,
	})
}

func writeHeadlessFailure(
	w io.Writer,
	format outputFormat,
	sessionID string,
	runErr error,
) error {
	if runErr == nil {
		return nil
	}
	if format != outputJSON && format != outputJSONL {
		return runErr
	}
	writeErr := writeHeadlessJSON(w, headlessErrorOutput{
		SchemaVersion: headlessOutputSchemaVersion,
		Type:          headlessOutputTypeError,
		SessionID:     strings.TrimSpace(sessionID),
		Message:       strings.TrimSpace(runErr.Error()),
	})
	if writeErr != nil {
		return errors.Join(runErr, writeErr)
	}
	return runErr
}

func writeHeadlessJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
