package file

import (
	"sort"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

// makeRoomLocked runs under Store.admission and p.mu. Reclaiming cache bytes
// never changes producer state. Low advances monotonically; readers behind it
// must request a replacement from the namespace owner.
func (p *partition) makeRoomLocked(bytes int64) error {
	s := p.store
	cost := func() (int64, bool) {
		roll := !p.physical || p.active == nil || p.activeBytes+bytes > s.cfg.SegmentBytes
		n := bytes
		if !p.physical {
			n += s.cfg.PartitionAllocationCharge
		}
		if roll {
			n += s.cfg.SegmentAllocationCharge + segmentHeaderSize
		}
		return n, roll
	}
	for {
		n, roll := cost()
		if p.accounted+n <= s.cfg.MaxStreamBytes && (!roll || p.allocSegments < s.cfg.MaxSegmentsPerPartition) {
			break
		}
		if len(p.segments) == 0 {
			return streamspool.ErrLimit
		}
		if err := p.discardFirstSegmentLocked(); err != nil {
			return err
		}
	}
	fits := func() bool {
		n, roll := cost()
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.usedBytes <= s.cfg.MaxBytes-n &&
			(p.physical || s.physicalParts < s.cfg.MaxPartitions) &&
			(!roll || s.segmentCount < s.cfg.MaxSegments)
	}
	if fits() {
		return nil
	}
	type candidate struct {
		part *partition
		at   time.Time
	}
	s.mu.Lock()
	parts := make([]*partition, 0, len(s.partitions))
	for _, other := range s.partitions {
		if other != p {
			parts = append(parts, other)
		}
	}
	s.mu.Unlock()
	var candidates []candidate
	for _, other := range parts {
		other.mu.Lock()
		if other.physical && other.published && other.state != streamspool.StateStoreClosed {
			candidates = append(candidates, candidate{other, other.updatedAt})
		}
		other.mu.Unlock()
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].at.Before(candidates[j].at) })
	for _, c := range candidates {
		c.part.mu.Lock()
		// TTL collection or explicit removal may have won since selection.
		if c.part.state == streamspool.StateStoreClosed {
			c.part.mu.Unlock()
			continue
		}
		for len(c.part.segments) > 0 && !fits() {
			if err := c.part.discardFirstSegmentLocked(); err != nil {
				c.part.mu.Unlock()
				return err
			}
		}
		terminal := !c.part.writerActive && c.part.readers == 0
		c.part.mu.Unlock()
		if terminal {
			_ = s.removePartition(c.part, false)
		}
		if fits() {
			return nil
		}
	}
	// All other windows are empty. The active stream may itself occupy the
	// global budget (including when a caller configures it below MaxStreamBytes).
	for len(p.segments) > 0 && !fits() {
		if err := p.discardFirstSegmentLocked(); err != nil {
			return err
		}
	}
	if !fits() {
		return streamspool.ErrLimit
	}
	return nil
}

func (p *partition) discardFirstSegmentLocked() error {
	seg := p.segments[0]
	if len(p.segments) == 1 && p.active != nil {
		if err := p.active.Close(); err != nil {
			return err
		}
		p.active, p.activeBytes = nil, 0
	}
	// Close descriptors before deletion so a slow reader cannot retain unlinked
	// disk bytes indefinitely, and Windows has the same retention semantics.
	for r := range p.leases {
		r.mu.Lock()
		if r.segment == seg.path && r.file != nil {
			_ = r.file.Close()
			r.file, r.segment = nil, ""
		}
		r.mu.Unlock()
	}
	if err := p.store.fsroot.Remove(seg.path); err != nil {
		return err
	}
	p.segments = p.segments[1:]
	p.low = p.high
	if len(p.segments) > 0 {
		p.low = p.segments[0].base
	}
	charge := seg.bytes + p.store.cfg.SegmentAllocationCharge
	p.allocSegments--
	p.accounted -= charge
	p.store.release(charge, false, true)
	if len(p.segments) == 0 {
		if err := removeManagedPartition(p.store.fsroot, p.dir); err != nil {
			return err
		}
		p.physical = false
		charge = p.store.cfg.PartitionAllocationCharge
		p.accounted -= charge
		p.store.release(charge, true, false)
	}
	p.signalLocked()
	return nil
}
