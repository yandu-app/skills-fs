package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Errno represents a POSIX error code returned by filesystem operations.
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
	EAGAIN    Errno = "EAGAIN"
	ENOSPC    Errno = "ENOSPC"
	ELOOP     Errno = "ELOOP"
)

// PosixError is the error type returned by all filesystem operations.
// Use IsCode to check for specific error codes.
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

// IsCode reports whether err wraps a PosixError with the given code.
func IsCode(err error, code Errno) bool {
	var pe *PosixError
	return errors.As(err, &pe) && pe.Code == code
}

func isPosix(err error) bool {
	var pe *PosixError
	return errors.As(err, &pe)
}

// ProviderError is the interface that provider-returned errors may implement
// to expose a structured error code. When a provider error implements this
// interface, MapProviderError maps the code directly instead of relying on
// string matching.
type ProviderError interface {
	error
	Code() string
}

// MapProviderError converts a provider-returned error into a PosixError.
// If the error implements ProviderError, its code is mapped to the
// corresponding Errno. Otherwise the error message is scanned for known
// provider error strings (both exact and substring matches).
func MapProviderError(err error, op OpCode, path string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return posix(ETIMEDOUT, op, path, err)
	}
	// Fast path: structured ProviderError interface.
	var pe ProviderError
	if errors.As(err, &pe) {
		return posix(providerCodeToErrno(pe.Code()), op, path, err)
	}
	// Fallback: scan the error message for known provider error strings.
	msg := err.Error()
	code := scanProviderErrorCode(msg)
	return posix(code, op, path, err)
}

// providerCodeToErrno maps a provider error code string to an Errno.
func providerCodeToErrno(code string) Errno {
	switch code {
	case "NOT_FOUND":
		return ENOENT
	case "PERMISSION_DENIED":
		return EACCES
	case "ALREADY_EXISTS":
		return EEXIST
	case "INVALID_ARGUMENT":
		return EINVAL
	case "UNAVAILABLE":
		return ECOMM
	case "TIMEOUT":
		return ETIMEDOUT
	case "STALE":
		return ESTALE
	case "PIPE":
		return EPIPE
	case "BUSY":
		return EBUSY
	case "IO_ERROR":
		return EIO
	case "NOT_SUPPORTED":
		return ENOSYS
	default:
		return EIO
	}
}

// scanProviderErrorCode scans the error message for known provider error
// strings. It checks for exact matches first, then falls back to substring
// matching for wrapped errors (e.g. "ipc provider p returned error: NOT_FOUND").
func scanProviderErrorCode(msg string) Errno {
	// Exact match (fast path for unwrapped errors).
	switch msg {
	case "NOT_FOUND":
		return ENOENT
	case "PERMISSION_DENIED":
		return EACCES
	case "ALREADY_EXISTS":
		return EEXIST
	case "INVALID_ARGUMENT":
		return EINVAL
	case "UNAVAILABLE":
		return ECOMM
	case "TIMEOUT":
		return ETIMEDOUT
	case "STALE":
		return ESTALE
	case "PIPE":
		return EPIPE
	case "BUSY":
		return EBUSY
	case "IO_ERROR":
		return EIO
	case "NOT_SUPPORTED":
		return ENOSYS
	}
	// Substring match for wrapped errors.
	// Order matters: longer strings first to avoid false positives
	// (e.g. "PERMISSION_DENIED" before "DENIED").
	for _, mapping := range []struct {
		substr string
		errno  Errno
	}{
		{"PERMISSION_DENIED", EACCES},
		{"ALREADY_EXISTS", EEXIST},
		{"INVALID_ARGUMENT", EINVAL},
		{"NOT_SUPPORTED", ENOSYS},
		{"NOT_FOUND", ENOENT},
		{"UNAVAILABLE", ECOMM},
		{"IO_ERROR", EIO},
		{"TIMEOUT", ETIMEDOUT},
		{"STALE", ESTALE},
		{"PIPE", EPIPE},
		{"BUSY", EBUSY},
	} {
		if strings.Contains(msg, mapping.substr) {
			return mapping.errno
		}
	}
	return EIO
}

// ExtractErrno returns the Errno from a PosixError, or EIO if the error
// is not a PosixError. This is the single source of truth for mapping
// core errors to errno codes; adapters should use this instead of
// duplicating their own switch statements.
func ExtractErrno(err error) Errno {
	if err == nil {
		return ""
	}
	var pe *PosixError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return EIO
}

// HTTPStatusFromError maps an error to the appropriate HTTP status code.
// Use this in HTTP-based adapters (WebDAV, WebSocket) instead of
// duplicating error-to-status mapping logic.
func HTTPStatusFromError(err error) int {
	switch ExtractErrno(err) {
	case ENOENT:
		return 404
	case EACCES:
		return 403
	case EEXIST:
		return 409
	case EINVAL:
		return 400
	case ETIMEDOUT:
		return 408
	case EBUSY:
		return 503
	case EAGAIN:
		return 503
	case ENOSPC:
		return 507
	case ENOTDIR:
		return 400
	case EISDIR:
		return 400
	case ENOSYS:
		return 501
	case ELOOP:
		return 400
	default:
		return 500
	}
}
