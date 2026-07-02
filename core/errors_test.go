package core

import (
	"errors"
	"testing"
)

func TestPosixErrorStringWithoutErr(t *testing.T) {
	err := posix(ENOENT, OpRead, "/x", nil)
	if err.Error() != "ENOENT read /x" {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}

func TestMapProviderErrorTable(t *testing.T) {
	tests := map[string]Errno{
		"NOT_FOUND":         ENOENT,
		"PERMISSION_DENIED": EACCES,
		"ALREADY_EXISTS":    EEXIST,
		"INVALID_ARGUMENT":  EINVAL,
		"UNAVAILABLE":       ECOMM,
		"TIMEOUT":           ETIMEDOUT,
		"STALE":             ESTALE,
		"PIPE":              EPIPE,
		"BUSY":              EBUSY,
		"IO_ERROR":          EIO,
		"NOT_SUPPORTED":     ENOSYS,
		"UNKNOWN":           EIO,
	}
	for in, want := range tests {
		err := MapProviderError(errors.New(in), OpRead, "/x")
		if !IsCode(err, want) {
			t.Fatalf("%s mapped to %v, want %s", in, err, want)
		}
	}
}

func TestExtractErrno(t *testing.T) {
	if got := ExtractErrno(nil); got != "" {
		t.Fatalf("ExtractErrno(nil) = %q, want empty", got)
	}
	if got := ExtractErrno(errors.New("not posix")); got != EIO {
		t.Fatalf("ExtractErrno(non-posix) = %q, want EIO", got)
	}
	if got := ExtractErrno(posix(ENOENT, OpRead, "/x", nil)); got != ENOENT {
		t.Fatalf("ExtractErrno(ENOENT) = %q, want ENOENT", got)
	}
}

func TestHTTPStatusFromError(t *testing.T) {
	tests := []struct {
		code Errno
		want int
	}{
		{ENOENT, 404},
		{EACCES, 403},
		{EEXIST, 409},
		{EINVAL, 400},
		{ETIMEDOUT, 408},
		{EBUSY, 503},
		{EAGAIN, 503},
		{ENOSPC, 507},
		{ENOTDIR, 400},
		{EISDIR, 400},
		{ENOSYS, 501},
		{ELOOP, 400},
		{EIO, 500},
		{ECOMM, 500},
	}
	for _, tc := range tests {
		err := posix(tc.code, OpRead, "/x", nil)
		if got := HTTPStatusFromError(err); got != tc.want {
			t.Fatalf("HTTPStatusFromError(%s) = %d, want %d", tc.code, got, tc.want)
		}
	}
	// Non-posix error defaults to 500.
	if got := HTTPStatusFromError(errors.New("unknown")); got != 500 {
		t.Fatalf("HTTPStatusFromError(non-posix) = %d, want 500", got)
	}
}
