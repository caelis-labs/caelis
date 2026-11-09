package file

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestTaskStateReadsSelectSessionAndTaskWithoutPayloads(t *testing.T) {
	store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
	ref := taskSessionRef("state-session")
	for _, entry := range []*task.Entry{
		{TaskID: "completed", Session: ref, State: task.StateCompleted},
		{TaskID: "steering", Session: ref, State: task.StateRunning, Metadata: map[string]any{"supports_steering": true}},
		{TaskID: "uncertain", Session: ref, State: task.StateUnknownOutcome, Metadata: map[string]any{"supports_steering": true}},
		{TaskID: "foreign", Session: taskSessionRef("other"), State: task.StateRunning},
	} {
		entry.Kind = task.KindSubagent
		entry.Spec = map[string]any{"prompt": strings.Repeat("spec", 4096)}
		entry.Result = map[string]any{"final_message": strings.Repeat("result", 4096)}
		if err := store.Upsert(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ReadTaskStates(t.Context(), ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	full, err := store.ListSession(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]task.StateSnapshot, 0, len(full))
	for _, entry := range full {
		steering, _ := entry.Metadata["supports_steering"].(bool)
		want = append(want, task.StateSnapshot{TaskID: entry.TaskID, Revision: entry.Revision, State: entry.State, SupportsSteering: steering})
	}
	if !reflect.DeepEqual(all, want) || len(all) != 3 {
		t.Fatalf("state-only projection differs: got %+v, want %+v", all, want)
	}
	selected, err := store.ReadTaskStates(t.Context(), ref, []string{" steering ", "foreign", "absent"})
	if err != nil || len(selected) != 1 || selected[0].TaskID != "steering" || !selected[0].SupportsSteering {
		t.Fatalf("selected states = %+v, %v", selected, err)
	}
	current, err := store.Get(t.Context(), "steering")
	if err != nil {
		t.Fatal(err)
	}
	current.State, current.Metadata["supports_steering"] = task.StateUnknownOutcome, false
	updated, err := store.Put(t.Context(), task.PutRequest{Entry: current, ExpectedRevision: current.Revision})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.ReadTaskStates(t.Context(), ref, []string{"steering"})
	if err != nil || len(fresh) != 1 || fresh[0].State != task.StateUnknownOutcome || fresh[0].Revision != updated.Revision || fresh[0].SupportsSteering {
		t.Fatalf("stale task state = %+v, %v", fresh, err)
	}
	// Caller mutation cannot corrupt any later observation.
	fresh[0].State = task.StateCompleted
	again, err := store.ReadTaskStates(t.Context(), ref, []string{"steering"})
	if err != nil || again[0].State != task.StateUnknownOutcome {
		t.Fatalf("caller changed durable state: %+v, %v", again, err)
	}
}

func TestTaskStateSteeringMatchesFullMetadataDecoder(t *testing.T) {
	for _, metadata := range []string{
		`null`, ``, " \n\t ", "\u00a0{\"supports_steering\":true}\u00a0", `{}`, `{"supports_steering":true}`,
		`{"supports_steering":false}`, `{"supports_steering":"true"}`, `{"supports_steering":1}`,
		`{"supports_steering":true,"supports_steering":false}`, `{"supports_steering":false,"supports_steering":true}`,
		`{"\u0073upports_steering":true}`, `{"SUPPORTS_STEERING":true}`,
		`{"supports_steering":{}}`, `{"supports_steering":1e10000}`, `{"supports_steering":true,"unrelated":1e10000}`, `{"unrelated":1e10000}`, `[]`, `true`, `"text"`, `{"supports_steering":`,
	} {
		t.Run(metadata, func(t *testing.T) {
			store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
			ref := taskSessionRef("metadata")
			if err := store.Upsert(t.Context(), &task.Entry{TaskID: "target", Kind: task.KindSubagent, Session: ref, State: task.StateRunning}); err != nil {
				t.Fatal(err)
			}
			db, err := store.store.openSessionIndex()
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`UPDATE tasks SET metadata_json=? WHERE task_id='target'`, metadata)
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			full, fullErr := store.ListSession(t.Context(), ref)
			states, stateErr := store.ReadTaskStates(t.Context(), ref, nil)
			if (fullErr != nil) != (stateErr != nil) {
				t.Fatalf("full error %v, state-only error %v", fullErr, stateErr)
			}
			if fullErr == nil {
				want, _ := full[0].Metadata["supports_steering"].(bool)
				if len(states) != 1 || states[0].SupportsSteering != want {
					t.Fatalf("steering = %+v, want %v", states, want)
				}
			}
		})
	}
}

func TestTaskStateReadHonorsCanceledStoreAcquisition(t *testing.T) {
	store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
	store.store.mu.Lock()
	defer store.store.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.ReadTaskStates(ctx, taskSessionRef("canceled"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
	var missing *TaskStore
	if _, err := missing.ReadTaskStates(t.Context(), taskSessionRef("missing"), nil); err == nil {
		t.Fatal("nil TaskStore returned a successful observation")
	}
}
