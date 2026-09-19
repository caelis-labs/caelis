package tuiapp

import "testing"

func TestActiveCompletionKindDoesNotFallThroughEmptyFlaggedPicker(t *testing.T) {
	tests := []struct {
		name     string
		kind     completionKind
		activate func(*Model)
	}{
		{
			name: "slash argument",
			kind: completionSlashArg,
			activate: func(model *Model) {
				model.slashArgActive = true
				model.slashArgCommand = "plugin rm"
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
