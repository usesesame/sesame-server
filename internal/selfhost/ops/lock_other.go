//go:build !unix

package ops

import "errors"

type Lock struct{}

func AcquireInstanceLock(string) (*Lock, error) {
	return nil, errors.New("ops: instance locking is only supported on unix systems")
}

func (l *Lock) Release() error { return nil }
