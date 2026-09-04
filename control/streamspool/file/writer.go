package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

type writer struct {
	partition *partition
}

func (w *writer) Key() streamspool.Key {
	if w == nil || w.partition == nil {
		return streamspool.Key{}
	}
	return w.partition.key
}

func (w *writer) Append(ctx context.Context, recordType uint16, occurredAt time.Time, payload []byte) (streamspool.Offset, error) {
	if w == nil || w.partition == nil {
		return 0, streamspool.ErrUnavailable
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	p := w.partition
	p.mu.Lock()
	forget := false
	defer func() {
		p.mu.Unlock()
		if forget {
			_ = p.store.removePartition(p, false)
		}
	}()
	if p.state != streamspool.StatePending && p.state != streamspool.StateOpen {
		return 0, stateError(p.state)
	}
	if len(payload) > p.store.cfg.MaxRecordBytes {
		p.poisonLocked(streamspool.ErrLimit)
		forget = !p.physical && p.readers == 0
		return 0, fmt.Errorf("%w: record payload is %d bytes", streamspool.ErrLimit, len(payload))
	}
	// Offset must be encoded under the writer mutex.
	encoded, err := encodeRecord(p.high, recordType, occurredAt, payload)
	if err != nil {
		return 0, err
	}
	newPartition := !p.physical
	newSegment := newPartition || p.active == nil || p.activeBytes+int64(len(encoded)) > p.store.cfg.SegmentBytes
	if newSegment && p.allocSegments >= p.store.cfg.MaxSegmentsPerPartition {
		p.poisonLocked(streamspool.ErrLimit)
		forget = !p.physical && p.readers == 0
		return 0, streamspool.ErrLimit
	}
	reserve := int64(len(encoded))
	if newPartition {
		reserve += p.store.cfg.PartitionAllocationCharge
	}
	if newSegment {
		reserve += p.store.cfg.SegmentAllocationCharge + segmentHeaderSize
	}
	if p.accounted+reserve > p.store.cfg.MaxStreamBytes {
		p.poisonLocked(streamspool.ErrLimit)
		forget = !p.physical && p.readers == 0
		return 0, streamspool.ErrLimit
	}
	if err := p.store.reserve(reserve, newPartition, newSegment); err != nil {
		p.poisonLocked(err)
		forget = !p.physical && p.readers == 0
		return 0, err
	}
	p.accounted += reserve
	if newPartition {
		p.physical = true
	}
	if newSegment {
		p.allocSegments++
	}
	if newSegment {
		if err := p.rollLocked(); err != nil {
			cleanupErr := error(nil)
			if newPartition {
				cleanupErr = removeManagedPartition(p.store.fsroot, p.dir)
			}
			if newPartition && cleanupErr == nil {
				p.accounted -= reserve
				p.physical = false
				p.allocSegments--
				p.store.release(reserve, true, true)
			} else {
				// No record bytes were accepted. Keep conservative directory and
				// segment charges until verified whole-partition deletion.
				payloadCharge := int64(len(encoded))
				p.accounted -= payloadCharge
				p.store.release(payloadCharge, false, false)
			}
			p.poisonLocked(err)
			forget = !p.physical && p.readers == 0
			return 0, errors.Join(err, cleanupErr)
		}
	}
	startSize := p.activeBytes
	n, writeErr := p.active.Write(encoded)
	if writeErr != nil || n != len(encoded) {
		if writeErr == nil {
			writeErr = errors.New("short write")
		}
		if info, statErr := p.active.Stat(); statErr == nil {
			p.activeBytes = info.Size()
		} else {
			p.activeBytes = startSize + int64(max(n, 0))
		}
		physicalGrowth := max(p.activeBytes-startSize, 0)
		unused := int64(len(encoded)) - min(int64(len(encoded)), physicalGrowth)
		if unused > 0 {
			p.accounted -= unused
			p.store.release(unused, false, false)
		}
		p.poisonLocked(writeErr)
		return 0, fmt.Errorf("%w: append record: %w", streamspool.ErrUnavailable, writeErr)
	}
	offset := p.high
	p.high++
	p.activeBytes += int64(len(encoded))
	p.segments[len(p.segments)-1].bytes = p.activeBytes
	p.state = streamspool.StateOpen
	p.updatedAt = p.store.cfg.Now()
	p.signalLocked()
	return offset, nil
}

func (w *writer) Bounds(ctx context.Context) (streamspool.Bounds, error) {
	if w == nil || w.partition == nil {
		return streamspool.Bounds{}, streamspool.ErrUnavailable
	}
	return w.partition.bounds(ctx)
}

func (w *writer) FinishEmpty(ctx context.Context) error {
	if w == nil || w.partition == nil {
		return streamspool.ErrUnavailable
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	p := w.partition
	p.mu.Lock()
	if p.state != streamspool.StatePending || p.high != 0 {
		if p.state == streamspool.StateOpen {
			p.mu.Unlock()
			return nil
		}
		err := stateError(p.state)
		p.mu.Unlock()
		return err
	}
	p.state = streamspool.StateEmptyTerminal
	p.updatedAt = p.store.cfg.Now()
	p.finishWriterLocked()
	p.signalLocked()
	forget := p.readers == 0
	p.mu.Unlock()
	if forget {
		_ = p.store.removePartition(p, false)
	}
	return nil
}

func (w *writer) Seal(ctx context.Context) error {
	if w == nil || w.partition == nil {
		return streamspool.ErrUnavailable
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	p := w.partition
	p.mu.Lock()
	if p.state == streamspool.StateSealed {
		p.mu.Unlock()
		return nil
	}
	if p.state != streamspool.StatePending && p.state != streamspool.StateOpen {
		err := stateError(p.state)
		p.mu.Unlock()
		return err
	}
	if p.active != nil {
		if err := errors.Join(p.active.Sync(), p.active.Close()); err != nil {
			p.active = nil
			p.poisonLocked(err)
			p.mu.Unlock()
			return fmt.Errorf("%w: seal: %w", streamspool.ErrUnavailable, err)
		}
		p.active = nil
	}
	if p.high == 0 {
		p.state = streamspool.StateEmptyTerminal
	} else {
		p.state = streamspool.StateSealed
	}
	p.updatedAt = p.store.cfg.Now()
	p.finishWriterLocked()
	p.signalLocked()
	forget := p.state == streamspool.StateEmptyTerminal && p.readers == 0
	p.mu.Unlock()
	if forget {
		_ = p.store.removePartition(p, false)
	}
	return nil
}

func (w *writer) Close() error {
	return nil
}

func (p *partition) rollLocked() error {
	if p.active != nil {
		if err := errors.Join(p.active.Sync(), p.active.Close()); err != nil {
			p.active = nil
			return fmt.Errorf("roll previous segment: %w", err)
		}
		p.active = nil
	}
	if err := secureManagedMkdirAll(p.store.fsroot, p.dir); err != nil {
		return err
	}
	path := filepath.Join(p.dir, segmentFilename(p.high))
	file, err := openExclusiveRegular(p.store.fsroot, path)
	if err != nil {
		return err
	}
	header := encodeSegmentHeader(p.key, p.originComplete, p.high, p.store.cfg.Now())
	n, err := file.Write(header)
	if err != nil || n != len(header) {
		if err == nil {
			err = errors.New("short segment header write")
		}
		_ = file.Close()
		_ = p.store.fsroot.Remove(path)
		return err
	}
	p.active = file
	p.activeBytes = int64(len(header))
	p.segments = append(p.segments, segment{base: p.high, path: path, bytes: p.activeBytes})
	return nil
}

func (p *partition) bounds(ctx context.Context) (streamspool.Bounds, error) {
	if p == nil {
		return streamspool.Bounds{}, streamspool.ErrNotFound
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return streamspool.Bounds{}, err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return streamspool.Bounds{Low: p.low, High: p.high, OriginComplete: p.originComplete, State: p.state}, nil
}

func (p *partition) signalLocked() {
	close(p.notify)
	p.notify = make(chan struct{})
}

func (p *partition) poisonLocked(_ error) {
	if p.state == streamspool.StatePoisoned || p.state == streamspool.StateStoreClosed {
		return
	}
	if p.active != nil {
		_ = p.active.Close()
		p.active = nil
	}
	p.state = streamspool.StatePoisoned
	p.updatedAt = p.store.cfg.Now()
	p.finishWriterLocked()
	p.signalLocked()
}

func (p *partition) finishWriterLocked() {
	if !p.writerActive {
		return
	}
	p.writerActive = false
	p.store.mu.Lock()
	if p.store.registrations > 0 {
		p.store.registrations--
	}
	p.store.mu.Unlock()
}

func (s *Store) reserve(bytes int64, newPartition, newSegment bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return streamspool.ErrClosed
	}
	if bytes < 0 || s.usedBytes > s.cfg.MaxBytes-bytes {
		return streamspool.ErrLimit
	}
	if newPartition && s.physicalParts >= s.cfg.MaxPartitions {
		return streamspool.ErrLimit
	}
	if newSegment && s.segmentCount >= s.cfg.MaxSegments {
		return streamspool.ErrLimit
	}
	s.usedBytes += bytes
	if newPartition {
		s.physicalParts++
	}
	if newSegment {
		s.segmentCount++
	}
	return nil
}

func (s *Store) release(bytes int64, partition, segment bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes > 0 {
		s.usedBytes -= min(s.usedBytes, bytes)
	}
	if partition && s.physicalParts > 0 {
		s.physicalParts--
	}
	if segment && s.segmentCount > 0 {
		s.segmentCount--
	}
}

func stateError(state streamspool.State) error {
	switch state {
	case streamspool.StateEmptyTerminal:
		return streamspool.ErrEmptyTerminal
	case streamspool.StateSealed:
		return io.EOF
	case streamspool.StateStoreClosed:
		return streamspool.ErrClosed
	default:
		return streamspool.ErrUnavailable
	}
}

var _ streamspool.Writer = (*writer)(nil)
