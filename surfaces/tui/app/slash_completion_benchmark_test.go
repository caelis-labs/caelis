package tuiapp

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func BenchmarkSlashInteraction(b *testing.B) {
	commands := DefaultCommands()
	skills := benchmarkSlashSkills(1000)

	b.Run("input-slash-and-view", func(b *testing.B) {
		model := benchmarkSlashModel(commands, skills)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			model.textarea.SetValue("")
			model.textarea.CursorStart()
			model.syncInputFromTextarea()
			_, _ = model.handleKey(keyPress("/"))
			_ = model.View()
		}
	})

	b.Run("input-st-and-view", func(b *testing.B) {
		model := benchmarkSlashModel(commands, skills)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			model.textarea.SetValue("/")
			model.textarea.CursorEnd()
			model.syncInputFromTextarea()
			_, _ = model.handleKey(keyPress("st"))
			_ = model.View()
		}
	})

	b.Run("refresh-1000-skills", func(b *testing.B) {
		model := benchmarkSlashModel(commands, skills)
		model.slashSkillCatalog = skills
		model.slashSkillLoaded = true
		model.setInputText("/st")
		model.syncTextareaFromInput()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			model.refreshSlashCommands()
		}
	})

	b.Run("render-1000-skill-overlay", func(b *testing.B) {
		model := benchmarkSlashModel(commands, skills)
		model.slashSkillCatalog = skills
		model.slashSkillLoaded = true
		model.setInputText("/")
		model.syncTextareaFromInput()
		model.refreshSlashCommands()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			_ = model.renderInputOverlay()
		}
	})
}

func benchmarkSlashModel(commands []string, skills []CompletionCandidate) *Model {
	model := NewModel(Config{
		Commands:    commands,
		NoAnimation: true,
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return skills, nil
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(*Model)
}

func benchmarkSlashSkills(count int) []CompletionCandidate {
	items := make([]CompletionCandidate, 0, count)
	for i := range count {
		name := fmt.Sprintf("plugin-%03d:skill-%03d", i/10, i)
		items = append(items, CompletionCandidate{
			Value:   name,
			Display: fmt.Sprintf("skill-%03d", i),
			Kind:    "Skill",
			Detail:  "Benchmark skill completion candidate",
		})
	}
	return items
}
