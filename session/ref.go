package session

import (
	"fmt"
	"strings"
)

// Validate checks that the Ref has all required fields populated.
func (r Ref) Validate() error {
	if r.AppName == "" {
		return fmt.Errorf("session ref: AppName is required")
	}
	if r.UserID == "" {
		return fmt.Errorf("session ref: UserID is required")
	}
	if r.WorkspaceKey == "" {
		return fmt.Errorf("session ref: WorkspaceKey is required")
	}
	if r.SessionID == "" {
		return fmt.Errorf("session ref: SessionID is required")
	}
	return nil
}

// String returns a human-readable representation of the ref.
func (r Ref) String() string {
	return strings.Join([]string{r.AppName, r.UserID, r.WorkspaceKey, r.SessionID}, "/")
}

// Equal reports whether two refs identify the same session.
func (r Ref) Equal(other Ref) bool {
	return r.AppName == other.AppName &&
		r.UserID == other.UserID &&
		r.WorkspaceKey == other.WorkspaceKey &&
		r.SessionID == other.SessionID
}
