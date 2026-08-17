package gatewayapp

// runtimeProjection returns the process root's Runtime composition without
// exposing it outside gatewayapp. Stack keeps the composition as a named field
// so only deliberate Host methods cross the boundary; detached Session
// Runtimes use runtimeComposition directly.
func (s *Stack) runtimeProjection() *runtimeComposition {
	if s == nil {
		return nil
	}
	return &s.composition
}

func (s *Stack) Models() ModelService {
	return s.runtimeProjection().Models()
}

func (s *Stack) Agents() AgentService {
	return s.runtimeProjection().Agents()
}

func (s *Stack) Skills() SkillService {
	return s.runtimeProjection().Skills()
}

func (s *Stack) plugins() PluginService {
	return s.runtimeProjection().Plugins()
}
