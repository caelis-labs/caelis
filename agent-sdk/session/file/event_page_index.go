package file

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	maxEventPageIndexes            = 64
	maxEventPageCheckpointsPerLog  = 256
	eventPageCheckpointVerifyChunk = 32 * 1024
)

// eventPageCheckpoint maps one durable source sequence to the byte immediately
// after its JSONL record. AnchorHash proves the record ending at Offset still
// matches before a cached seek skips any durable prefix.
type eventPageCheckpoint struct {
	Seq         uint64
	Offset      int64
	LineNo      int
	AnchorStart int64
	AnchorHash  [sha256.Size]byte
}

type eventPageIndex struct {
	info        os.FileInfo
	size        int64
	modTime     time.Time
	lastUsed    uint64
	checkpoints []eventPageCheckpoint
}

func (s *Store) prepareEventPageIndex(path string, info os.FileInfo) *eventPageIndex {
	if s.eventPageIndexes == nil {
		s.eventPageIndexes = map[string]*eventPageIndex{}
	}
	path = filepath.Clean(path)
	index := s.eventPageIndexes[path]
	if index == nil {
		if len(s.eventPageIndexes) >= maxEventPageIndexes {
			s.evictOldestEventPageIndex()
		}
		index = &eventPageIndex{}
		s.eventPageIndexes[path] = index
	} else if eventPageIndexFileChanged(index, info) {
		index.checkpoints = nil
	}
	s.eventPageIndexClock++
	index.lastUsed = s.eventPageIndexClock
	index.info = info
	index.size = info.Size()
	index.modTime = info.ModTime()
	return index
}

func eventPageIndexFileChanged(index *eventPageIndex, info os.FileInfo) bool {
	if index == nil || index.info == nil || info == nil {
		return index != nil && index.info != nil
	}
	if !os.SameFile(index.info, info) || info.Size() < index.size {
		return true
	}
	// An append grows the file and preserves every prior checkpoint. A changed
	// timestamp at the same size indicates truncate/rewrite or rollback.
	return info.Size() == index.size && !info.ModTime().Equal(index.modTime)
}

func (s *Store) evictOldestEventPageIndex() {
	var oldestPath string
	var oldestClock uint64
	for path, index := range s.eventPageIndexes {
		if oldestPath == "" || index.lastUsed < oldestClock {
			oldestPath = path
			oldestClock = index.lastUsed
		}
	}
	if oldestPath != "" {
		delete(s.eventPageIndexes, oldestPath)
	}
}

func (s *Store) eventPageStartCheckpoint(
	ctx context.Context,
	file *os.File,
	path string,
	info os.FileInfo,
	afterSeq uint64,
) (eventPageCheckpoint, error) {
	index := s.prepareEventPageIndex(path, info)
	position := sort.Search(len(index.checkpoints), func(i int) bool {
		return index.checkpoints[i].Seq > afterSeq
	})
	if position == 0 {
		return eventPageCheckpoint{}, nil
	}
	checkpoint := index.checkpoints[position-1]
	valid, err := validateEventPageCheckpoint(ctx, file, info.Size(), checkpoint)
	if err != nil {
		return eventPageCheckpoint{}, err
	}
	if !valid {
		index.checkpoints = nil
		return eventPageCheckpoint{}, nil
	}
	return checkpoint, nil
}

func validateEventPageCheckpoint(
	ctx context.Context,
	file *os.File,
	fileSize int64,
	checkpoint eventPageCheckpoint,
) (bool, error) {
	if checkpoint.Seq == 0 || checkpoint.Offset <= 0 || checkpoint.LineNo <= 0 ||
		checkpoint.AnchorStart < 0 || checkpoint.AnchorStart >= checkpoint.Offset || checkpoint.Offset > fileSize {
		return false, nil
	}
	hash := sha256.New()
	buffer := make([]byte, eventPageCheckpointVerifyChunk)
	position := checkpoint.AnchorStart
	for position < checkpoint.Offset {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		remaining := checkpoint.Offset - position
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, err := file.ReadAt(chunk, position)
		if n > 0 {
			_, _ = hash.Write(chunk[:n])
			position += int64(n)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if n == 0 {
			return false, nil
		}
	}
	var actual [sha256.Size]byte
	copy(actual[:], hash.Sum(nil))
	return actual == checkpoint.AnchorHash, nil
}

func (s *Store) recordEventPageCheckpoint(path string, checkpoint eventPageCheckpoint) {
	if checkpoint.Seq == 0 || checkpoint.Offset <= 0 || checkpoint.LineNo <= 0 {
		return
	}
	index := s.eventPageIndexes[filepath.Clean(path)]
	if index == nil {
		return
	}
	position := sort.Search(len(index.checkpoints), func(i int) bool {
		return index.checkpoints[i].Seq >= checkpoint.Seq
	})
	if position < len(index.checkpoints) && index.checkpoints[position].Seq == checkpoint.Seq {
		index.checkpoints[position] = checkpoint
		return
	}
	index.checkpoints = append(index.checkpoints, eventPageCheckpoint{})
	copy(index.checkpoints[position+1:], index.checkpoints[position:])
	index.checkpoints[position] = checkpoint
	if len(index.checkpoints) > maxEventPageCheckpointsPerLog {
		overflow := len(index.checkpoints) - maxEventPageCheckpointsPerLog
		copy(index.checkpoints, index.checkpoints[overflow:])
		index.checkpoints = index.checkpoints[:maxEventPageCheckpointsPerLog]
	}
}

func newEventPageCheckpoint(seq uint64, lineStart int64, offset int64, lineNo int, raw string) eventPageCheckpoint {
	return eventPageCheckpoint{
		Seq:         seq,
		Offset:      offset,
		LineNo:      lineNo,
		AnchorStart: lineStart,
		AnchorHash:  sha256.Sum256([]byte(raw)),
	}
}

func eventPageFileSnapshotChanged(before os.FileInfo, after os.FileInfo) bool {
	return before == nil || after == nil || !os.SameFile(before, after) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime())
}

func (s *Store) invalidateEventPageIndex(path string) {
	delete(s.eventPageIndexes, filepath.Clean(path))
}
