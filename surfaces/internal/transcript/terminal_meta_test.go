package transcript

func terminalInfoMetaForDisplayTest(terminalID string) map[string]any {
	return map[string]any{terminalInfoMetaKey: map[string]any{"terminal_id": terminalID}}
}
