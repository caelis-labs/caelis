package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/bot"
)

// The selector plus current marker exceeds the 40-column picker unless the
// suffix shares a width budget with the name.
const botPickerLongSelector = "xiaomi@token-plan-cn/mimo-v2.5"

func botPickerLongSelectorBots() []bot.Bot {
	return []bot.Bot{
		{ID: "bot-1", SessionID: "chat-1", Config: bot.Config{Name: "Ada"}, ModelSelector: botPickerLongSelector},
		{ID: "bot-2", SessionID: "chat-2", Config: bot.Config{Name: "Team Planning Bot Alpha"}, ModelSelector: botPickerLongSelector},
		{ID: "bot-3", SessionID: "chat-3", Config: bot.Config{Name: "处理长名称的机器人"}, ModelSelector: botPickerLongSelector},
	}
}

func botPickerLongSelectorModel(t *testing.T, width, height int, bots []bot.Bot, submitted *[]Submission) *Model {
	t.Helper()
	model := newBotTestModel(t, width, height, &fakeBotClient{bots: bots}, func(sub Submission) TaskResultMsg {
		*submitted = append(*submitted, sub)
		return TaskResultMsg{ContinueRunning: true}
	})
	stageBotForGolden(model, bots[0])
	model = loadBotPickerForTest(t, model, model.openBotPicker())
	return model
}

func TestBotPickerLongSelectorRowsStayReadableAndHitTestable(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 24}} {
		for _, samePrefix := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/same-prefix=%v", size[0], size[1], samePrefix), func(t *testing.T) {
				bots := botPickerLongSelectorBots()
				if samePrefix {
					bots[1].Config.Name = "Team Alpha"
					bots[2].Config.Name = "Team Beta"
				}
				for target, value := range bots {
					var submitted []Submission
					model := botPickerLongSelectorModel(t, size[0], size[1], bots, &submitted)
					frame := model.View().Content
					assertBotFrameBounds(t, model, frame)
					geometry := model.sessionPicker.geometry
					lines := strings.Split(ansi.Strip(frame), "\n")
					fragment := sliceByDisplayColumns(value.Config.Name, 0, minInt(8, displayColumns(value.Config.Name)))
					paintedY := -1
					// Locate the actual painted row, not the cached mouse target;
					// the footer may also contain the active Bot's name.
					for y := geometry.y; y < geometry.y+geometry.height; y++ {
						if strings.Contains(lines[y], fragment) {
							if paintedY != -1 {
								t.Fatalf("multiple picker rows contain %q:\n%s", fragment, frame)
							}
							paintedY = y
						}
					}
					if paintedY < 0 || geometry.rows[target] != paintedY {
						t.Fatalf("Bot %q: painted row=%d hit rows=%v:\n%s", value.Config.Name, paintedY, geometry.rows, frame)
					}
					if strings.Contains(lines[paintedY], "current") != (target == 0) {
						t.Fatalf("wrong current marker: %q", lines[paintedY])
					}
					updates := renderFullscreenFramesForTest(t, size[0], size[1], frame)
					assertPhysicalFullscreenFrame(t, size[0], size[1], frame, updates)

					point := tea.Mouse{X: geometry.x + model.overlayBorderChromeWidth()/2 + 1, Y: paintedY, Button: tea.MouseLeft}
					model.Update(tea.MouseClickMsg(point))
					point.Button = tea.MouseNone
					_, cmd := model.Update(tea.MouseReleaseMsg(point))
					if cmd == nil || !model.bot.hasPending || model.bot.pending.SessionID != value.SessionID {
						t.Fatalf("clicking %q selected %+v", value.Config.Name, model.bot.pending)
					}
					cmd()
					if len(submitted) != 1 || submitted[0].Text != "/resume "+value.SessionID {
						t.Fatalf("clicking %q dispatched %+v", value.Config.Name, submitted)
					}
				}
			})
		}
	}
}

func TestBotPickerRowLineStaysWithinColumns(t *testing.T) {
	for inner := 16; inner <= 100; inner++ {
		for _, current := range []bool{false, true} {
			for _, name := range []string{"Ada", "Team Planning Bot Alpha", "处理长名称的机器人"} {
				line := botPickerRowLine(inner, "> ", name, strings.Repeat(botPickerLongSelector, 3), current)
				if width := displayColumns(line); width > inner || strings.Contains(line, "\n") {
					t.Fatalf("inner=%d current=%v: invalid width %d: %q", inner, current, width, line)
				}
				fragment := sliceByDisplayColumns(name, 0, minInt(4, displayColumns(name)))
				if !strings.Contains(line, fragment) {
					t.Fatalf("inner=%d: name collapsed: %q", inner, line)
				}
			}
		}
	}
}

func TestBotPickerLongSelectorGoldenFrames(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			var submitted []Submission
			model := botPickerLongSelectorModel(t, size[0], size[1], botPickerLongSelectorBots(), &submitted)
			checkBotGolden(t, fmt.Sprintf("picker_long_selector_%dx%d", size[0], size[1]), size[0], size[1], model.View().Content)
		})
	}
}
