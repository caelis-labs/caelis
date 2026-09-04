package file

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

var (
	segmentMagic = [8]byte{'C', 'A', 'E', 'L', 'S', 'P', '0', '1'}
	crcTable     = crc32.MakeTable(crc32.Castagnoli)
)

const (
	segmentHeaderSize  = int64(98)
	recordPrefixSize   = 4
	recordFixedBody    = 8 + 2 + 2 + 8 + 4
	recordChecksumSize = 4
)

type decodedHeader struct {
	key            streamspool.Key
	base           streamspool.Offset
	originComplete bool
}

func encodeSegmentHeader(key streamspool.Key, originComplete bool, base streamspool.Offset, createdAt time.Time) []byte {
	buf := make([]byte, segmentHeaderSize)
	copy(buf[0:8], segmentMagic[:])
	binary.BigEndian.PutUint16(buf[8:10], streamspool.FormatVersion)
	binary.BigEndian.PutUint16(buf[10:12], uint16(segmentHeaderSize))
	buf[12] = byte(key.Namespace)
	if originComplete {
		buf[13] = 1
	}
	copy(buf[14:46], key.Digest[:])
	copy(buf[46:62], key.Epoch[:])
	copy(buf[62:78], key.Incarnation[:])
	binary.BigEndian.PutUint64(buf[78:86], uint64(base))
	binary.BigEndian.PutUint64(buf[86:94], uint64(createdAt.UnixNano()))
	binary.BigEndian.PutUint32(buf[94:98], crc32.Checksum(buf[:94], crcTable))
	return buf
}

func decodeSegmentHeader(r io.Reader) (decodedHeader, error) {
	buf := make([]byte, segmentHeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return decodedHeader{}, fmt.Errorf("%w: segment header: %w", streamspool.ErrCorrupt, err)
	}
	if string(buf[:8]) != string(segmentMagic[:]) || binary.BigEndian.Uint16(buf[8:10]) != streamspool.FormatVersion || binary.BigEndian.Uint16(buf[10:12]) != uint16(segmentHeaderSize) {
		return decodedHeader{}, fmt.Errorf("%w: invalid segment header", streamspool.ErrCorrupt)
	}
	if got, want := binary.BigEndian.Uint32(buf[94:98]), crc32.Checksum(buf[:94], crcTable); got != want {
		return decodedHeader{}, fmt.Errorf("%w: segment header checksum", streamspool.ErrCorrupt)
	}
	var out decodedHeader
	out.key.Namespace = streamspool.Namespace(buf[12])
	out.originComplete = buf[13]&1 != 0
	copy(out.key.Digest[:], buf[14:46])
	copy(out.key.Epoch[:], buf[46:62])
	copy(out.key.Incarnation[:], buf[62:78])
	out.base = streamspool.Offset(binary.BigEndian.Uint64(buf[78:86]))
	if !out.key.Namespace.Valid() {
		return decodedHeader{}, fmt.Errorf("%w: invalid namespace", streamspool.ErrCorrupt)
	}
	return out, nil
}

func encodeRecord(offset streamspool.Offset, recordType uint16, occurredAt time.Time, payload []byte) ([]byte, error) {
	bodyLen := recordFixedBody + len(payload) + recordChecksumSize
	if bodyLen < recordFixedBody+recordChecksumSize || uint64(bodyLen) > uint64(^uint32(0)) {
		return nil, streamspool.ErrLimit
	}
	buf := make([]byte, recordPrefixSize+bodyLen)
	binary.BigEndian.PutUint32(buf[0:4], uint32(bodyLen))
	binary.BigEndian.PutUint64(buf[4:12], uint64(offset))
	binary.BigEndian.PutUint16(buf[12:14], recordType)
	// bytes 14:16 are reserved flags.
	binary.BigEndian.PutUint64(buf[16:24], uint64(occurredAt.UnixNano()))
	binary.BigEndian.PutUint32(buf[24:28], uint32(len(payload)))
	copy(buf[28:28+len(payload)], payload)
	checksumAt := len(buf) - recordChecksumSize
	binary.BigEndian.PutUint32(buf[checksumAt:], crc32.Checksum(buf[4:checksumAt], crcTable))
	return buf, nil
}

func decodeRecord(r io.Reader, maxPayload int) (streamspool.Record, error) {
	prefix := make([]byte, recordPrefixSize)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return streamspool.Record{}, err
	}
	bodyLen := int(binary.BigEndian.Uint32(prefix))
	if bodyLen < recordFixedBody+recordChecksumSize || bodyLen > maxPayload+recordFixedBody+recordChecksumSize {
		return streamspool.Record{}, fmt.Errorf("%w: invalid record length %d", streamspool.ErrCorrupt, bodyLen)
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return streamspool.Record{}, fmt.Errorf("%w: record body: %w", streamspool.ErrCorrupt, err)
	}
	payloadLen := int(binary.BigEndian.Uint32(body[20:24]))
	if payloadLen < 0 || payloadLen > maxPayload || recordFixedBody+payloadLen+recordChecksumSize != len(body) {
		return streamspool.Record{}, fmt.Errorf("%w: invalid payload length %d", streamspool.ErrCorrupt, payloadLen)
	}
	checksumAt := len(body) - recordChecksumSize
	if got, want := binary.BigEndian.Uint32(body[checksumAt:]), crc32.Checksum(body[:checksumAt], crcTable); got != want {
		return streamspool.Record{}, fmt.Errorf("%w: record checksum", streamspool.ErrCorrupt)
	}
	nanos := int64(binary.BigEndian.Uint64(body[12:20]))
	return streamspool.Record{
		Offset:     streamspool.Offset(binary.BigEndian.Uint64(body[0:8])),
		Type:       binary.BigEndian.Uint16(body[8:10]),
		OccurredAt: time.Unix(0, nanos),
		Payload:    append([]byte(nil), body[24:24+payloadLen]...),
	}, nil
}

func validateSegmentHeader(file *os.File, want streamspool.Key, base streamspool.Offset, originComplete bool) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	header, err := decodeSegmentHeader(file)
	if err != nil {
		return err
	}
	if header.key != want || header.base != base || header.originComplete != originComplete {
		return fmt.Errorf("%w: segment identity mismatch", streamspool.ErrCorrupt)
	}
	return nil
}

func isRecordEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
