//go:build !windows

package job

import "context"

type Object struct{}

func New() (*Object, error)                                      { return &Object{}, nil }
func (j *Object) Terminate(uint32) error                         { return nil }
func (j *Object) WaitEmpty(context.Context) error                { return nil }
func (j *Object) TerminateAndWait(context.Context, uint32) error { return nil }
func (j *Object) Handle() uintptr                                { return 0 }
func (j *Object) Close() error                                   { return nil }
