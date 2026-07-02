// Package core implements the embedded virtual filesystem engine.
//
// It provides mount registration, radix-tree routing with path parameters,
// POSIX permission checks, provider dispatch, handle lifecycle management,
// advisory file locking, write buffering, stream ring buffers, event
// notifications, and Agent Skill directory generation.
//
// Provider result caching is available at two levels:
//   - Per-mount: set CapConfig.CacheTTL on a MountEntry's Ops entries.
//     The filesystem caches results keyed by (providerID, action, params).
//   - Provider-level: wrap a Provider with provider/cache.New to cache
//     all Invoke results keyed by (action, params), regardless of mount.
//
// When both are active, the per-mount cache is checked first. See
// provider/cache/doc.go for the full caching architecture.
package core
