package httpclient

import (
	"math"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func TestSessionInterruptPreservesNativeSessionCASOverHTTP(t *testing.T) {
	var received appserver.SessionInterruptRequest
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wirev1.APIPrefix+"/sessions/native-session/interrupt" {
			http.NotFound(w, r)
			return
		}
		assertFixtureRequest(t, r, http.MethodPost, "interrupt-native")
		decodeFixtureRequest(t, r, &received)
		writeFixtureJSON(t, w, http.StatusOK, appserver.CommandResult{OperationID: "interrupt-native", Outcome: appserver.OutcomeCommitted, SessionID: "native-session", Revision: math.MaxUint64})
	})
	defer closeServer()
	revision := uint64(math.MaxUint64)
	result, err := client.InterruptSession(t.Context(), appserver.SessionInterruptRequest{
		WriteBase: appserver.WriteBase{OperationID: "interrupt-native", SessionID: "native-session", ExpectedRevision: &revision},
		Reason:    "Esc",
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != revision {
		t.Fatalf("interrupt result=%#v err=%v", result, err)
	}
	if received.OperationID != "interrupt-native" || received.ExpectedRevision == nil || *received.ExpectedRevision != revision || received.ExpectedControllerEpoch != "" || received.Reason != "Esc" {
		t.Fatalf("Session interrupt lost native CAS: %#v", received)
	}
}
