// basic demonstrates core filesystem operations: mounting blobs, directories,
// symlinks, reading, writing, and listing entries.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/skills-fs/skills-fs/core"
)

func main() {
	fs := core.NewFS(core.GlobalConfig{})
	ctx := context.Background()
	caller := core.CallerIdentity{UID: 1000, GID: 1000}

	// Mount a blob.
	if err := fs.Mount("/hello", core.MountEntry{
		Kind:     core.KindBlob,
		Mode:     0o644,
		UID:      caller.UID,
		GID:      caller.GID,
		BlobData: []byte("world"),
	}); err != nil {
		log.Fatal(err)
	}

	// Mount a directory.
	if err := fs.Mount("/docs", core.MountEntry{
		Kind: core.KindDir,
		Mode: 0o755,
		UID:  caller.UID,
		GID:  caller.GID,
	}); err != nil {
		log.Fatal(err)
	}

	// Mount a symlink.
	if err := fs.Mount("/docs/link", core.MountEntry{
		Kind:     core.KindLink,
		Mode:     0o777,
		LinkPath: "/hello",
	}); err != nil {
		log.Fatal(err)
	}

	// Read the blob.
	data, err := fs.Read(ctx, "/hello", caller)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("/hello = %q\n", data)

	// Follow the symlink.
	target, err := fs.FollowLink("/docs/link")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("/docs/link -> %s\n", target)

	// List directory entries.
	entries, err := fs.Readdir("/docs", caller)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("/docs entries:")
	for _, e := range entries {
		fmt.Printf("  %s (kind=%v, mode=%o)\n", e.Name, e.Kind, e.Mode)
	}

	// Overwrite the blob.
	if err := fs.Write(ctx, "/hello", []byte("universe"), caller); err != nil {
		log.Fatal(err)
	}
	data, _ = fs.Read(ctx, "/hello", caller)
	fmt.Printf("/hello after write = %q\n", data)
}
