package runnerproto

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	Version      = 1
	MaxFrameSize = 16 << 20
)

const (
	TypeHello      = "hello"
	TypeSpawn      = "spawn"
	TypeStdin      = "stdin"
	TypeStdinClose = "stdin_close"
	TypeResize     = "resize"
	TypeInterrupt  = "interrupt"
	TypeKill       = "kill"
	TypeStdout     = "stdout"
	TypeStderr     = "stderr"
	TypeExit       = "exit"
	TypeError      = "error"
)

type Frame struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Hello struct {
	RunnerVersion string   `json:"runner_version,omitempty"`
	Identity      string   `json:"identity,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

type Spawn struct {
	Command       string            `json:"command,omitempty"`
	CWD           string            `json:"cwd,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Timeout       time.Duration     `json:"timeout,omitempty"`
	TTY           bool              `json:"tty,omitempty"`
	Rows          int               `json:"rows,omitempty"`
	Cols          int               `json:"cols,omitempty"`
	StdinOpen     bool              `json:"stdin_open,omitempty"`
	ReadRoots     []string          `json:"read_roots,omitempty"`
	WriteRoots    []string          `json:"write_roots,omitempty"`
	DenyRead      []string          `json:"deny_read,omitempty"`
	DenyWrite     []string          `json:"deny_write,omitempty"`
	Network       string            `json:"network,omitempty"`
	FullAccess    bool              `json:"full_access,omitempty"`
	CapabilitySID []string          `json:"capability_sids,omitempty"`
}

type Bytes struct {
	Data []byte `json:"data,omitempty"`
}

type Resize struct {
	Rows int `json:"rows,omitempty"`
	Cols int `json:"cols,omitempty"`
}

type Exit struct {
	ExitCode int    `json:"exit_code"`
	Reason   string `json:"reason,omitempty"`
}

type Error struct {
	Phase   string `json:"phase,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func NewFrame(typ string, payload any) (Frame, error) {
	var raw json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return Frame{}, err
		}
		raw = data
	}
	return Frame{Version: Version, Type: typ, Payload: raw}, nil
}

func (f Frame) DecodePayload(out any) error {
	if out == nil || len(f.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(f.Payload, out)
}

type Writer struct {
	w io.Writer
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

func (w *Writer) WriteFrame(frame Frame) error {
	if w == nil || w.w == nil {
		return fmt.Errorf("runnerproto: writer is nil")
	}
	if frame.Version == 0 {
		frame.Version = Version
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameSize {
		return fmt.Errorf("runnerproto: frame too large: %d", len(data))
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.w.Write(data)
	return err
}

type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r)}
}

func (r *Reader) ReadFrame() (Frame, error) {
	if r == nil || r.r == nil {
		return Frame{}, fmt.Errorf("runnerproto: reader is nil")
	}
	var header [4]byte
	if _, err := io.ReadFull(r.r, header[:]); err != nil {
		return Frame{}, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameSize {
		return Frame{}, fmt.Errorf("runnerproto: invalid frame size %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r.r, data); err != nil {
		return Frame{}, err
	}
	var frame Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		return Frame{}, err
	}
	if frame.Version != Version {
		return Frame{}, fmt.Errorf("runnerproto: unsupported protocol version %d", frame.Version)
	}
	return frame, nil
}
