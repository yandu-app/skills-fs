# API Reference

This directory contains auto-generated API documentation for all public packages.

Generated with [gomarkdoc](https://github.com/princjef/gomarkdoc) from Go doc comments.

## Packages

| Package | Description |
|---------|-------------|
| [core](core.md) | Embedded filesystem API, mount registry, routing, providers, and streams |
| [adapter](adapter.md) | Adapter contracts shared by FUSE and WebDAV |
| [adapter/fuse](adapter_fuse.md) | Linux FUSE implementation |
| [adapter/webdav](adapter_webdav.md) | WebDAV server implementation |
| [adapter/websocket](adapter_websocket.md) | WebSocket streaming adapter |
| [provider/cache](provider_cache.md) | Caching provider wrapper |
| [provider/http](provider_http.md) | HTTP-backed provider |
| [provider/local](provider_local.md) | Local filesystem provider |

## Regenerating

```bash
make gen-docs
```

Do not edit `.md` files in this directory directly; they are overwritten on regeneration.
