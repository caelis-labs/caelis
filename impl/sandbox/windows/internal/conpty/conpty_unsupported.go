//go:build !windows

package conpty

import (
	"fmt"
	"io"
	"runtime"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
)

type Config struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
	Rows    int
	Cols    int
	Token   win32.Token
}

type Session struct{}

func Start(Config) (*Session, error) {
	return nil, fmt.Errorf("conpty: unsupported on %s", runtime.GOOS)
}

func (s *Session) PID() int {
	return 0
}

func (s *Session) Input() io.WriteCloser {
	return nil
}

func (s *Session) Output() io.Reader {
	return nil
}

func (s *Session) Resize(int, int) error {
	return nil
}

func (s *Session) Wait() (int, error) {
	return 0, nil
}

func (s *Session) Kill() error {
	return nil
}

func (s *Session) Close() error {
	return nil
}

func (s *Session) CloseConsole() error {
	return nil
}
