package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/fuse"
	"github.com/skills-fs/skills-fs/adapter/webdav"
	"github.com/skills-fs/skills-fs/adapter/websocket"
	"github.com/skills-fs/skills-fs/core"
)

const version = "0.1.0"

// Build metadata — set via ldflags at build time.
var (
	gitCommit = "unknown"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "webdav":
		os.Exit(cmdWebDAV(os.Args[2:]))
	case "websocket":
		os.Exit(cmdWebSocket(os.Args[2:]))
	case "fuse":
		os.Exit(cmdFUSE(os.Args[2:]))
	case "validate":
		os.Exit(cmdValidate(os.Args[2:]))
	case "version":
		printVersion(os.Stdout)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <command> [options]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  webdav    Start WebDAV server\n")
	fmt.Fprintf(os.Stderr, "  websocket Start WebSocket server\n")
	fmt.Fprintf(os.Stderr, "  fuse      Mount FUSE filesystem (Linux only)\n")
	fmt.Fprintf(os.Stderr, "  validate  Validate configuration file\n")
	fmt.Fprintf(os.Stderr, "  version   Print version\n")
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "skills-fs %s\n", version)
	fmt.Fprintf(w, "  git:    %s\n", gitCommit)
	fmt.Fprintf(w, "  built:  %s\n", buildTime)
	fmt.Fprintf(w, "  go:     %s\n", runtime.Version())
}

func setupLogger(level string, logFile string) *slog.Logger {
	var lw *os.File = os.Stderr
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			lw = f
		}
	}
	var sl slog.Level
	switch level {
	case "debug":
		sl = slog.LevelDebug
	case "warn":
		sl = slog.LevelWarn
	case "error":
		sl = slog.LevelError
	default:
		sl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(lw, &slog.HandlerOptions{Level: sl})
	return slog.New(h)
}

