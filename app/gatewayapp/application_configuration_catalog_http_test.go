package gatewayapp_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestApplicationConfigurationHostHTTPModelConnectedDuringTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newRevisionProvider()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, provider)
	defer func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
		host.close(t)
	}()
	connectRevisionHTTPModel(t, ctx, host, "gpt-5.4-mini")
	client, _ := registerApplicationHTTP(t, ctx, host, "late-model", filepath.Join(root, "application.credential"))
	profile := applicationHTTPProfile()
	profile.Model = "openai/gpt-5.4-mini"
	profile.ReasoningEffort = "low"
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "create-late-model"}, Profile: profile,
	})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create = %+v %v", created, err)
	}
	session := created.SessionID
	promptRevisionHTTP(t, ctx, client, session, "prompt-late-model")
	select {
	case <-provider.first:
	case <-ctx.Done():
		t.Fatal("initial model request did not reach the provider barrier")
	}
	issued, err := client.ApplicationConfiguration(ctx, session)
	if err != nil || issued.LastRequest == nil || issued.LastRequest.Revision != 1 || issued.LastRequest.Model != "gpt-5.4-mini" {
		t.Fatalf("initial request admission = %+v %v", issued, err)
	}

	// This model is absent from the activation snapshot. Public configuration
	// admission and the next request must resolve the same current Host catalog.
	connectRevisionHTTPModel(t, ctx, host, "gpt-5.4")
	newModel := "openai/gpt-5.4"
	updated, err := client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
		OperationID: "select-late-model", ExpectedConfigurationRevision: 1,
		Patch: application.ConfigurationPatch{Model: &newModel},
	})
	if err != nil || updated.Revision != 2 || updated.Profile.Model != newModel || updated.LastRequest == nil || *updated.LastRequest != *issued.LastRequest {
		t.Fatalf("hot update while first request is issued = %+v %v", updated, err)
	}
	if requests := provider.snapshot(); len(requests) != 1 {
		t.Fatalf("configuration update dispatched a provider request: %d", len(requests))
	}
	close(provider.release)
	oldCall := claimAndCompleteRevisionCall(t, ctx, client, session, 1, profile.ToolsVersion, `{"key":"old"}`)
	waitApplicationHTTPIdle(t, ctx, client, session)

	requests := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("Turn produced %d provider requests, want the old callback request and continuation on the newly connected model", len(requests))
	}
	assertRevisionPrefix(t, requests[0], "gpt-5.4-mini", "low", "", profile.Instructions, "string")
	assertRevisionPrefix(t, requests[1], "gpt-5.4", "low", "", profile.Instructions, "string")
	if !strings.Contains(fmt.Sprint(requests[1]["input"]), "CONFIGURATION_TOOL_SENTINEL") {
		t.Fatal("new model request lost the callback result from the issued request")
	}
	observed, err := client.ApplicationConfiguration(ctx, session)
	if err != nil || observed.LastRequest == nil || observed.LastRequest.Revision != 2 || observed.LastRequest.Model != "gpt-5.4" || observed.LastRequest.TurnID != oldCall.TurnID || observed.LastRequest.TurnID != issued.LastRequest.TurnID || observed.LastRequest.RequestID == issued.LastRequest.RequestID || observed.LastRequest.ReasoningEffort != "low" || observed.LastRequest.ToolsVersion != profile.ToolsVersion {
		t.Fatalf("newly connected model did not execute under revision 2 in the same Turn: %+v %v", observed, err)
	}
	assertRevisionUserHistory(t, applicationHTTPHistory(t, ctx, client, session), 1)
}
