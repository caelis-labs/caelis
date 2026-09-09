package file

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// HistoryPath returns the stable canonical event-log address for an existing
// Session. The live append-only file may not exist before the first event;
// this address is neither a snapshot nor an access grant.
// Callers must bind their own reader permissions and tolerate incomplete tails.
func (s *Store) HistoryPath(ctx context.Context, ref session.SessionRef) (string, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return "", err
	}
	defer s.mu.Unlock()
	var path string
	err := s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		document, err := s.resolveWritePath(doc.Session)
		if err != nil {
			return err
		}
		path = eventLogPath(document)
		return nil
	})
	return path, err
}
