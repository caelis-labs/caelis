package httpclient

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestRemoteTaskDirectoryWatchPreservesRevisionAndActivityIdentity(t *testing.T) {
	t.Parallel()

	client, closeServer := newFixtureClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != wirev1.APIPrefix+"/sessions/session-1/tasks/watch" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: "+taskstream.DirectorySnapshotEventName+"\n"+
			"data: {\"revision\":\"18446744073709551615\",\"tasks\":[{\"session_id\":\"session-1\",\"task_id\":\"task-1\",\"handle\":\"child-1\",\"kind\":\"subagent\",\"state\":\"running\",\"running\":true,\"activity_id\":\"activity-2\",\"current_turn_id\":\"task-1:2\"}]}\n\n")
		_, _ = io.WriteString(writer, "event: "+taskstream.StreamDoneEventName+"\ndata: {}\n\n")
	})
	defer closeServer()
	tasks, err := NewTaskClient(client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tasks.WatchDirectory(context.Background(), taskstream.DirectoryWatchRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	select {
	case snapshot, open := <-result.Subscription.Snapshots():
		if !open || snapshot.Revision != ^uint64(0) || len(snapshot.Tasks) != 1 {
			t.Fatalf("remote directory snapshot = %#v open=%v", snapshot, open)
		}
		descriptor := snapshot.Tasks[0]
		if descriptor.Kind != task.KindSubagent || !descriptor.Running || descriptor.ActivityID != "activity-2" || descriptor.CurrentTurnID != "task-1:2" {
			t.Fatalf("remote Task descriptor = %#v", descriptor)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for remote Task directory snapshot")
	}
	select {
	case _, open := <-result.Subscription.Snapshots():
		if open {
			t.Fatal("remote Task directory remained open after explicit done")
		}
	case <-time.After(time.Second):
		t.Fatal("remote Task directory did not close after explicit done")
	}
	if err := result.Subscription.Err(); err != nil {
		t.Fatalf("remote Task directory error = %v, want clean end", err)
	}
}

func TestRemoteTaskEventsCarriesActivityFenceAndEnvelopeIdentity(t *testing.T) {
	t.Parallel()

	client, closeServer := newFixtureClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet ||
			request.URL.Path != wirev1.APIPrefix+"/sessions/session-1/tasks/task-1/events" ||
			request.URL.Query().Get("after") != "cursor-0" ||
			request.URL.Query().Get("activity_id") != "activity-2" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"events":[{"kind":"caelis/notice","cursor":"cursor-1","position":{"transient":{"anchor":{"seq":"0","projection_index":0},"generation":"generation-1","sequence":"1"}},"delivery":{"mode":"transient"},"activity_id":"activity-2","notice":"live"}],"activity_id":"activity-2","resume_mode":"exact"}`)
	})
	defer closeServer()
	tasks, err := NewTaskClient(client)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := tasks.Events(context.Background(), taskstream.ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: "cursor-0", ExpectedActivityID: "activity-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.ActivityID != "activity-2" || len(batch.Events) != 1 ||
		batch.Events[0].ActivityID != "activity-2" {
		t.Fatalf("remote activity-fenced batch = %#v", batch)
	}
}
