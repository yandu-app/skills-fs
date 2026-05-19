//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func startDaemon(args []string, pidfile string) (int, error) {
	// #nosec G702 G703 -- daemon intentionally re-executes itself with the same args.
	cmd, err := os.StartProcess(args[0], args, &os.ProcAttr{
		Files: []*os.File{nil, nil, nil},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return 0, err
	}
	if pidfile != "" {
		// #nosec G306 G703 -- PID files follow standard world-readable convention.
		os.WriteFile(pidfile, []byte(fmt.Sprintf("%d\n", cmd.Pid)), 0644)
	}
	return cmd.Pid, nil
}
