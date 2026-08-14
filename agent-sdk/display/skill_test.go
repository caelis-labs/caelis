package display

import "testing"

func TestSkillContentNameFromHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		title    string
		pathHint string
		want     string
	}{
		{
			name:  "read title",
			title: `Read <skill_content name="review">`,
			want:  "review",
		},
		{
			name:  "namespaced skill",
			title: `Read <skill_content name="superpowers:brainstorm">`,
			want:  "superpowers:brainstorm",
		},
		{
			name:  "escaped name",
			title: `Read <skill_content name="foo&amp;bar">`,
			want:  "foo&bar",
		},
		{
			name:     "path hint",
			pathHint: `<skill_content name="review">`,
			want:     "review",
		},
		{
			name:  "ordinary read title",
			title: "Read src/foo.go",
			want:  "",
		},
		{
			name:  "embedded non read title",
			title: `Command echo <skill_content name="review">`,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SkillContentNameFromHint(tt.title, tt.pathHint); got != tt.want {
				t.Fatalf("SkillContentNameFromHint() = %q, want %q", got, tt.want)
			}
		})
	}
}
