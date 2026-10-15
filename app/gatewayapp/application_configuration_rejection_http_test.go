package gatewayapp_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationConfigurationHTTPRejectionPreservesIssuedRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newRevisionProvider()
	host := startApplicationHTTPHostWithSavedModels(t, filepath.Join(root, "store"), workspace, provider)
	defer func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
		host.close(t)
	}()
	credentialPath := filepath.Join(root, "application.credential")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", credentialPath)
	profile := applicationHTTPProfile()
	profile.Model = "openai/gpt-5.4-mini"
	profile.ReasoningEffort = "low"
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Profile: profilePointer(profile)})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create = %+v %v", created, err)
	}
	promptRevisionHTTP(t, ctx, client, created.SessionID, "prompt")
	select {
	case <-provider.first:
	case <-ctx.Done():
		t.Fatal("provider did not reach admission barrier")
	}
	issued, err := client.SessionConfiguration(ctx, created.SessionID)
	if err != nil || issued.Revision != 1 || issued.LastRequest == nil || issued.LastRequest.Revision != 1 {
		t.Fatalf("issued configuration = %+v %v", issued, err)
	}
	cases := []struct {
		name   string
		patch  sessionprofile.ConfigurationPatch
		code   errorcode.Code
		detail []string
	}{
		{"model", sessionprofile.ConfigurationPatch{Model: stringPointer("openai/not-configured")}, errorcode.InvalidArgument, []string{"model", "openai/not-configured", "not configured"}},
		{"effort", sessionprofile.ConfigurationPatch{ReasoningEffort: stringPointer("unsupported-effort")}, errorcode.Unsupported, []string{profile.Model, "reasoning_effort", "unsupported-effort"}},
		{"tier", sessionprofile.ConfigurationPatch{Model: stringPointer("openai/gpt-4.1"), ReasoningEffort: stringPointer(""), ServiceTier: stringPointer("priority")}, errorcode.Unsupported, []string{"openai/gpt-4.1", "service_tier", "priority"}},
		{"unknown-tier", sessionprofile.ConfigurationPatch{ServiceTier: stringPointer("unsupported-tier")}, errorcode.Unsupported, []string{"service_tier", "unsupported-tier"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := sessionprofile.UpdateConfigurationRequest{OperationID: "rejected-" + test.name, ExpectedConfigurationRevision: 1, Patch: test.patch}
			for range 2 {
				_, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(created.SessionID, request)))
				var remote *httpclient.RemoteError
				if !errors.As(err, &remote) || remote.StatusCode != http.StatusBadRequest || errorcode.CodeOf(err) != test.code {
					t.Fatalf("rejection = %v, want HTTP 400 %s", err, test.code)
				}
				for _, want := range test.detail {
					if !strings.Contains(remote.Detail, want) {
						t.Fatalf("detail %q does not identify %q", remote.Detail, want)
					}
				}
			}
			if receipt, err := client.Operation(ctx, request.OperationID); err != nil || receipt.Outcome != appserver.OutcomeRejected || receipt.Configuration != nil || receipt.ErrorCode != test.code {
				t.Fatalf("rejected update receipt: %+v, %v", receipt, err)
			}
			current, err := client.SessionConfiguration(ctx, created.SessionID)
			if err != nil || !reflect.DeepEqual(current, issued) || len(provider.snapshot()) != 1 {
				t.Fatalf("rejection changed desired or admitted state: %+v %v", current, err)
			}
		})
	}
	// A lost response is a transport uncertainty, not a received rejection.
	credential, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	lossy, err := httpclient.New(httpclient.Config{BaseURL: host.server.URL, BearerToken: string(credential), Compatibility: appserver.CurrentCompatibility(), HTTPClient: &http.Client{Transport: droppedConfigurationResponse{transport: host.server.Client().Transport, operation: "lost-rejection"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = configurationResult(lossy.UpdateSessionConfiguration(ctx, configurationCommand(created.SessionID, sessionprofile.UpdateConfigurationRequest{OperationID: "lost-rejection", ExpectedConfigurationRevision: 1, Patch: cases[2].patch})))
	var remote *httpclient.RemoteError
	if err == nil || errors.As(err, &remote) {
		t.Fatalf("lost response incorrectly reported as a definite rejection: %v", err)
	}
	if receipt, err := client.Operation(ctx, "lost-rejection"); err != nil || receipt.Outcome != appserver.OutcomeRejected || receipt.Configuration != nil {
		t.Fatalf("lost rejection was not reconciled: %+v, %v", receipt, err)
	}
	close(provider.release)
	claimAndCompleteRevisionCall(t, ctx, client, created.SessionID, 1, profile.ToolsVersion, `{"key":"old"}`)
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	requests := provider.snapshot()
	if len(requests) != 2 || requests[0]["model"] != "gpt-5.4-mini" || requests[1]["model"] != "gpt-5.4-mini" {
		t.Fatalf("rejected model change affected the running Turn: %v", requests)
	}
	final, err := client.SessionConfiguration(ctx, created.SessionID)
	if err != nil || final.Revision != 1 || !reflect.DeepEqual(final.Profile, issued.Profile) || final.LastRequest == nil || final.LastRequest.Revision != 1 {
		t.Fatalf("rejected update changed final configuration: %+v %v", final, err)
	}
}
