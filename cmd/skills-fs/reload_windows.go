//go:build windows

package main

func setupConfigReload(configPath string, reload func() error) {
	// SIGHUP is not available on Windows; config reload via signal is unsupported.
}
