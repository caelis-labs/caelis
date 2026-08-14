package controlserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	controlclient "github.com/caelis-labs/caelis/control/client"
)

func TestCommandOutcomeHTTPStatuses(t *testing.T) {
	for _, test := range []struct {
		outcome controlclient.Outcome
		status  int
	}{
		{outcome: controlclient.OutcomeCommitted, status: http.StatusOK},
		{outcome: controlclient.OutcomeAccepted, status: http.StatusAccepted},
		{outcome: controlclient.OutcomeUnknown, status: http.StatusAccepted},
		{outcome: controlclient.OutcomeRejected, status: http.StatusBadRequest},
		{outcome: controlclient.OutcomeConflicted, status: http.StatusConflict},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			result := controlclient.CommandResult{OperationID: "operation-1", Outcome: test.outcome}
			recorder := httptest.NewRecorder()
			writeCommandResult(recorder, result, nil)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}

func TestCommandOutcomeRecoverySurvivesUncodedBackendError(t *testing.T) {
	for _, test := range []struct {
		outcome controlclient.Outcome
		status  int
	}{
		{outcome: controlclient.OutcomeUnknown, status: http.StatusAccepted},
		{outcome: controlclient.OutcomeConflicted, status: http.StatusConflict},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			result := controlclient.CommandResult{OperationID: "operation-1", Outcome: test.outcome, Detail: "recovery detail"}
			err := controlclient.NewOutcomeError(test.outcome, errors.New("uncoded backend failure"))
			recorder := httptest.NewRecorder()
			writeCommandResult(recorder, result, err)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			var got controlclient.CommandResult
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Outcome != test.outcome || got.OperationID != result.OperationID {
				t.Fatalf("CommandResult = %#v, want %#v", got, result)
			}
		})
	}

	recorder := httptest.NewRecorder()
	writeCommandResult(recorder, controlclient.CommandResult{
		OperationID: "operation-1", Outcome: controlclient.OutcomeRejected, Detail: "private backend detail",
	}, controlclient.NewOutcomeError(controlclient.OutcomeRejected, errors.New("uncoded backend failure")))
	if recorder.Code != http.StatusInternalServerError ||
		!bytes.Contains(recorder.Body.Bytes(), []byte("internal server error")) ||
		bytes.Contains(recorder.Body.Bytes(), []byte("private backend detail")) ||
		bytes.Contains(recorder.Body.Bytes(), []byte("uncoded backend failure")) {
		t.Fatalf("uncoded rejected fallback = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	writeCommandResult(recorder, controlclient.CommandResult{}, errors.New("uncoded backend failure"))
	if recorder.Code != http.StatusInternalServerError || !bytes.Contains(recorder.Body.Bytes(), []byte("internal server error")) {
		t.Fatalf("invalid outcome fallback = %d %s", recorder.Code, recorder.Body.String())
	}
}
