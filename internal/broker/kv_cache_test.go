package broker

import (
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// newKVCacheTestBroker builds a broker whose KV stores are empty, so every
// watchKVBucket call fails with "bucket not found" before it touches a
// connection. That makes the failure paths reachable without a live NATS.
func newKVCacheTestBroker(buckets ...string) *NATSBroker {
	cfg := &config.Config{}
	cfg.KV.Enabled = true
	cfg.KV.LocalCache.Enabled = true
	for _, name := range buckets {
		cfg.KV.Buckets = append(cfg.KV.Buckets, config.KVBucketConfig{Name: name, KeyFilter: ">"})
	}

	log := logger.NewNop()
	return &NATSBroker{
		logger:       log,
		config:       cfg,
		kvStores:     map[string]jetstream.KeyValue{},
		localKVCache: rule.NewLocalKVCache(log),
	}
}

// TestInitializeKVCache_AllBucketsFail pins that a total watch failure is
// reported instead of logged and swallowed. The cache can never be populated
// after this — reconnect only cycles watchers that were established — so the
// caller has to learn it is running on direct KV reads.
func TestInitializeKVCache_AllBucketsFail(t *testing.T) {
	b := newKVCacheTestBroker("sensors", "config")

	err := b.InitializeKVCache()
	if err == nil {
		t.Fatal("InitializeKVCache() = nil, want an error when no bucket can be watched")
	}
	for _, want := range []string{"sensors", "config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name failed bucket %q, got %q", want, err)
		}
	}

	// Left enabled, the cache would be consulted on every lookup and always
	// miss, so it is switched off rather than kept as a permanent detour.
	if b.localKVCache.IsEnabled() {
		t.Error("local KV cache should be disabled when no bucket could be watched")
	}
}

// TestInitializeKVCache_NoBucketsConfigured guards the total-failure check
// against counting "nothing configured" as "everything failed".
func TestInitializeKVCache_NoBucketsConfigured(t *testing.T) {
	b := newKVCacheTestBroker()

	if err := b.InitializeKVCache(); err != nil {
		t.Fatalf("InitializeKVCache() with no buckets = %v, want nil", err)
	}
	if !b.localKVCache.IsEnabled() {
		t.Error("cache should stay enabled when there was simply nothing to watch")
	}
}

// TestInitializeKVCache_CacheDisabledInConfig verifies the configured-off path
// still short-circuits without reporting an error.
func TestInitializeKVCache_CacheDisabledInConfig(t *testing.T) {
	b := newKVCacheTestBroker("sensors")
	b.config.KV.LocalCache.Enabled = false

	if err := b.InitializeKVCache(); err != nil {
		t.Fatalf("InitializeKVCache() with cache disabled = %v, want nil", err)
	}
	if b.localKVCache.IsEnabled() {
		t.Error("local KV cache should be disabled when turned off in config")
	}
}
