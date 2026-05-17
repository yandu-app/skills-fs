package core

import (
	"testing"
	"time"
)

func TestGlobalConfigValidate(t *testing.T) {
	valid := GlobalConfig{MaxOpenHandles: 1}
	if err := valid.Validate(); err != nil {
		t.Fatalf("unexpected error for valid config: %v", err)
	}

	cases := []struct {
		name string
		cfg  GlobalConfig
	}{
		{"MaxOpenHandles", GlobalConfig{MaxOpenHandles: -1}},
		{"MaxMounts", GlobalConfig{MaxMounts: -1}},
		{"MaxBlobSize", GlobalConfig{MaxBlobSize: -1}},
		{"ZeroCopyThreshold", GlobalConfig{ZeroCopyThreshold: -1}},
		{"StatCacheTTL", GlobalConfig{StatCacheTTL: -1 * time.Second}},
		{"LockTimeout", GlobalConfig{LockTimeout: -1 * time.Second}},
		{"Breaker.FailureThreshold", GlobalConfig{Breaker: CircuitBreakerConfig{FailureThreshold: -1}}},
		{"Breaker.ResetTimeout", GlobalConfig{Breaker: CircuitBreakerConfig{ResetTimeout: -1 * time.Second}}},
		{"Breaker.HalfOpenMaxCalls", GlobalConfig{Breaker: CircuitBreakerConfig{HalfOpenMaxCalls: -1}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMountEntryValidate(t *testing.T) {
	valid := MountEntry{Kind: KindBlob, Mode: 0o644}
	if err := valid.Validate(); err != nil {
		t.Fatalf("unexpected error for valid entry: %v", err)
	}

	cases := []struct {
		name  string
		entry MountEntry
	}{
		{"invalid kind", MountEntry{Kind: "unknown"}},
		{"BlobData on dir", MountEntry{Kind: KindDir, BlobData: []byte("x")}},
		{"LinkPath on blob", MountEntry{Kind: KindBlob, LinkPath: "/x"}},
		{"Stream on blob", MountEntry{Kind: KindBlob, Stream: &StreamConfig{Capacity: 1}}},
		{"stream zero capacity", MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 0}}},
		{"stream negative chunk", MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 1, MaxChunkSize: -1}}},
		{"skill without name", MountEntry{Kind: KindDir, Skill: &SkillConfig{Enabled: true}}},
		{"invalid visibility", MountEntry{Kind: KindDir, Visibility: "secret"}},
		{"empty provider id", MountEntry{Kind: KindAPI, Ops: map[OpCode]*CapConfig{OpRead: {ProviderID: ""}}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.entry.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMountEntryValidateValidStream(t *testing.T) {
	entry := MountEntry{
		Kind: KindStream,
		Stream: &StreamConfig{
			Capacity:     8,
			Mode:         BackpressureBlock,
			MaxChunkSize: 1024,
		},
	}
	if err := entry.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
