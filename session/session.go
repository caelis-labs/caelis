package session

// Validate checks that the session has a valid ref and timestamps.
func (s Session) Validate() error {
	if err := s.Ref.Validate(); err != nil {
		return err
	}
	if s.CreatedAt.IsZero() {
		return ErrInvalidSession("CreatedAt is zero")
	}
	return nil
}

// WithDefaults fills zero-value fields with sensible defaults.
// Timestamps are not set here — the caller (runner/gateway) is responsible
// for assigning timestamps at creation time.
func (s Session) WithDefaults() Session {
	if s.State == nil {
		s.State = make(State)
	}
	return s
}

// ErrInvalidSession is returned when a session fails validation.
type ErrInvalidSession string

func (e ErrInvalidSession) Error() string {
	return "invalid session: " + string(e)
}
