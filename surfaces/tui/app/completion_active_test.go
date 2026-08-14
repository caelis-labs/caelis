package tuiapp

import "testing"

func TestActiveCompletionKindDoesNotFallThroughEmptyFlaggedPicker(t *testing.T) {
	tests := []struct {
		name     string
		kind     completionKind
		activate func(*Model)
	}{
		{
			name: "resume",
			kind: completionResume,
			activate: func(model *Model) {
				model.resumeActive = true
			},
		},
		{
			name: "slash argument",
			kind: completionSlashArg,
			activate: func(model *Model) {
				model.slashArgActive = true
				model.slashArgCommand = "model"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := NewModel(Config{})
			model.slashCandidates = []string{"/alpha", "/bravo"}
			tt.activate(model)

			if got := model.activeCompletionKind(); got != tt.kind {
				t.Fatalf("active completion kind = %v, want %v", got, tt.kind)
			}
			if _, _, ok := model.activeCompletionGeometry(); ok {
				t.Fatal("empty active picker exposed lower completion geometry")
			}
			if rendered := model.renderInputOverlay(); rendered != "" {
				t.Fatalf("empty active picker rendered lower completion: %q", rendered)
			}

			handled, _ := model.handleActiveCompletionKey(keyPress("down"))
			if !handled {
				t.Fatal("active picker did not handle completion navigation")
			}
			if got := model.slashIndex; got != 0 {
				t.Fatalf("lower slash selection moved to %d, want 0", got)
			}
		})
	}
}

func TestActiveCompletionKindRoutesKeysAndGeometryToSamePicker(t *testing.T) {
	model := NewModel(Config{})
	model.resumeActive = true
	model.resumeCandidates = []ResumeCandidate{
		{SessionID: "session-1"},
		{SessionID: "session-2"},
	}
	model.slashArgActive = true
	model.slashArgCommand = "model"
	model.slashArgCandidates = []SlashArgCandidate{
		{Value: "alpha"},
		{Value: "bravo"},
	}

	snapshot, geometry, ok := model.activeCompletionGeometry()
	if !ok || snapshot.kind != completionResume || geometry.kind != completionResume {
		t.Fatalf("active completion snapshot=%+v geometry=%+v ok=%v, want resume", snapshot, geometry, ok)
	}
	handled, _ := model.handleActiveCompletionKey(keyPress("down"))
	if !handled {
		t.Fatal("resume completion did not handle navigation")
	}
	if model.resumeIndex != 1 {
		t.Fatalf("resume index = %d, want 1", model.resumeIndex)
	}
	if model.slashArgIndex != 0 {
		t.Fatalf("lower slash-arg index = %d, want 0", model.slashArgIndex)
	}
}
