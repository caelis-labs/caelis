package tuiapp

type WelcomeBlock struct {
	id        string
	Version   string
	Workspace string
	ModelName string
}

func NewWelcomeBlock(version, workspace, modelName string) *WelcomeBlock {
	return &WelcomeBlock{
		id:        nextBlockID(),
		Version:   version,
		Workspace: workspace,
		ModelName: modelName,
	}
}

func (b *WelcomeBlock) BlockID() string { return b.id }
func (b *WelcomeBlock) Kind() BlockKind { return BlockWelcome }
func (b *WelcomeBlock) Render(ctx BlockRenderContext) []RenderedRow {
	vm := buildWelcomePanelViewModel(
		buildWelcomeViewModel(b.Version, b.Workspace, b.ModelName),
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
