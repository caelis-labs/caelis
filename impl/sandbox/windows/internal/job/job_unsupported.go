//go:build !windows

package job

type Object struct{}

func New() (*Object, error)              { return &Object{}, nil }
func (j *Object) AssignPID(int) error    { return nil }
func (j *Object) Terminate(uint32) error { return nil }
func (j *Object) Close() error           { return nil }
