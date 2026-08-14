package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func TestScrollbarGeometryMapsEndpointsMonotonically(t *testing.T) {
	const (
		total   = 101
		visible = 10
	)
	geometry := newScrollbarGeometry(total, visible, 50)
	if !geometry.scrollable() {
		t.Fatal("test geometry is not scrollable")
	}
	if got := geometry.offsetForPointer(0, 0); got != 0 {
		t.Fatalf("top pointer offset = %d, want 0", got)
	}
	if got := geometry.offsetForPointer(visible-1, 0); got != total-visible {
		t.Fatalf("bottom pointer offset = %d, want %d", got, total-visible)
	}

	previous := -1
	for position := range visible {
		offset := geometry.offsetForPointer(position, 0)
		if offset < previous {
			t.Fatalf("pointer offset decreased at row %d: %d after %d", position, offset, previous)
		}
		previous = offset
	}
}

func TestScrollbarGeometryOneRowIsNotDraggable(t *testing.T) {
	geometry := newScrollbarGeometry(100, 1, 0)
	if geometry.scrollable() {
		t.Fatalf("one-row geometry = %+v, want no draggable track", geometry)
	}
}

func TestViewportScrollbarOneRowDoesNotClaimMouse(t *testing.T) {
	model := newViewportScrollbarTestModel(t, 100, 1, 0)
	mouse := viewportScrollbarMouse(model, 0)

	if target, ok := model.viewportScrollbarTargetAtMouse(mouse.X, mouse.Y); ok {
		t.Fatalf("one-row scrollbar target = %+v, want no hit target", target)
	}
	if handled, _ := model.beginScrollbarDrag(mouse); handled {
		t.Fatal("one-row scrollbar press was handled, want it left to other mouse routes")
	}
	if model.scrollbarDrag.active {
		t.Fatal("one-row scrollbar press started a drag")
	}
}

func TestScrollbarRenderingUsesSharedGeometry(t *testing.T) {
	const (
		total   = 30
		visible = 10
		offset  = 10
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	geometry := newScrollbarGeometry(total, visible, offset)
	lines := make([]string, visible)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index)
	}

	rendered := addScrollbar(lines, 12, visible, offset, total, model.theme, true)
	for index, line := range rendered {
		isThumb := strings.HasSuffix(ansi.Strip(line), "▎")
		if isThumb != geometry.thumbContains(index) {
			t.Fatalf("rendered thumb at row %d = %v, geometry = %+v, line = %q", index, isThumb, geometry, line)
		}
	}
}

func TestViewportScrollbarPressOnThumbDoesNotJump(t *testing.T) {
	model := newViewportScrollbarTestModel(t, 30, 10, 10)
	geometry := model.viewportScrollbarGeometry()
	position := geometry.thumbStart + minInt(1, geometry.thumbLength-1)
	mouse := viewportScrollbarMouse(model, position)

	handled, _ := model.beginScrollbarDrag(mouse)
	if !handled {
		t.Fatal("thumb press was not handled")
	}
	if got := model.viewport.YOffset(); got != 10 {
		t.Fatalf("offset after pressing thumb = %d, want unchanged 10", got)
	}
	if got := model.scrollbarDrag.grabOffset; got != position-geometry.thumbStart {
		t.Fatalf("grab offset = %d, want %d", got, position-geometry.thumbStart)
	}
}

func TestViewportScrollbarDragReachesBothEndpoints(t *testing.T) {
	model := newViewportScrollbarTestModel(t, 30, 10, 10)
	geometry := model.viewportScrollbarGeometry()
	position := geometry.thumbStart + minInt(1, geometry.thumbLength-1)
	handled, _ := model.beginScrollbarDrag(viewportScrollbarMouse(model, position))
	if !handled {
		t.Fatal("thumb press was not handled")
	}

	if changed := model.dragViewportScrollbarTo(model.viewport.Height() - 1); !changed {
		t.Fatal("drag to bottom did not change the offset")
	}
	if got, want := model.viewport.YOffset(), model.viewportMaxOffset(); got != want {
		t.Fatalf("bottom drag offset = %d, want %d", got, want)
	}
	if !model.isViewportFollowTail() {
		t.Fatal("bottom drag did not restore follow-tail")
	}

	if changed := model.dragViewportScrollbarTo(0); !changed {
		t.Fatal("drag to top did not change the offset")
	}
	if got := model.viewport.YOffset(); got != 0 {
		t.Fatalf("top drag offset = %d, want 0", got)
	}
	if model.isViewportFollowTail() {
		t.Fatal("top drag unexpectedly kept follow-tail")
	}
}

func newViewportScrollbarTestModel(t *testing.T, total, visible, offset int) *Model {
	t.Helper()
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 80
	model.viewport.SetWidth(40)
	model.viewport.SetHeight(visible)
	lines := make([]string, total)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index)
	}
	model.viewportStyledLines = append([]string(nil), lines...)
	model.viewportPlainLines = append([]string(nil), lines...)
	model.viewport.SetContentLines(lines)
	model.viewport.SetYOffset(offset)
	model.refreshViewportFollowStateFromOffset()
	return model
}

func viewportScrollbarMouse(model *Model, y int) tea.Mouse {
	return tea.Mouse{
		Button: tea.MouseLeft,
		X:      model.mainColumnX() + tuikit.GutterNarrative + model.viewport.Width(),
		Y:      y,
	}
}
