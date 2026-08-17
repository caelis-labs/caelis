package gatewayapp

func testControlCommandBackend(stack *Stack) *controlCommandBackend {
	if stack == nil {
		return nil
	}
	if stack.commandBackend == nil {
		stack.commandBackend = &controlCommandBackend{
			composition:         &stack.composition,
			modelRecovery:       stack.modelRecovery,
			hostAuthentications: map[string]struct{}{},
		}
	}
	return stack.commandBackend
}

// plugins keeps plugin implementation tests focused on the mutable Host
// service without restoring a production Stack mirror.
func (s *Stack) plugins() PluginService {
	if s == nil {
		return PluginService{}
	}
	return testControlCommandBackend(s).plugins()
}
