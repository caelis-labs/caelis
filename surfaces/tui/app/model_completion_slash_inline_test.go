package tuiapp

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestSlashSkillQueryAtCursorRejectsURLsAndPaths(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		input  string
		cursor int
	}{
		{name: "https scheme", input: "https://example.com"},
		{name: "http scheme", input: "see http://example.com"},
		{name: "url path", input: "https://example.com/docs/readme"},
		{name: "relative path", input: "foo/bar"},
		{name: "nested path", input: "path/to/file.go"},
		{name: "rooted path after text", input: "see /usr/bin"},
		{name: "rooted path at first segment", input: "see /usr/bin", cursor: len([]rune("see /usr"))},
		{name: "dot slash path", input: "./src"},
		{name: "parent path", input: "../pkg/app"},
		{name: "windows drive", input: `C:/Users/docs`},
		{name: "file scheme", input: "file://tmp/notes.md"},
		{name: "unicode prose suffix", input: "/lint这个命令怎么用?", cursor: len([]rune("/lint"))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := []rune(tt.input)
			cursor := tt.cursor
			if cursor == 0 {
				cursor = len(input)
			}
			if _, _, _, ok := slashSkillQueryAtCursor(input, cursor); ok {
				t.Fatalf("slashSkillQueryAtCursor(%q) opened a skill token, want URL/path ignored", tt.input)
			}
		})
	}
}

func TestSlashSkillQueryAtCursorAcceptsTokenBoundaries(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		input     string
		wantQuery string
		wantSpan  string
	}{
		{name: "line start", input: "/lint", wantQuery: "lint", wantSpan: "/lint"},
		{name: "after space", input: "use /lint", wantQuery: "lint", wantSpan: "/lint"},
		{name: "partial query", input: "use /li", wantQuery: "li", wantSpan: "/li"},
		{name: "after newline", input: "use\n/brain", wantQuery: "brain", wantSpan: "/brain"},
		{name: "after open paren", input: "(/cmpctl", wantQuery: "cmpctl", wantSpan: "/cmpctl"},
		{name: "namespaced", input: "please /superpowers:brain", wantQuery: "superpowers:brain", wantSpan: "/superpowers:brain"},
		{name: "empty after slash", input: "use /", wantQuery: "", wantSpan: "/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := []rune(tt.input)
			start, end, query, ok := slashSkillQueryAtCursor(input, len(input))
			if !ok {
				t.Fatalf("slashSkillQueryAtCursor(%q) ok = false, want token", tt.input)
			}
			if query != tt.wantQuery {
				t.Fatalf("query = %q, want %q", query, tt.wantQuery)
			}
			if got := string(input[start:end]); got != tt.wantSpan {
				t.Fatalf("span = %q, want %q", got, tt.wantSpan)
			}
		})
	}
}

func TestIdleLineStartSlashCompletionStillMixesCommandsAndSkills(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"help", "status"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "lint", Display: "lint", Kind: "Skill", Detail: "Run lint checks"},
			}, nil
		},
	})
	model.setInputText("/")
	loadSlashSkillCatalog(t, model)

	if model.slashSkillOnly {
		t.Fatal("idle line-start slash overlay is skill-only, want mixed commands and skills")
	}
	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/help", "/lint", "/status"}) {
		t.Fatalf("idle line-start slashCandidates = %#v, want mixed commands and skills", got)
	}
}

func TestInlineSlashSkillCompletionInsertsMultipleCanonicalSkills(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"help", "status"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "lint", Display: "lint", Kind: "Skill"},
				{Value: "superpowers:brainstorm", Display: "brainstorm", Kind: "Plugin"},
			}, nil
		},
	})

	model.setInputText("review with /li then later")
	model.cursor = len([]rune("review with /li"))
	loadSlashSkillCatalog(t, model)
	if !model.slashSkillOnly {
		t.Fatal("inline slash overlay mixed commands, want skill-only")
	}
	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/lint"}) {
		t.Fatalf("inline slashCandidates = %#v, want skill-only /lint", got)
	}
	if got := model.cfg.Commands; !reflect.DeepEqual(got, []string{"help", "status"}) {
		t.Fatalf("commands = %#v, inline overlay must not mutate the command catalog", got)
	}
	model.applySlashCommandCompletion()
	if got := string(model.input); got != "review with /lint then later" {
		t.Fatalf("first inline skill insert = %q, want surrounding text preserved", got)
	}

	model.setInputText("review with /lint then /br")
	model.refreshSlashCommands()
	if got := model.slashCommandDisplay("/superpowers:brainstorm"); got != "/brainstorm" {
		t.Fatalf("slashCommandDisplay = %q, want local alias while inserting canonical value", got)
	}
	model.applySlashCommandCompletion()
	if got := string(model.input); got != "review with /lint then /superpowers:brainstorm " {
		t.Fatalf("second inline skill insert = %q, want canonical /skill and preserved first reference", got)
	}
}

