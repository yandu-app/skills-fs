// Package cache implements a caching provider decorator that memoizes
// Invoke results with TTL-based invalidation.
//
// This is a provider-level cache. It wraps a single [core.Provider] and
// caches its Invoke results keyed by (action, params). Use it when you
// want to cache at the provider registration site, independent of any
// mount configuration.
//
// The filesystem also has a built-in per-mount cache in
// [core.FileSystem] (providerCache) that is keyed by (providerID,
// action, params) and driven by CapConfig.CacheTTL on each mount entry.
// When both layers are active, the filesystem cache is checked first;
// on a miss the provider (which may itself be wrapped in this cache) is
// called. The two layers use different key formats but both produce
// deterministic keys (SHA-256 and sorted-string respectively).
//
// Prefer the per-mount cache (CapConfig.CacheTTL) for most use cases.
// Use this package when you need provider-wide caching that applies
// regardless of mount configuration.
package cache
