//go:build linux

package main

import "syscall"

func unmountBeforeStop(mountpoint string) {
	_ = syscall.Unmount(mountpoint, 0)
}
