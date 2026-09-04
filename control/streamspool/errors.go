package streamspool

import "errors"

var (
	ErrUnavailable   = errors.New("stream spool unavailable")
	ErrInUse         = errors.New("stream spool root is already in use")
	ErrNotFound      = errors.New("stream spool partition not found")
	ErrExpired       = errors.New("stream spool cursor expired")
	ErrCorrupt       = errors.New("stream spool record corrupt")
	ErrClosed        = errors.New("stream spool closed")
	ErrLimit         = errors.New("stream spool limit reached")
	ErrEmptyTerminal = errors.New("stream spool producer ended without records")
)
