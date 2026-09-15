package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestComposerClearAndRestoreAcrossPlatforms(t *testing.T) {
	for _, platform := range []struct {
		goos string
		wsl  bool
	}{{"darwin", false}, {"windows", false}, {"linux", false}, {"linux", true}} {
		for _, running := range []bool{false, true} {
			for _, text := range []string{"draft", "  中文\nsecond line  ", "   ", "/help"} {
				t.Run(fmt.Sprintf("%s/wsl=%t/running=%t/%q", platform.goos, platform.wsl, running, text), func(t *testing.T) {
					m := NewModel(Config{NoColor: true, NoAnimation: true})
					m.keys = defaultKeyMapForPlatform(platform.goos, platform.wsl)
					if running {
						m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
					}
					m.setInputText(text)
					m.syncTextareaFromInput()
					m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
					frames := []string{m.View().Content}
					quit := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
					m.Update(quit)
					if m.textarea.Value() != "" || len(m.input) != 0 || m.ctrlCArmed || m.quit {
						t.Fatal("clear must empty composer without arming exit")
					}
					if len(m.history) != 1 || m.history[0] != text {
						t.Fatalf("history=%q", m.history)
					}
					frames = append(frames, m.View().Content)
					if text == "draft" && strings.Contains(frames[1], text) {
						t.Fatal("cleared draft still rendered")
					}
					m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
					if m.textarea.Value() != text {
						t.Fatalf("restored=%q, want %q", m.textarea.Value(), text)
					}
					if text == "draft" && !strings.Contains(m.View().Content, text) {
						t.Fatal("restored draft not rendered")
					}
					frames = append(frames, m.View().Content)
					if text == "draft" {
						updates := renderFullscreenFramesForTest(t, 80, 24, frames...)
						assertPhysicalFullscreenFrame(t, 80, 24, frames[2], updates)
					}
					m.Update(quit)
					if len(m.history) != 1 {
						t.Fatal("re-clearing duplicated history")
					}
					m.Update(quit)
					if m.quit || !m.ctrlCArmed {
						t.Fatal("first empty quit must only arm")
					}
					m.Update(quit)
					if !m.quit {
						t.Fatal("second empty quit must exit")
					}
				})
			}
		}
	}
}

func TestComposerClearRestoresPasteAndImage(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	text := multiLinePaste("中文", pasteCollapseMinLines)
	m.insertComposerTextOrCollapse(text)
	m.insertAttachmentsAtCursor([]string{"/tmp/draft.png"})
	want, images := expandPastesRemapImages(m.textarea.Value(), m.inputAttachments)
	if len(images) != 1 {
		t.Fatal("fixture missing image")
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if len(m.inputAttachments) != 0 {
		t.Fatal("attachments not cleared")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got, restored := expandPastesRemapImages(m.textarea.Value(), m.inputAttachments)
	if got != want || len(restored) != 1 || restored[0].Name != images[0].Name || restored[0].Offset != images[0].Offset {
		t.Fatalf("restored text=%q images=%+v", got, restored)
	}
}

func TestComposerEmptyQuitRequiresConsecutiveKeysWithinWindow(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	quit := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	m.Update(quit)
	m.lastCtrlCAt = time.Now().Add(-ctrlCExitWindow - time.Second)
	m.Update(quit)
	if m.quit {
		t.Fatal("expired confirmation exited")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m.Update(quit)
	if m.quit {
		t.Fatal("nonconsecutive confirmation exited")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m.Update(quit)
	if m.quit || m.ctrlCArmed || m.textarea.Value() != "" {
		t.Fatal("new input must be cleared, not exit")
	}
}
