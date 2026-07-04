package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	case "stop":
		os.Exit(cmdStop(os.Args[2:]))
	case "validate":
		os.Exit(cmdValidate(os.Args[2:]))
	case "health":
		os.Exit(cmdHealth(os.Args[2:]))
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
	fmt.Fprintf(os.Stderr, "  stop      Stop a running daemon by pidfile\n")
	fmt.Fprintf(os.Stderr, "  validate  Validate configuration file\n")
	fmt.Fprintf(os.Stderr, "  health    Check server health endpoint\n")
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
		// #nosec G302 -- log files are intentionally world-readable for debugging.
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
			// #nosec G306 -- PID files follow standard world-readable convention.
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
	pid, err := startDaemon(args, pidfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("daemon started pid=%d\n", pid)
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

	setupConfigReload(*configPath, func() error {
		cfg, err := LoadConfig(*configPath)
		if err != nil {
			return err
		}
		return cfg.Reload(fsys)
	})

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

	setupConfigReload(*configPath, func() error {
		cfg, err := LoadConfig(*configPath)
		if err != nil {
			return err
		}
		return cfg.Reload(fsys)
	})

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
	fmt.Fprintln(os.Stderr, "DEBUG: mount succeeded, waiting for signal")
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

func cmdStop(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	pidfile := fs.String("pidfile", "", "Path to PID file")
	mountpoint := fs.String("mountpoint", "", "FUSE mount point to unmount first (Linux only)")
	_ = fs.Parse(args)

	if *pidfile == "" {
		fmt.Fprintln(os.Stderr, "Usage: skills-fs stop -pidfile <path> [-mountpoint <path>]")
		return 1
	}

	data, err := os.ReadFile(*pidfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read pidfile: %v\n", err)
		return 1
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse pidfile: %v\n", err)
		return 1
	}

	// For FUSE daemons, unmount the filesystem before sending SIGTERM so any
	// process currently blocked on a FUSE request is released before the
	// userspace daemon goes away. This prevents D-state stuck processes.
	if runtime.GOOS == "linux" && *mountpoint != "" {
		_ = syscall.Unmount(*mountpoint, 0)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "find process: %v\n", err)
		return 1
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "send SIGTERM: %v\n", err)
		return 1
	}

	// Wait briefly for graceful exit.
	for range 50 {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			fmt.Printf("stopped pid=%d\n", pid)
			_ = os.Remove(*pidfile)
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "pid %d did not exit within 5s; try -9 or check logs\n", pid)
	return 1
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

	if err := cfg.validateAgents(); err != nil {
		fmt.Fprintf(os.Stderr, "config validation error: %v\n", err)
		return 1
	}

	fmt.Println("configuration valid")
	return 0
}

// validateAgents ensures every dir/dynamic_dir mount has a corresponding
// AGENTS.md child mount explaining its purpose, unless explicitly disabled
// with "agents": false.
func (c *Config) validateAgents() error {
	agentPaths := make(map[string]bool)
	for _, mc := range c.Mounts {
		if mc.Kind == "blob" && strings.HasSuffix(mc.Path, "/AGENTS.md") {
			agentPaths[mc.Path] = true
		}
	}

	for _, mc := range c.Mounts {
		if mc.Kind != "dir" && mc.Kind != "dynamic_dir" {
			continue
		}
		if mc.Agents != nil && !*mc.Agents {
			continue
		}
		agentPath := strings.TrimSuffix(mc.Path, "/") + "/AGENTS.md"
		if !agentPaths[agentPath] {
			return fmt.Errorf("dir mount %q requires an %q child or `agents: false`", mc.Path, agentPath)
		}
	}
	return nil
}

func cmdHealth(args []string) int {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	addr := fs.String("addr", "http://localhost:8080", "Server address to probe")
	path := fs.String("path", "/healthz", "Health endpoint path")
	timeout := fs.Duration("timeout", 5*time.Second, "Request timeout")
	_ = fs.Parse(args)

	client := &http.Client{Timeout: *timeout}
	url := strings.TrimSuffix(*addr, "/") + *path
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health check failed: status %d\n", resp.StatusCode)
		return 1
	}
	fmt.Println("healthy")
	return 0
}
