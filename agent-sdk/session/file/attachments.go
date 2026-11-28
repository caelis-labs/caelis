package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/google/uuid"
)

const attachmentHeaderSize = 40

// Bound serialization to an image shard and keep attachment I/O independent
// of Session event log readers. Offline maintenance also pins Host ownership.
var attachmentLocks [64]sync.Mutex

type attachmentHeader struct {
	instance uuid.UUID
	created  time.Time
	size     int64
	width    int
	height   int
}

func readAttachmentHeader(r io.Reader) (attachmentHeader, error) {
	var raw [attachmentHeaderSize]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return attachmentHeader{}, err
	}
	return attachmentHeader{
		instance: uuid.UUID(raw[:16]),
		created:  time.Unix(0, int64(binary.BigEndian.Uint64(raw[16:24]))),
		size:     int64(binary.BigEndian.Uint64(raw[24:32])),
		width:    int(binary.BigEndian.Uint32(raw[32:36])),
		height:   int(binary.BigEndian.Uint32(raw[36:40])),
	}, nil
}

func (h attachmentHeader) encode() []byte {
	var raw [attachmentHeaderSize]byte
	copy(raw[:], h.instance[:])
	binary.BigEndian.PutUint64(raw[16:24], uint64(h.created.UnixNano()))
	binary.BigEndian.PutUint64(raw[24:32], uint64(h.size))
	binary.BigEndian.PutUint32(raw[32:36], uint32(h.width))
	binary.BigEndian.PutUint32(raw[36:40], uint32(h.height))
	return raw[:]
}

func (s *Store) attachmentDir(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(s.rootDir, "attachments", hex.EncodeToString(sum[:]))
}

// StoreAttachment publishes source bytes in one atomically installed file.
func (s *Store) StoreAttachment(ctx context.Context, sessionID string, data []byte) (model.AttachmentRef, error) {
	var ref model.AttachmentRef
	if !s.attachments {
		return ref, nil
	}
	if err := ctx.Err(); err != nil {
		return ref, err
	}
	if sessionID == "" || len(data) == 0 {
		return ref, session.ErrInvalidEvent
	}
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])
	dir := s.attachmentDir(sessionID)
	path := filepath.Join(dir, key)
	lock := &attachmentLocks[digest[0]%byte(len(attachmentLocks))]
	lock.Lock()
	defer lock.Unlock()
	if f, err := os.Open(path); err == nil {
		h, headerErr := readAttachmentHeader(f)
		info, statErr := f.Stat()
		_ = f.Close()
		if headerErr != nil || statErr != nil || h.size != int64(len(data)) || info.Size() != h.size+attachmentHeaderSize {
			return ref, model.ErrAttachmentUnavailable
		}
		// A concurrent transaction must not commit a reference before the earlier
		// publication's directory sync succeeds.
		return h.ref(sessionID, key), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ref, err
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
	h := attachmentHeader{instance: uuid.New(), created: s.now(), size: int64(len(data)), width: cfg.Width, height: cfg.Height}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ref, err
	}
	f, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return ref, err
	}
	defer os.Remove(f.Name())
	_, headerErr := f.Write(h.encode())
	var writeErr error
	if headerErr == nil {
		_, writeErr = f.Write(data)
	}
	syncErr := s.durability.SyncFile(f)
	if err := errors.Join(headerErr, writeErr, syncErr, f.Close(), ctx.Err()); err != nil {
		return ref, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return ref, err
	}
	for _, parent := range []string{dir, filepath.Dir(dir), s.rootDir} {
		if err := s.durability.SyncDirectory(parent); err != nil {
			_ = os.Remove(path)
			return ref, err
		}
	}
	return h.ref(sessionID, key), nil
}

func (h attachmentHeader) ref(sessionID, digest string) model.AttachmentRef {
	return model.AttachmentRef{SessionID: sessionID, Digest: digest, Instance: h.instance.String(), Size: h.size, Width: h.width, Height: h.height}
}

