package file

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/control/streamspool"
)

// Collect reclaims expired terminal cache partitions. Open partitions and any
// partition with a reader lease are never removed.
func (s *Store) Collect(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return streamspool.ErrClosed
	}
	parts := make([]*partition, 0, len(s.partitions))
	for _, p := range s.partitions {
		parts = append(parts, p)
	}
	s.mu.Unlock()

	var joined error
	now := s.cfg.Now()
	for _, p := range parts {
		if err := ctx.Err(); err != nil {
			return errors.Join(joined, err)
		}
		p.mu.Lock()
		terminal := p.state == streamspool.StateEmptyTerminal || p.state == streamspool.StateSealed || p.state == streamspool.StatePoisoned
		expired := !p.updatedAt.IsZero() && now.Sub(p.updatedAt) >= s.cfg.TerminalTTL
		readers := p.readers
		p.mu.Unlock()
		if terminal && expired && readers == 0 {
			joined = errors.Join(joined, s.removePartition(p, true))
		}
	}
	return joined
}

func (s *Store) removePartition(p *partition, gc bool) error {
	if s == nil || p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readers > 0 || p.state == streamspool.StatePending || p.state == streamspool.StateOpen {
		return streamspool.ErrInUse
	}
	if p.active != nil {
		_ = p.active.Close()
		p.active = nil
	}
	if p.physical {
		if err := removeManagedPartition(s.fsroot, p.dir); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.partitions[p.key]; current != p {
		return nil
	}
	delete(s.partitions, p.key)
	if s.current[p.key.LogicalKey] == p.key {
		delete(s.current, p.key.LogicalKey)
	}
	if p.accounted > 0 {
		s.usedBytes -= min(s.usedBytes, p.accounted)
	}
	if p.physical && s.physicalParts > 0 {
		s.physicalParts--
	}
	if count := p.allocSegments; count > 0 {
		s.segmentCount -= min(s.segmentCount, count)
	}
	if p.writerActive && s.registrations > 0 {
		s.registrations--
	}
	p.writerActive = false
	p.state = streamspool.StateStoreClosed
	p.signalLocked()
	_ = gc
	return nil
}