func TestRunningSlashCompletionIsSkillOnly(t *testing.T) {
	var submitted []string
	model := NewModel(Config{
		Commands: []string{"help", "status", "model", "resume"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "lint", Display: "lint", Kind: "Skill"},
				{Value: "superpowers:brainstorm", Display: "brainstorm", Kind: "Plugin"},
			}, nil
		},
		ResumeComplete: func(context.Context, string, int) ([]ResumeCandidate, error) {
			t.Fatal("resume completion requested while running")
			return nil, nil
		},
		SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
			t.Fatal("slash argument completion requested while running")
			return nil, nil
		},
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = append(submitted, submission.Text)
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
	})
	model.beginLiveTurn(SubmissionModeDefault, true, time.Unix(1, 0))

	model.setInputText("/")
	loadSlashSkillCatalog(t, model)
	if !model.slashSkillOnly {
		t.Fatal("running line-start overlay mixed commands, want skill-only")
	}
	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/lint", "/superpowers:brainstorm"}) {
		t.Fatalf("running slashCandidates = %#v, want skills without ordinary commands", got)
	}

	model.setInputText("/help")
	model.refreshSlashCommands()
	if len(model.slashCandidates) != 0 {
		t.Fatalf("running /help slashCandidates = %#v, want ordinary command hidden", model.slashCandidates)
	}

	model.setInputText("continue with /li")
	model.refreshSlashCommands()
	if !model.slashSkillOnly || !reflect.DeepEqual(model.slashCandidates, []string{"/lint"}) {
		t.Fatalf("running inline overlay = skillOnly=%v candidates=%#v, want /lint", model.slashSkillOnly, model.slashCandidates)
	}
	handled, cmd := model.handleSlashCommandKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashCommandKey(enter) = false while running, want skill insert")
	}
	if cmd != nil {
		t.Fatal("running skill completion submitted a command, want insert only")
	}
	if got := string(model.input); got != "continue with /lint " {
		t.Fatalf("running skill insert = %q, want canonical skill token", got)
	}
	if len(submitted) != 0 {
		t.Fatalf("submitted = %#v, want no running skill submission", submitted)
	}

	model.setInputText("/resume ")
	model.syncTextareaFromInput()
	runCompletionCmd(t, model, model.refreshCompletionOverlaysNow())
	if model.resumeActive || len(model.resumeCandidates) != 0 {
		t.Fatal("running /resume opened resume completion")
	}
	if model.slashArgActive || model.isWizardActive() {
		t.Fatal("running /resume opened slash-arg or wizard completion")
	}

	model.setInputText("/model ")
	model.syncTextareaFromInput()
	runCompletionCmd(t, model, model.refreshCompletionOverlaysNow())
	if model.slashArgActive || model.isWizardActive() || len(model.slashArgCandidates) != 0 {
		t.Fatal("running /model opened ordinary slash-arg or wizard completion")
	}
}

