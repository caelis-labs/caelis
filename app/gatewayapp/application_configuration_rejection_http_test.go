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
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
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
	connectRevisionHTTPModel(t, ctx, host, "gpt-4.1")
	credentialPath := filepath.Join(root, "application.credential")
	client, _ := registerApplicationHTTP(t, ctx, host, "rejection", credentialPath)
	profile := applicationHTTPProfile()
	profile.Model = "openai/gpt-5.4-mini"
	profile.ReasoningEffort = "low"
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Profile: profile})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create = %+v %v", created, err)
	}
	promptRevisionHTTP(t, ctx, client, created.SessionID, "prompt")
	select {
	case <-provider.first:
	case <-ctx.Done():
		t.Fatal("provider did not reach admission barrier")
	}
	issued, err := client.ApplicationConfiguration(ctx, created.SessionID)
	if err != nil || issued.Revision != 1 || issued.LastRequest == nil || issued.LastRequest.Revision != 1 {
		t.Fatalf("issued configuration = %+v %v", issued, err)
	}
	cases := []struct {
		name   string
		patch  application.ConfigurationPatch
		code   errorcode.Code
		detail []string
	}{
		{"model", application.ConfigurationPatch{Model: stringPointer("openai/not-configured")}, errorcode.InvalidArgument, []string{"model", "openai/not-configured", "not configured"}},
		{"effort", application.ConfigurationPatch{ReasoningEffort: stringPointer("unsupported-effort")}, errorcode.Unsupported, []string{profile.Model, "reasoning_effort", "unsupported-effort"}},
		{"tier", application.ConfigurationPatch{Model: stringPointer("openai/gpt-4.1"), ReasoningEffort: stringPointer(""), ServiceTier: stringPointer("priority")}, errorcode.Unsupported, []string{"openai/gpt-4.1", "service_tier", "priority"}},
		{"unknown-tier", application.ConfigurationPatch{ServiceTier: stringPointer("unsupported-tier")}, errorcode.Unsupported, []string{"service_tier", "unsupported-tier"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := application.UpdateConfigurationRequest{OperationID: "rejected-" + test.name, ExpectedConfigurationRevision: 1, Patch: test.patch}
			for range 2 {
				_, err := client.UpdateApplicationConfiguration(ctx, created.SessionID, request)
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
			if _, err := client.ApplicationConfigurationOperation(ctx, request.OperationID); errorcode.CodeOf(err) != errorcode.NotFound {
				t.Fatalf("rejected update has a committed receipt: %v", err)
			}
			current, err := client.ApplicationConfiguration(ctx, created.SessionID)
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
	_, err = lossy.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{OperationID: "lost-rejection", ExpectedConfigurationRevision: 1, Patch: cases[2].patch})
	var remote *httpclient.RemoteError
	if err == nil || errors.As(err, &remote) {
		t.Fatalf("lost response incorrectly reported as a definite rejection: %v", err)
	}
	if _, err := client.ApplicationConfigurationOperation(ctx, "lost-rejection"); errorcode.CodeOf(err) != errorcode.NotFound {
		t.Fatalf("lost rejected update has a committed receipt: %v", err)
	}
	close(provider.release)
	claimAndCompleteRevisionCall(t, ctx, client, created.SessionID, 1, profile.ToolsVersion, `{"key":"old"}`)
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	requests := provider.snapshot()
	if len(requests) != 2 || requests[0]["model"] != "gpt-5.4-mini" || requests[1]["model"] != "gpt-5.4-mini" {
		t.Fatalf("rejected model change affected the running Turn: %v", requests)
	}
	final, err := client.ApplicationConfiguration(ctx, created.SessionID)
	if err != nil || final.Revision != 1 || !reflect.DeepEqual(final.Profile, issued.Profile) || final.LastRequest == nil || final.LastRequest.Revision != 1 {
		t.Fatalf("rejected update changed final configuration: %+v %v", final, err)
	}
}
