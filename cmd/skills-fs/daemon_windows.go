//go:build windows

package main

import "fmt"

func startDaemon(args []string, pidfile string) (int, error) {
	return 0, fmt.Errorf("daemon mode not supported on Windows")
}
