package tuiapp

import appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"

type WelcomeBlock struct {
	id   string
	Home appviewmodel.HomeView
}

func NewWelcomeBlock(home appviewmodel.HomeView) *WelcomeBlock {
	return &WelcomeBlock{
		id:   nextBlockID(),
		Home: cloneHomeView(home),
	}
}

func (b *WelcomeBlock) BlockID() string { return b.id }
func (b *WelcomeBlock) Kind() BlockKind { return BlockWelcome }
func (b *WelcomeBlock) Render(ctx BlockRenderContext) []RenderedRow {
	vm := buildWelcomePanelViewModel(
		b.Home,
		maxInt(30, minInt(68, maxInt(30, ctx.Width-6))),
		ctx.Theme,
	)
	lines := renderPanelViewModel(ctx.Theme, vm)
	rows := make([]RenderedRow, len(lines))
	for i, line := range lines {
		rows[i] = StyledRow(b.id, line)
	}
	return rows
}