// OpenAttachment checks ownership and incarnation without reading image bytes.
func (s *Store) OpenAttachment(ctx context.Context, sessionID string, ref model.AttachmentRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest, err := hex.DecodeString(ref.Digest)
	if err != nil || len(digest) != sha256.Size || sessionID == "" || sessionID != ref.SessionID {
		return nil, model.ErrAttachmentUnavailable
	}
	lock := &attachmentLocks[digest[0]%byte(len(attachmentLocks))]
	lock.Lock()
	defer lock.Unlock()
	f, err := os.Open(filepath.Join(s.attachmentDir(sessionID), ref.Digest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, model.ErrAttachmentUnavailable
	}
	if err != nil {
		return nil, err
	}
	h, headerErr := readAttachmentHeader(f)
	info, statErr := f.Stat()
	if headerErr != nil || statErr != nil || h.ref(sessionID, ref.Digest) != ref || info.Size() != h.size+attachmentHeaderSize {
		_ = f.Close()
		return nil, model.ErrAttachmentUnavailable
	}
	return f, nil
}

// AttachmentPrune reports the result of inspecting or pruning one Session.
type AttachmentPrune struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// PruneAttachments inspects one Session's attachments. Applying a prune requires
// exclusive Store ownership, including admission of publishers and readers.
func (s *Store) PruneAttachments(ctx context.Context, sessionID string, before time.Time, apply bool) (AttachmentPrune, error) {
	var result AttachmentPrune
	if sessionID == "" || before.IsZero() {
		return result, session.ErrInvalidSession
	}
	dir := s.attachmentDir(sessionID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		digest, err := hex.DecodeString(entry.Name())
		if err != nil || len(digest) != sha256.Size || !entry.Type().IsRegular() {
			continue
		}
		lock := &attachmentLocks[digest[0]%byte(len(attachmentLocks))]
		lock.Lock()
		removed, err := pruneAttachment(filepath.Join(dir, entry.Name()), before, apply)
		lock.Unlock()
		if err != nil {
			return result, err
		}
		if removed > 0 {
			result.Files++
			result.Bytes += removed
		}
	}
	if apply && result.Files != 0 {
		return result, s.durability.SyncDirectory(dir)
	}
	return result, nil
}

func pruneAttachment(path string, before time.Time, apply bool) (int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	h, err := readAttachmentHeader(f)
	if err := errors.Join(err, f.Close()); err != nil {
		return 0, err
	}
	if !h.created.Before(before) {
		return 0, nil
	}
	if apply {
		if err := os.Remove(path); err != nil {
			return 0, err
		}
	}
	return h.size, nil
}

func (s *Store) stagePreparedEventAttachments(ctx context.Context, id string, prepared *session.PreparedAppendEvents) error {
	if !s.attachments {
		return nil
	}
	var bySeq map[uint64]*session.Event
	for _, event := range prepared.Persisted {
		changed := false
		if event.Tool != nil && event.Tool.Result != nil {
			var err error
			changed, err = s.stageResultAttachments(ctx, id, event.Tool.Result)
			if err != nil {
				return err
			}
		}
		messageChanged, err := s.stageMessageAttachments(ctx, id, event.Message)
		if err != nil {
			return err
		}
		evidenceChanged := false
		if event.InputEvidence != nil {
			evidenceChanged, err = s.stageMessageAttachments(ctx, id, &event.InputEvidence.Message)
			if err != nil {
				return err
			}
		}
		if changed || messageChanged || evidenceChanged {
			if bySeq == nil {
				bySeq = make(map[uint64]*session.Event)
			}
			bySeq[event.Seq] = event
		}
	}
	if len(bySeq) != 0 {
		for i, event := range prepared.Events {
			if stored := bySeq[event.Seq]; stored != nil && stored.ID == event.ID {
				prepared.Events[i] = stored
			}
		}
	}
	return nil
}

func (s *Store) stageMessageAttachments(ctx context.Context, id string, msg *model.Message) (bool, error) {
	if msg == nil {
		return false, nil
	}
	changed := false
	err := model.MapImageParts(msg.Parts, func(m *model.MediaPart) error {
		switch m.Source.Kind {
		case model.MediaSourceAttachment:
			if m.Source.Attachment.SessionID != id {
				return model.ErrAttachmentUnavailable
			}
		case model.MediaSourceInline:
			data, err := base64.StdEncoding.DecodeString(m.Source.Data)
			if err != nil || len(data) == 0 {
				return fmt.Errorf("encode Session attachment: %w", session.ErrInvalidEvent)
			}
			ref, err := s.StoreAttachment(ctx, id, data)
			if err != nil {
				return err
			}
			m.Source = model.MediaSource{Kind: model.MediaSourceAttachment, Attachment: ref}
			changed = true
		}
		return nil
	})
	return changed, err
}

func (s *Store) stageResultAttachments(ctx context.Context, id string, result *tool.Result) (bool, error) {
	changed := false
	for i, block := range result.Content {
		image, ok := block.(tool.Image)
		if !ok {
			continue
		}
		if image.Attachment.Instance != "" {
			if image.Attachment.SessionID != id {
				return false, model.ErrAttachmentUnavailable
			}
			continue
		}
		ref, err := s.StoreAttachment(ctx, id, image.Data)
		if err != nil {
			return false, err
		}
		image.Data, image.Attachment = nil, ref
		result.Content[i] = image
		changed = true
	}
	return changed, nil
}
