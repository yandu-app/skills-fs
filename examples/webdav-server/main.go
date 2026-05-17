// webdav-server starts an in-memory WebDAV server on localhost:8080.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/webdav"
	"github.com/skills-fs/skills-fs/core"
)

func main() {
	fs := core.NewFS(core.GlobalConfig{})

	// Pre-seed a few files.
	caller := core.CallerIdentity{UID: 1000, GID: 1000}
	fs.Mount("/notes", core.MountEntry{Kind: core.KindDir, Mode: 0o755, UID: caller.UID, GID: caller.GID})
	fs.Mount("/notes/hello.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("Hello, WebDAV!")})

	server := webdav.New(fs, "127.0.0.1:8080", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		log.Fatal(err)
	}
	fmt.Println("WebDAV listening on", server.Addr())
	fmt.Println("Try: curl -T file.txt http://localhost:8080/notes/file.txt")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down...")
	server.Unmount(context.Background())
	fs.Shutdown(context.Background())
}
