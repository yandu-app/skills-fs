//go:build !windows

package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func setupConfigReload(configPath string, reload func() error) {
	if configPath == "" {
		return
	}
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)
	go func() {
		for range reloadCh {
			if err := reload(); err != nil {
				slog.Error("reload", "err", err)
				continue
			}
			slog.Info("config reloaded")
		}
	}()
}
