package eval

type evalTerminalOutputMeta struct {
	TerminalID string
	Data       string
}

func evalTerminalOutput(meta map[string]any) (evalTerminalOutputMeta, bool) {
	values, _ := meta["terminal_output"].(map[string]any)
	terminalID, _ := values["terminal_id"].(string)
	data, _ := values["data"].(string)
	if terminalID == "" || data == "" {
		return evalTerminalOutputMeta{}, false
	}
	return evalTerminalOutputMeta{TerminalID: terminalID, Data: data}, true
}

func evalTerminalID(meta map[string]any, key string) string {
	values, _ := meta[key].(map[string]any)
	terminalID, _ := values["terminal_id"].(string)
	return terminalID
}
