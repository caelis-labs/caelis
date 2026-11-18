package appserveradapter

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestDeleteModelsUsesOneHostRevisionAndReportsConfirmedIDs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome appserver.Outcome
		detail  string
	}{
		{name: "committed", outcome: appserver.OutcomeCommitted},
		{name: "committed with warning", outcome: appserver.OutcomeCommitted, detail: "Session reconciliation failed"},
		{name: "conflicted", outcome: appserver.OutcomeConflicted, detail: "Host revision changed"},
		{name: "unknown", outcome: appserver.OutcomeUnknown, detail: "readback unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &modelBatchConfigurationProbe{result: appserver.CommandResult{Outcome: tc.outcome, Detail: tc.detail}}
			adapter := &SessionClientAdapter{sessionClient: client, configClient: client, statusClient: client, sessionID: "selected"}
			ids := []string{"provider:one", "acp:codex:two"}
			confirmed, err := adapter.DeleteModels(t.Context(), append(slices.Clone(ids), " PROVIDER:one ", ""))
			if (err != nil) != (tc.detail != "") {
				t.Fatalf("DeleteModels error = %v", err)
			}
			if err != nil {
				var receipt *appserver.CommandReceiptError
				if !errors.As(err, &receipt) || receipt.Receipt.Outcome != tc.outcome {
					t.Fatalf("missing receipt: %v", err)
				}
			}
			if tc.outcome == appserver.OutcomeCommitted && !slices.Equal(confirmed, ids) || tc.outcome != appserver.OutcomeCommitted && len(confirmed) != 0 {
				t.Fatalf("confirmed IDs = %v", confirmed)
			}
			if len(client.status) != 1 || client.status[0].SessionID != "" || client.status[0].IncludeDiagnostics {
				t.Fatalf("Host revision reads = %#v", client.status)
			}
			if len(client.requests) != 1 || client.requests[0].ExpectedRevision == nil || *client.requests[0].ExpectedRevision != 10 || !slices.Equal(client.requests[0].ProfileIDs, ids) {
				t.Fatalf("batch requests = %#v", client.requests)
			}
		})
	}
}

type modelBatchConfigurationProbe struct {
	appserver.SessionClient
	appserver.ConfigurationClient
	status   []appserver.StatusRequest
	requests []appserver.DeleteModelsRequest
	result   appserver.CommandResult
}

func (*modelBatchConfigurationProbe) Initialize(context.Context) (appserver.ServerInfo, error) {
	return appserver.ServerInfo{Capabilities: []string{appserver.CapabilityDeleteModels}}, nil
}

func (p *modelBatchConfigurationProbe) SessionStatus(_ context.Context, req appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	p.status = append(p.status, req)
	var result controlstatus.StatusSnapshot
	result.Configuration.Revision = 10
	return result, nil
}

func (p *modelBatchConfigurationProbe) DeleteModels(_ context.Context, req appserver.DeleteModelsRequest) (appserver.CommandResult, error) {
	p.requests = append(p.requests, req)
	p.result.OperationID = req.OperationID
	return p.result, nil
}
