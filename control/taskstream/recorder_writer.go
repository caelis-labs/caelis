package taskstream

import (
	"context"
	"errors"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

// writeLoop is the only file writer for a Task. The queue mutex protects memory
// admission only, so a slow disk cannot hold an ACP callback or command lock.
func (r *Recorder) writeLoop(logical streamspool.LogicalKey, p *recordingPartition) {
	defer close(p.done)
	for {
		<-p.wake
		timer := time.NewTimer(taskOutputFlushInterval)
		for {
			p.mu.Lock()
			urgent := p.failure != nil || p.bytes >= taskOutputBatchBytes || p.released
			for _, item := range p.queue {
				urgent = urgent || item.replace || item.barrier != nil
			}
			p.mu.Unlock()
			if urgent {
				break
			}
			select {
			case <-timer.C:
				urgent = true
			case <-p.wake:
			}
			if urgent {
				break
			}
		}
		timer.Stop()
		for {
			p.mu.Lock()
			failure := p.failure
			if failure != nil {
				p.mu.Unlock()
				r.failWriter(p, failure)
				return
			}
			if len(p.queue) == 0 {
				p.mu.Unlock()
				break
			}
			count, bytes := 0, 0
			for _, item := range p.queue {
				if count > 0 && (item.replace || bytes+item.bytes > taskOutputBatchBytes || count >= maxDeliveryRecords) {
					break
				}
				count++
				bytes += item.bytes
				if item.replace || item.seal || item.barrier != nil {
					break
				}
			}
			items := append([]outputWrite(nil), p.queue[:count]...)
			clear(p.queue[:count])
			p.queue = p.queue[count:]
			p.mu.Unlock()

			var records []streamspool.Record
			sealed := false
			for _, item := range items {
				records = append(records, item.records...)
				sealed = sealed || item.seal
			}
			var err error
			if items[0].replace {
				err = r.replacePartition(p, logical, records, items[0].stream)
			} else {
				err = appendOutputRecords(p.writer, records)
				if err == nil && p.subagent {
					err = r.retainChildWindow(p, logical, records)
				}
			}
			if err == nil && sealed {
				err = p.writer.Seal(context.Background())
			}
			p.mu.Lock()
			p.bytes -= bytes
			p.records -= len(records)
			r.queuedBytes.Add(-int64(bytes))
			if err != nil && p.failure == nil {
				p.failure = err
			}
			failure = p.failure
			p.mu.Unlock()
			for _, item := range items {
				if item.barrier != nil {
					item.barrier <- failure
				}
			}
			if failure != nil {
				r.failWriter(p, failure)
				return
			}
			if sealed {
				r.forgetPartition(logical, p)
				return
			}
			// A small tail arriving during a write gets its own batching window.
			// Otherwise a steady producer could turn the drain loop into one
			// disk write per delta despite the timer at the outer boundary.
			p.mu.Lock()
			urgent := p.failure != nil || p.released || p.bytes >= taskOutputBatchBytes
			for _, item := range p.queue {
				urgent = urgent || item.replace || item.barrier != nil
			}
			if !urgent && len(p.queue) > 0 {
				signalOutputWriter(p)
			}
			p.mu.Unlock()
			if !urgent {
				break
			}
		}
	}
}

func appendOutputRecords(writer streamspool.Writer, records []streamspool.Record) error {
	for len(records) > 0 {
		n, bytes := 0, 0
		for _, record := range records {
			if n > 0 && (n >= maxDeliveryRecords || bytes+len(record.Payload) > taskOutputBatchBytes) {
				break
			}
			n++
			bytes += len(record.Payload)
		}
		if _, err := writer.AppendBatch(context.Background(), records[:n]); err != nil {
			return err
		}
		records = records[n:]
	}
	return nil
}

func (r *Recorder) replacePartition(p *recordingPartition, logical streamspool.LogicalKey, records []streamspool.Record, stream func(streamspool.Writer) error) error {
	ctx := context.Background()
	staged, err := r.store.Register(ctx, logical, streamspool.WriterOptions{OriginComplete: true, Unpublished: true})
	if err != nil {
		return err
	}
	discard := func() { _ = staged.Invalidate(ctx); _ = r.store.Remove(ctx, staged.Key()) }
	write := func() error {
		if stream != nil {
			return stream(staged)
		}
		return appendOutputRecords(staged, records)
	}
	if err := write(); err != nil {
		discard()
		return err
	}
	p.mu.Lock()
	failure := p.failure
	p.mu.Unlock()
	if failure != nil {
		discard()
		return failure
	}
	if err := r.store.Publish(ctx, staged.Key()); err != nil {
		discard()
		return err
	}
	_ = p.writer.Seal(ctx)
	_ = r.store.Remove(ctx, p.writer.Key())
	p.unpublished = false
	p.writer = staged
	p.writtenBytes = 0
	return nil
}

func (r *Recorder) failWriter(p *recordingPartition, err error) {
	_ = p.writer.Invalidate(context.Background())
	p.mu.Lock()
	if p.failure == nil {
		p.failure = err
	}
	pending := p.queue
	p.queue = nil
	r.queuedBytes.Add(-int64(p.bytes))
	p.bytes = 0
	p.records = 0
	p.mu.Unlock()
	for _, item := range pending {
		if item.barrier != nil {
			item.barrier <- err
		}
	}
	if r.diagnostics != nil && !errors.Is(err, context.Canceled) {
		r.diagnostics.Warn("Control Task output cache has a gap", "error", err)
	}
}
