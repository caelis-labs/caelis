package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/caelis-labs/caelis/control/streamspool"
)

type reader struct {
	partition *partition
	key       streamspool.Key
	offset    streamspool.Offset

	mu         sync.Mutex
	closed     bool
	closedCh   chan struct{}
	file       *os.File
	segment    string
	nextOffset streamspool.Offset
}

func (r *reader) Key() streamspool.Key {
	if r == nil {
		return streamspool.Key{}
	}
	return r.key
}

func (r *reader) Next(ctx context.Context) (streamspool.Record, error) {
	if r == nil || r.partition == nil {
		return streamspool.Record{}, streamspool.ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return streamspool.Record{}, streamspool.ErrClosed
		}
		offset := r.offset
		r.mu.Unlock()

		p := r.partition
		p.mu.Lock()
		if offset < p.low {
			p.mu.Unlock()
			return streamspool.Record{}, streamspool.ErrExpired
		}
		if offset < p.high {
			segments := append([]segment(nil), p.segments...)
			originComplete := p.originComplete
			p.mu.Unlock()
			record, err := r.readOffset(offset, segments, originComplete)
			if err != nil {
				return streamspool.Record{}, err
			}
			if record.Offset != offset {
				return streamspool.Record{}, fmt.Errorf("%w: got offset %d, want %d", streamspool.ErrCorrupt, record.Offset, offset)
			}
			r.mu.Lock()
			if !r.closed && r.offset == offset {
				r.offset++
			}
			r.mu.Unlock()
			return record, nil
		}
		state := p.state
		notify := p.notify
		p.mu.Unlock()
		switch state {
		case streamspool.StatePending, streamspool.StateOpen:
			select {
			case <-ctx.Done():
				return streamspool.Record{}, ctx.Err()
			case <-r.closedCh:
				return streamspool.Record{}, streamspool.ErrClosed
			case <-notify:
				continue
			}
		case streamspool.StateEmptyTerminal:
			return streamspool.Record{}, streamspool.ErrEmptyTerminal
		case streamspool.StateSealed:
			return streamspool.Record{}, io.EOF
		case streamspool.StateStoreClosed:
			return streamspool.Record{}, streamspool.ErrClosed
		default:
			return streamspool.Record{}, streamspool.ErrUnavailable
		}
	}
}

func (r *reader) readOffset(offset streamspool.Offset, segments []segment, originComplete bool) (streamspool.Record, error) {
	selected := -1
	for index := range segments {
		if segments[index].base > offset {
			break
		}
		selected = index
	}
	if selected < 0 {
		return streamspool.Record{}, fmt.Errorf("%w: no segment for offset %d", streamspool.ErrCorrupt, offset)
	}
	seg := segments[selected]
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return streamspool.Record{}, streamspool.ErrClosed
	}
	if r.file == nil || r.segment != seg.path || r.nextOffset > offset {
		if r.file != nil {
			_ = r.file.Close()
		}
		file, err := openReadOnlyRegular(r.partition.store.fsroot, seg.path)
		if err != nil {
			return streamspool.Record{}, err
		}
		if err := validateSegmentHeader(file, r.key, seg.base, originComplete); err != nil {
			_ = file.Close()
			return streamspool.Record{}, err
		}
		r.file = file
		r.segment = seg.path
		r.nextOffset = seg.base
	}
	for r.nextOffset < offset {
		skipped, err := decodeRecord(r.file, r.partition.store.cfg.MaxRecordBytes)
		if err != nil {
			return streamspool.Record{}, normalizeReadError(err)
		}
		if skipped.Offset != r.nextOffset {
			return streamspool.Record{}, fmt.Errorf("%w: segment offset discontinuity", streamspool.ErrCorrupt)
		}
		r.nextOffset++
	}
	record, err := decodeRecord(r.file, r.partition.store.cfg.MaxRecordBytes)
	if err != nil {
		return streamspool.Record{}, normalizeReadError(err)
	}
	if record.Offset != r.nextOffset {
		return streamspool.Record{}, fmt.Errorf("%w: segment offset discontinuity", streamspool.ErrCorrupt)
	}
	r.nextOffset++
	return record, nil
}

func normalizeReadError(err error) error {
	if err == nil {
		return nil
	}
	if isRecordEOF(err) {
		return fmt.Errorf("%w: incomplete record: %w", streamspool.ErrCorrupt, err)
	}
	if errors.Is(err, streamspool.ErrCorrupt) {
		return err
	}
	return fmt.Errorf("%w: %w", streamspool.ErrUnavailable, err)
}

func (r *reader) Close() error {
	if r == nil || r.partition == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	close(r.closedCh)
	var err error
	if r.file != nil {
		err = r.file.Close()
		r.file = nil
	}
	r.mu.Unlock()
	p := r.partition
	p.mu.Lock()
	if p.readers > 0 {
		p.readers--
	}
	forget := p.readers == 0 && !p.physical &&
		(p.state == streamspool.StateEmptyTerminal || p.state == streamspool.StatePoisoned)
	p.mu.Unlock()
	p.store.mu.Lock()
	if p.store.readerCount > 0 {
		p.store.readerCount--
	}
	p.store.mu.Unlock()
	if forget {
		_ = p.store.removePartition(p, false)
	}
	return err
}

var _ streamspool.Reader = (*reader)(nil)
