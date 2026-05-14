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
