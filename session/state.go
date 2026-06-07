package session

// State is the key-value store attached to a session. Keys are
// domain-scoped strings; values are opaque to the session package.
type State map[string]string

// Clone returns a deep copy of the state map.
func (s State) Clone() State {
	if s == nil {
		return nil
	}
	cp := make(State, len(s))
	for k, v := range s {
		cp[k] = v
	}
	return cp
}
