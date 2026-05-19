// Package fuse implements a Linux FUSE adapter using go-fuse/v2.
//
// On non-Linux platforms the package compiles to a stub that returns
// ErrNotImplemented for every operation.
package fuse
