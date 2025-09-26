//file: internal/rule/kv_local_cache.go

package rule

import (
	"sync"
	"rule-router/internal/logger"
)

// LocalKVCache provides in-memory caching for NATS KV buckets
// Uses simple map structure for fast access and easy debugging
type LocalKVCache struct {
	cache   map[string]map[string]interface{} // bucket -> key -> parsed JSON value
	mu      sync.RWMutex                      // Simple read-write mutex for concurrent access
	logger  *logger.Logger                    // For debugging and monitoring
	enabled bool                              // Feature flag for easy disable
}

// NewLocalKVCache creates a new local KV cache instance
func NewLocalKVCache(logger *logger.Logger) *LocalKVCache {
	if logger == nil {
		// Defensive programming - should never happen but be safe
		panic("LocalKVCache requires a logger")
	}

	cache := &LocalKVCache{
		cache:   make(map[string]map[string]interface{}),
		logger:  logger,
		enabled: true,
	}

	logger.Info("local KV cache initialized", "enabled", cache.enabled)
	return cache
}

// Get retrieves a value from the local cache
// Returns the value and whether it was found
func (c *LocalKVCache) Get(bucket, key string) (interface{}, bool) {
	if !c.enabled {
		c.logger.Debug("local cache disabled, returning cache miss", "bucket", bucket, "key", key)
		return nil, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if bucketData, exists := c.cache[bucket]; exists {
		if value, exists := bucketData[key]; exists {
			c.logger.Debug("cache hit", "bucket", bucket, "key", key)
			return value, true
		}
	}

	c.logger.Debug("cache miss", "bucket", bucket, "key", key)
	return nil, false
}

// Set stores a value in the local cache
// Value should be the parsed JSON object, not raw bytes
func (c *LocalKVCache) Set(bucket, key string, value interface{}) {
	if !c.enabled {
		c.logger.Debug("local cache disabled, skipping set", "bucket", bucket, "key", key)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Initialize bucket map if it doesn't exist
	if c.cache[bucket] == nil {
		c.cache[bucket] = make(map[string]interface{})
	}

	c.cache[bucket][key] = value
	c.logger.Debug("cache updated", "bucket", bucket, "key", key)
}

// Delete removes a value from the local cache
// Used when KV stream indicates a key was deleted
func (c *LocalKVCache) Delete(bucket, key string) {
	if !c.enabled {
		c.logger.Debug("local cache disabled, skipping delete", "bucket", bucket, "key", key)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if bucketData, exists := c.cache[bucket]; exists {
		delete(bucketData, key)
		c.logger.Debug("cache entry deleted", "bucket", bucket, "key", key)
	}
}

// SetEnabled enables or disables the cache
// Useful for runtime configuration or troubleshooting
func (c *LocalKVCache) SetEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.enabled = enabled
	c.logger.Info("local cache enabled status changed", "enabled", enabled)
}

// IsEnabled returns whether the cache is currently enabled
func (c *LocalKVCache) IsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// Clear removes all entries from the cache
// Useful for testing or manual cache invalidation
func (c *LocalKVCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	previousSize := len(c.cache)
	c.cache = make(map[string]map[string]interface{})
	c.logger.Info("cache cleared", "previousBuckets", previousSize)
}

// GetStats returns cache statistics for monitoring
func (c *LocalKVCache) GetStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := map[string]interface{}{
		"enabled": c.enabled,
		"buckets": make(map[string]int),
	}

	totalKeys := 0
	for bucket, keys := range c.cache {
		keyCount := len(keys)
		stats["buckets"].(map[string]int)[bucket] = keyCount
		totalKeys += keyCount
	}
	stats["total_keys"] = totalKeys
	stats["bucket_count"] = len(c.cache)

	return stats
}

// GetAllKeys returns all keys for a specific bucket
// Useful for debugging and monitoring
func (c *LocalKVCache) GetAllKeys(bucket string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if bucketData, exists := c.cache[bucket]; exists {
		keys := make([]string, 0, len(bucketData))
		for key := range bucketData {
			keys = append(keys, key)
		}
		return keys
	}

	return []string{}
}

// GetAllBuckets returns all bucket names in the cache
func (c *LocalKVCache) GetAllBuckets() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	buckets := make([]string, 0, len(c.cache))
	for bucket := range c.cache {
		buckets = append(buckets, bucket)
	}
	return buckets
}
