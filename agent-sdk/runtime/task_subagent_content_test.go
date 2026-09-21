package runtime

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

func TestSidecarInitialContentSurvivesTaskAndContextReplay(t *testing.T) {
	runner := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "image inspected"}}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	runtime.tasks.store = newFileTaskStoreForTest(t)
	parts := []model.ContentPart{{Type: model.ContentPartText, Text: "inspect image"}, {Type: model.ContentPartImage, Data: "aW1hZ2U=", MimeType: "image/png"}}
	snapshot, err := runtime.StartSubagentWithOptions(t.Context(), active.SessionRef, "helper", "inspect image", "slash_profile_orbit", StartSubagentOptions{SpawnID: "image-task", ContentParts: parts})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.spawnContext.ContentParts, parts) {
		t.Fatalf("spawn lost content: %#v", runner.spawnContext.ContentParts)
	}
	entry, err := runtime.tasks.store.Get(t.Context(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	restored := runtime.tasks.rehydrateSubagentTask(entry)
	if !reflect.DeepEqual(restored.contentParts, parts) {
		t.Fatalf("Task rehydrate lost content: %#v", restored.contentParts)
	}
	loaded, err := runtime.sessions.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range loaded.Events {
		if event.Type != session.EventTypeUser || event.Scope == nil || event.Scope.Participant.DelegationID != snapshot.Ref.TaskID {
			continue
		}
		found = true
		if event.Message == nil || event.Message.Role != model.RoleUser || !reflect.DeepEqual(model.ContentPartsFromParts(event.Message.Parts), parts) {
			t.Fatalf("canonical model context lost user content: %#v", event.Message)
		}
	}
	if !found {
		t.Fatal("replayed model context omitted sidecar user input")
	}
}

func TestBackgroundStartReturnsWithoutWaitingForChild(t *testing.T) {
	runner := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateRunning, Running: true}}
	runner.waitHook = func() { t.Error("background admission waited for child progress") }
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	snapshot, err := runtime.StartSubagentWithOptions(t.Context(), active.SessionRef, "helper", "work independently", "user", StartSubagentOptions{ReturnOnStart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Running || snapshot.Ref.TaskID == "" {
		t.Fatalf("background start lost running Task identity: %#v", snapshot)
	}
}
