package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/fuse"
	"github.com/skills-fs/skills-fs/adapter/webdav"
	"github.com/skills-fs/skills-fs/core"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "webdav":
		os.Exit(cmdWebDAV(os.Args[2:]))
	case "fuse":
		os.Exit(cmdFUSE(os.Args[2:]))
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <command> [options]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  webdav   Start WebDAV server\n")
	fmt.Fprintf(os.Stderr, "  fuse     Mount FUSE filesystem (Linux only)\n")
	fmt.Fprintf(os.Stderr, "  version  Print version\n")
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
	_ = fs.Parse(args)

	fsys, err := buildFS(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	server := webdav.New(fsys, *addr, adapter.MountOptions{ReadOnly: *readOnly})
	if err := server.Mount(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "mount: %v\n", err)
		return 1
	}
	fmt.Printf("WebDAV listening on %s\n", server.MountPoint())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("Shutting down...")
	if err := server.Unmount(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "unmount: %v\n", err)
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
	_ = fs.Parse(args)

	fsys, err := buildFS(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	server := fuse.New(fsys, *mountpoint, adapter.MountOptions{AllowOther: *allowOther})
	if err := server.Mount(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "mount: %v\n", err)
		return 1
	}
	fmt.Printf("FUSE mounted at %s\n", server.MountPoint())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("Shutting down...")
	if err := server.Unmount(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "unmount: %v\n", err)
		return 1
	}
	fsys.Shutdown(context.Background())
	return 0
}
