//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func startDaemon(args []string, pidfile string) (int, error) {
	cmd, err := os.StartProcess(args[0], args, &os.ProcAttr{
		Files: []*os.File{nil, nil, nil},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return 0, err
	}
	if pidfile != "" {
		os.WriteFile(pidfile, []byte(fmt.Sprintf("%d\n", cmd.Pid)), 0644)
	}
	return cmd.Pid, nil
}
