package controlserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func TestCommandOutcomeHTTPStatuses(t *testing.T) {
	for _, test := range []struct {
		outcome appserver.Outcome
		status  int
	}{
		{outcome: appserver.OutcomeCommitted, status: http.StatusOK},
		{outcome: appserver.OutcomeAccepted, status: http.StatusAccepted},
		{outcome: appserver.OutcomeUnknown, status: http.StatusAccepted},
		{outcome: appserver.OutcomeRejected, status: http.StatusBadRequest},
		{outcome: appserver.OutcomeConflicted, status: http.StatusConflict},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			result := appserver.CommandResult{OperationID: "operation-1", Outcome: test.outcome}
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
		outcome appserver.Outcome
		status  int
	}{
		{outcome: appserver.OutcomeUnknown, status: http.StatusAccepted},
		{outcome: appserver.OutcomeConflicted, status: http.StatusConflict},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			result := appserver.CommandResult{OperationID: "operation-1", Outcome: test.outcome, Detail: "recovery detail"}
			err := appserver.NewOutcomeError(test.outcome, errors.New("uncoded backend failure"))
			recorder := httptest.NewRecorder()
			writeCommandResult(recorder, result, err)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			var got appserver.CommandResult
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Outcome != test.outcome || got.OperationID != result.OperationID {
				t.Fatalf("CommandResult = %#v, want %#v", got, result)
			}
		})
	}

	recorder := httptest.NewRecorder()
	writeCommandResult(recorder, appserver.CommandResult{
		OperationID: "operation-1", Outcome: appserver.OutcomeRejected, Detail: "private backend detail",
	}, appserver.NewOutcomeError(appserver.OutcomeRejected, errors.New("uncoded backend failure")))
	if recorder.Code != http.StatusInternalServerError ||
		!bytes.Contains(recorder.Body.Bytes(), []byte("internal server error")) ||
		bytes.Contains(recorder.Body.Bytes(), []byte("private backend detail")) ||
		bytes.Contains(recorder.Body.Bytes(), []byte("uncoded backend failure")) {
		t.Fatalf("uncoded rejected fallback = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	writeCommandResult(recorder, appserver.CommandResult{}, errors.New("uncoded backend failure"))
	if recorder.Code != http.StatusInternalServerError || !bytes.Contains(recorder.Body.Bytes(), []byte("internal server error")) {
		t.Fatalf("invalid outcome fallback = %d %s", recorder.Code, recorder.Body.String())
	}
}
