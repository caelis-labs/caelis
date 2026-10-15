package gatewayapp_test

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestApplicationProfileNamedModelUsesFrozenImageCapability(t *testing.T) {
	yes, no := true, false
	for _, test := range []struct {
		name   string
		parent *bool
		child  *bool
	}{
		{"text-parent-image-child", &no, &yes},
		{"image-parent-text-child", &yes, &no},
		{"image-parent-unknown-child", &yes, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			root := t.TempDir()
			provider := &applicationHTTPModel{}
			host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
			defer host.close(t)
			connectApplicationChildModelWithImage(t, ctx, host, "profile-parent", test.parent)
			connectApplicationChildModelWithImage(t, ctx, host, "profile-named", test.child)
			catalog, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
			if err != nil {
				t.Fatal(err)
			}
			target, effort := "", ""
			for _, profile := range catalog.Targets {
				if profile.Backend.Provider != nil && strings.HasSuffix(profile.Backend.Provider.ModelConfigID, "/profile-named") {
					target, effort = profile.ID, profile.Effort.DefaultEffort
				}
			}
			status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
			if err != nil || target == "" {
				t.Fatalf("named native catalog: %+v %v", catalog, err)
			}
			_, err = host.host.CreateAgentRole(ctx, appserver.CreateAgentRoleRequest{WriteBase: appserver.WriteBase{OperationID: "named-image-role", ExpectedRevision: &status.Configuration.Revision}, Role: agentbinding.Role{Handle: "vision", Description: "Named native provider"}, Binding: agentbinding.Binding{ProfileID: target, Effort: effort}})
			if err != nil {
				t.Fatal(err)
			}
			client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
			profile := applicationHTTPProfile()
			profile.Model = "custom/profile-parent"
			created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "parent"}, Profile: profilePointer(profile)})
			if err != nil {
				t.Fatal(err)
			}
			state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: appserver.WriteBase{OperationID: "named-image-child", SessionID: created.SessionID, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}, Handle: "vision", Label: "vision", Input: "child-only-input", Background: true,
				ContentParts: []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(tinyApplicationPNG(t))}}})
			if test.child != nil && *test.child {
				if err != nil || result.Resource == nil {
					t.Fatalf("native image child task admission: %+v %v", result, err)
				}
				child, _ := applicationAttachedChild(t, ctx, client, created.SessionID, "vision")
				completeBackgroundCall(t, ctx, client, backgroundCall(t, ctx, client, child))
				if !strings.Contains(provider.snapshotFrom(0), "data:image/png;base64,") {
					t.Fatal("named image-capable model did not receive image")
				}
				return
			}
			if err == nil || result.ErrorCode != "unsupported" || result.Resource != nil || result.Target.TurnID != "" {
				t.Fatalf("unsupported image admission lost original result: %+v %v", result, err)
			}
			tasks, err := httpclient.NewTaskClient(client)
			if err != nil {
				t.Fatal(err)
			}
			list, err := tasks.List(ctx, taskstream.ListRequest{SessionID: created.SessionID})
			if err != nil || len(list.Tasks) != 1 || list.Tasks[0].Running || list.Tasks[0].State != task.StateFailed || list.Tasks[0].CurrentTurnID != "" {
				t.Fatalf("unsupported image lost native terminal Task: %+v %v", list, err)
			}
			state, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
			if err != nil || provider.requestCount() != 0 {
				t.Fatalf("unsupported image admitted model request: %d, %v", provider.requestCount(), err)
			}
			for _, participant := range state.Participants {
				if participant.SessionID != "" {
					t.Fatalf("unsupported image created child: %+v", participant)
				}
			}

		})
	}
}
