package core

import (
	"errors"
	"fmt"
)

type Errno string

const (
	ENOENT    Errno = "ENOENT"
	EACCES    Errno = "EACCES"
	EEXIST    Errno = "EEXIST"
	EINVAL    Errno = "EINVAL"
	ECOMM     Errno = "ECOMM"
	ETIMEDOUT Errno = "ETIMEDOUT"
	ESTALE    Errno = "ESTALE"
	EPIPE     Errno = "EPIPE"
	EBUSY     Errno = "EBUSY"
	EIO       Errno = "EIO"
	ENOSYS    Errno = "ENOSYS"
	ENOTDIR   Errno = "ENOTDIR"
	EISDIR    Errno = "EISDIR"
)

type PosixError struct {
	Code Errno
	Op   OpCode
	Path string
	Err  error
}

func (e *PosixError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s %s %s", e.Code, e.Op, e.Path)
	}
	return fmt.Sprintf("%s %s %s: %v", e.Code, e.Op, e.Path, e.Err)
}

func (e *PosixError) Unwrap() error {
	return e.Err
}

func posix(code Errno, op OpCode, path string, err error) error {
	return &PosixError{Code: code, Op: op, Path: path, Err: err}
}

func IsCode(err error, code Errno) bool {
	var pe *PosixError
	return errors.As(err, &pe) && pe.Code == code
}

func MapProviderError(err error, op OpCode, path string) error {
	if err == nil {
		return nil
	}
	switch err.Error() {
	case "NOT_FOUND":
		return posix(ENOENT, op, path, err)
	case "PERMISSION_DENIED":
		return posix(EACCES, op, path, err)
	case "ALREADY_EXISTS":
		return posix(EEXIST, op, path, err)
	case "INVALID_ARGUMENT":
		return posix(EINVAL, op, path, err)
	case "UNAVAILABLE":
		return posix(ECOMM, op, path, err)
	case "TIMEOUT":
		return posix(ETIMEDOUT, op, path, err)
	case "STALE":
		return posix(ESTALE, op, path, err)
	case "PIPE":
		return posix(EPIPE, op, path, err)
	case "BUSY":
		return posix(EBUSY, op, path, err)
	case "IO_ERROR":
		return posix(EIO, op, path, err)
	case "NOT_SUPPORTED":
		return posix(ENOSYS, op, path, err)
	default:
		return posix(EIO, op, path, err)
	}
}