func TestRunningCompletionEscapeDismissesBeforeInterrupt(t *testing.T) {
	t.Run("skill", func(t *testing.T) {
		interrupted := false
		model := NewModel(Config{
			SkillComplete: func(string, int) ([]CompletionCandidate, error) {
				return []CompletionCandidate{{Value: "lint", Display: "lint", Kind: "Skill"}}, nil
			},
			CancelRunning: func() bool {
				interrupted = true
				return true
			},
		})
		model.beginLiveTurn(SubmissionModeDefault, true, time.Unix(3, 0))
		model.setInputText("/li")
		loadSlashSkillCatalog(t, model)
		if len(model.slashCandidates) == 0 {
			t.Fatal("running Skill completion did not open")
		}

		next, cmd := model.handleKey(keyPress("esc"))
		model = next.(*Model)
		if interrupted || cmd != nil {
			t.Fatalf("Esc interrupted running turn=%v cmd=%v, want completion dismiss only", interrupted, cmd != nil)
		}
		if !model.turnRunning() || len(model.slashCandidates) != 0 {
			t.Fatalf("after Esc running=%v slashCandidates=%#v", model.turnRunning(), model.slashCandidates)
		}
	})

	t.Run("file", func(t *testing.T) {
		interrupted := false
		model := NewModel(Config{
			FileComplete: func(context.Context, string, int) ([]CompletionCandidate, error) {
				return []CompletionCandidate{{Value: "docs/architecture.md", Display: "docs/architecture.md"}}, nil
			},
			CancelRunning: func() bool {
				interrupted = true
				return true
			},
		})
		model.beginLiveTurn(SubmissionModeDefault, true, time.Unix(4, 0))
		model.setInputText("@docs")
		runCompletionCmd(t, model, model.requestMentionCompletion(0))
		if len(model.mentionCandidates) == 0 {
			t.Fatal("running @file completion did not open")
		}

		next, cmd := model.handleKey(keyPress("esc"))
		model = next.(*Model)
		if interrupted || cmd != nil {
			t.Fatalf("Esc interrupted running turn=%v cmd=%v, want completion dismiss only", interrupted, cmd != nil)
		}
		if !model.turnRunning() || len(model.mentionCandidates) != 0 {
			t.Fatalf("after Esc running=%v mentionCandidates=%#v", model.turnRunning(), model.mentionCandidates)
		}
	})
}

func TestRunningFileCompletionRequestsWorkspaceCandidates(t *testing.T) {
	var queries []string
	model := NewModel(Config{
		FileComplete: func(_ context.Context, query string, limit int) ([]CompletionCandidate, error) {
			queries = append(queries, query)
			if query != "docs" || limit != completionCandidateFetchLimit {
				t.Errorf("FileComplete(%q, %d), want workspace query docs and initial page", query, limit)
			}
			return []CompletionCandidate{{Value: "docs/architecture.md", Display: "docs/architecture.md"}}, nil
		},
	})
	model.beginLiveTurn(SubmissionModeDefault, true, time.Unix(2, 0))
	model.setInputText("@docs")
	model.syncTextareaFromInput()

	if !model.hasMentionCompletionTarget() {
		t.Fatal("hasMentionCompletionTarget() = false while running, want @file available")
	}
	if model.requestCompletionRefresh() == nil {
		t.Fatal("requestCompletionRefresh() = nil while running, want @file refresh armed")
	}
	runCompletionCmd(t, model, model.requestMentionCompletion(0))
	if !reflect.DeepEqual(queries, []string{"docs"}) {
		t.Fatalf("FileComplete queries = %#v, want running workspace completion", queries)
	}
	if len(model.mentionCandidates) != 1 || model.mentionCandidates[0].Value != "docs/architecture.md" {
		t.Fatalf("running mentionCandidates = %#v, want workspace file", model.mentionCandidates)
	}
	model.applyMentionCompletion()
	if got := string(model.input); got != "@docs/architecture.md " {
		t.Fatalf("running @file insert = %q, want workspace mention", got)
	}
}

func TestURLAndPathSlashDoNotOpenSkillCompletion(t *testing.T) {
	calls := 0
	model := NewModel(Config{
		Commands: []string{"help"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			calls++
			return []CompletionCandidate{{Value: "lint", Display: "lint", Kind: "Skill"}}, nil
		},
	})

	for _, input := range []string{
		"https://example.com",
		"see http://example.com/docs",
		"foo/bar",
		"path/to/file",
	} {
		model.resetSlashSkillCatalog()
		model.setInputText(input)
		model.syncTextareaFromInput()
		runCompletionCmd(t, model, model.refreshCompletionOverlaysNow())
		if model.completionOverlayActive() {
			t.Fatalf("input %q opened completion overlay, want URL/path ignored", input)
		}
		if len(model.slashCandidates) != 0 {
			t.Fatalf("input %q slashCandidates = %#v, want none", input, model.slashCandidates)
		}
	}
	if calls != 0 {
		t.Fatalf("SkillComplete calls = %d, want URLs and paths disconnected from skill completion", calls)
	}
}