func maybeDaemonize(daemon bool, pidfile string) bool {
	if !daemon {
		if pidfile != "" {
			os.WriteFile(pidfile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)
		}
		return false
	}
	// Re-execute the same binary without -daemon, detached from terminal.
	args := make([]string, 0, len(os.Args))
	for _, a := range os.Args {
		if a == "-daemon" || a == "--daemon" {
			continue
		}
		args = append(args, a)
	}
	cmd, err := os.StartProcess(args[0], args, &os.ProcAttr{
		Files: []*os.File{nil, nil, nil},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: %v\n", err)
		os.Exit(1)
	}
	if pidfile != "" {
		os.WriteFile(pidfile, []byte(fmt.Sprintf("%d\n", cmd.Pid)), 0644)
	}
	fmt.Printf("daemon started pid=%d\n", cmd.Pid)
	return true
}

func buildFS(configPath string) (*core.FileSystem, error) {
	if configPath == "" {
		return core.NewFS(core.GlobalConfig{}), nil
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg.BuildFS()
}

func cmdWebDAV(args []string) int {
	fs := flag.NewFlagSet("webdav", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "WebDAV listen address")
	readOnly := fs.Bool("readonly", false, "Read-only mode")
	configPath := fs.String("config", "", "Path to JSON config file")
	logLevel := fs.String("log-level", "info", "Log level (debug, info, warn, error)")
	logFile := fs.String("log-file", "", "Path to log file")
	daemon := fs.Bool("daemon", false, "Run as background daemon")
	pidfile := fs.String("pidfile", "", "Path to PID file")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file")
	tlsKey := fs.String("tls-key", "", "TLS key file")
	corsOrigins := fs.String("cors-origins", "", "Comma-separated CORS origins (empty = allow all)")
	rateLimitRPS := fs.Float64("rate-limit-rps", 0, "Per-IP rate limit requests/sec (0 = unlimited)")
	rateLimitBurst := fs.Int("rate-limit-burst", 0, "Rate limit burst size (0 = match RPS)")
	enableGzip := fs.Bool("gzip", false, "Enable gzip compression")
	maxConns := fs.Int("max-connections", 0, "Max concurrent connections (0 = unlimited)")
	debug := fs.Bool("debug", false, "Enable /debug/pprof endpoints")
	shutdownTimeout := fs.Duration("shutdown-timeout", adapter.DefaultShutdownTimeout, "Graceful shutdown timeout")
	maxReqSize := fs.Int64("max-request-size", 0, "Max request body bytes (0 = 64MiB default)")
	maxRespSize := fs.Int64("max-response-size", 0, "Max response body bytes (0 = unlimited)")
	_ = fs.Parse(args)

	if maybeDaemonize(*daemon, *pidfile) {
		return 0
	}

	logger := setupLogger(*logLevel, *logFile)
	slog.SetDefault(logger)

	fsys, err := buildFS(*configPath)
	if err != nil {
		slog.Error("build fs", "err", err)
		return 1
	}

	var origins []string
	if *corsOrigins != "" {
		origins = strings.Split(*corsOrigins, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
	}

	opts := adapter.MountOptions{
		ReadOnly:        *readOnly,
		TLSCertFile:     *tlsCert,
		TLSKeyFile:      *tlsKey,
		AllowedOrigins:  origins,
		RateLimitRPS:    *rateLimitRPS,
		RateLimitBurst:  *rateLimitBurst,
		EnableGzip:      *enableGzip,
		CORSOrigins:     origins,
		MaxConnections:  *maxConns,
		Debug:           *debug,
		ShutdownTimeout: *shutdownTimeout,
		MaxRequestSize:  *maxReqSize,
		MaxResponseSize: *maxRespSize,
	}
	server := webdav.New(fsys, *addr, opts)
	if err := server.Mount(context.Background()); err != nil {
		slog.Error("mount", "err", err)
		return 1
	}
	slog.Info("webdav listening", "addr", server.Addr())

	if *configPath != "" {
		reloadCh := make(chan os.Signal, 1)
		signal.Notify(reloadCh, syscall.SIGHUP)
		go func() {
			for range reloadCh {
				cfg, err := LoadConfig(*configPath)
				if err != nil {
					slog.Error("reload config load", "err", err)
					continue
				}
				if err := cfg.Reload(fsys); err != nil {
					slog.Error("reload fs", "err", err)
					continue
				}
				slog.Info("config reloaded")
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down")
	if err := server.Unmount(context.Background()); err != nil {
		slog.Error("unmount", "err", err)
		return 1
	}
	fsys.Shutdown(context.Background())
	return 0
}

func cmdWebSocket(args []string) int {
	fs := flag.NewFlagSet("websocket", flag.ExitOnError)
	addr := fs.String("addr", ":8081", "WebSocket listen address")
	readOnly := fs.Bool("readonly", false, "Read-only mode")
	configPath := fs.String("config", "", "Path to JSON config file")
	logLevel := fs.String("log-level", "info", "Log level (debug, info, warn, error)")
	logFile := fs.String("log-file", "", "Path to log file")
	daemon := fs.Bool("daemon", false, "Run as background daemon")
	pidfile := fs.String("pidfile", "", "Path to PID file")
	allowedOrigins := fs.String("allowed-origins", "", "Comma-separated allowed origins (empty = allow all)")
	corsOrigins := fs.String("cors-origins", "", "Comma-separated CORS origins (empty = allow all)")
	maxConns := fs.Int("max-connections", 0, "Max concurrent connections (0 = unlimited)")
	debug := fs.Bool("debug", false, "Enable /debug/pprof endpoints")
	shutdownTimeout := fs.Duration("shutdown-timeout", adapter.DefaultShutdownTimeout, "Graceful shutdown timeout")
	_ = fs.Parse(args)

	if maybeDaemonize(*daemon, *pidfile) {
		return 0
	}

	logger := setupLogger(*logLevel, *logFile)
	slog.SetDefault(logger)

	fsys, err := buildFS(*configPath)
	if err != nil {
		slog.Error("build fs", "err", err)
		return 1
	}

	var origins []string
	if *allowedOrigins != "" {
		origins = strings.Split(*allowedOrigins, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
	}
	var cors []string
	if *corsOrigins != "" {
		cors = strings.Split(*corsOrigins, ",")
		for i := range cors {
			cors[i] = strings.TrimSpace(cors[i])
		}
	}

	opts := adapter.MountOptions{
		ReadOnly:        *readOnly,
		AllowedOrigins:  origins,
		CORSOrigins:     cors,
		MaxConnections:  *maxConns,
		Debug:           *debug,
		ShutdownTimeout: *shutdownTimeout,
	}
	server := websocket.New(fsys, *addr, opts)
	if err := server.Mount(context.Background()); err != nil {
		slog.Error("mount", "err", err)
		return 1
	}
	slog.Info("websocket listening", "addr", server.Addr())

	if *configPath != "" {
		reloadCh := make(chan os.Signal, 1)
		signal.Notify(reloadCh, syscall.SIGHUP)
		go func() {
			for range reloadCh {
				cfg, err := LoadConfig(*configPath)
				if err != nil {
					slog.Error("reload config load", "err", err)
					continue
				}
				if err := cfg.Reload(fsys); err != nil {
					slog.Error("reload fs", "err", err)
					continue
				}
				slog.Info("config reloaded")
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down")
	if err := server.Unmount(context.Background()); err != nil {
		slog.Error("unmount", "err", err)
		return 1
	}
	fsys.Shutdown(context.Background())
	return 0
}

func cmdFUSE(args []string) int {
	fs := flag.NewFlagSet("fuse", flag.ExitOnError)
	mountpoint := fs.String("mountpoint", "/mnt/skills", "FUSE mount point")
	allowOther := fs.Bool("allow-other", false, "Allow other users to access the mount")
	configPath := fs.String("config", "", "Path to JSON config file")
	logLevel := fs.String("log-level", "info", "Log level (debug, info, warn, error)")
	logFile := fs.String("log-file", "", "Path to log file")
	daemon := fs.Bool("daemon", false, "Run as background daemon")
	pidfile := fs.String("pidfile", "", "Path to PID file")
	_ = fs.Parse(args)

	if maybeDaemonize(*daemon, *pidfile) {
		return 0
	}

	logger := setupLogger(*logLevel, *logFile)
	slog.SetDefault(logger)

	fsys, err := buildFS(*configPath)
	if err != nil {
		slog.Error("build fs", "err", err)
		return 1
	}
	server := fuse.New(fsys, *mountpoint, adapter.MountOptions{AllowOther: *allowOther})
	if err := server.Mount(context.Background()); err != nil {
		slog.Error("mount", "err", err)
		return 1
	}
	slog.Info("fuse mounted", "path", server.MountPoint())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down")
	if err := server.Unmount(context.Background()); err != nil {
		slog.Error("unmount", "err", err)
		return 1
	}
	fsys.Shutdown(context.Background())
	return 0
}

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to JSON config file (required)")
	_ = fs.Parse(args)

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: skills-fs validate -config <path>")
		return 1
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load error: %v\n", err)
		return 1
	}

	fsys, err := cfg.BuildFS()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fs build error: %v\n", err)
		return 1
	}
	fsys.Shutdown(context.Background())

	fmt.Println("configuration valid")
	return 0
}
