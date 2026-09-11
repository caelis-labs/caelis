package runtime

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestUserChildInputPreservesUserAndRejectsReplacedBinding(t *testing.T) {
	runtime, task, runner := newLocalControllerChildActivityTask(t)
	trace := &recordingProducerLifecycle{}
	runtime.taskOutput = trace
	active, err := runtime.sessions.Session(t.Context(), task.sessionRef)
	if err != nil {
		t.Fatal(err)
	}
	var binding session.ParticipantBinding
	for _, p := range active.Participants {
		if p.Kind == session.ParticipantKindSubagent {
			binding = p
			break
		}
	}
	if binding.ID == "" {
		t.Fatal("missing child fixture")
	}
	parts := []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "aGk="}}
	_, err = runtime.SubmitUserChildInput(t.Context(), task.sessionRef, binding, "owner", "user guide", parts)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if trace.binding.ActivityID == "" || trace.binding.ActivityID != got.ActivityID {
		t.Fatalf("user follow-up bypassed shared output binding: %#v", trace.binding)
	}
	if !got.UserInput || got.Source.Kind != session.ActorKindUser || got.Source.ID != "owner" || got.Input != "user guide" {
		t.Fatalf("human input=%#v", got)
	}
	if len(got.ContentParts) != 1 || got.ContentParts[0].Data != parts[0].Data {
		t.Fatalf("image lost: %#v", got)
	}
	binding.AttachmentGeneration += "stale"
	if _, err = runtime.SubmitUserChildInput(t.Context(), task.sessionRef, binding, "owner", "do not send", nil); err == nil {
		t.Fatal("stale binding accepted")
	}
	_, err = runtime.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{Target: task.handle, Source: session.ActorRef{Kind: session.ActorKindUser, ID: "owner"}, Input: "spoof", UserInput: true})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("Agent endpoint accepted human flag: %v", err)
	}
}
