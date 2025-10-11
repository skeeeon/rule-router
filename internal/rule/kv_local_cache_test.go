package rule

import (
	"sync"
	"testing"

	"rule-router/internal/logger"
)

// TestLocalKVCache_BasicOperations tests Get, Set, Delete operations
func TestLocalKVCache_BasicOperations(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	t.Run("set and get string value", func(t *testing.T) {
		cache.Set("bucket1", "key1", "value1")
		got, exists := cache.Get("bucket1", "key1")
		if !exists {
			t.Fatal("expected key to exist")
		}
		if got != "value1" {
			t.Errorf("got = %v, want value1", got)
		}
	})

	t.Run("set and get numeric value", func(t *testing.T) {
		cache.Set("bucket1", "key2", 42)
		got, exists := cache.Get("bucket1", "key2")
		if !exists {
			t.Fatal("expected key to exist")
		}
		if got != 42 {
			t.Errorf("got = %v, want 42", got)
		}
	})

	t.Run("set and get complex object", func(t *testing.T) {
		obj := map[string]interface{}{
			"field1": "value1",
			"field2": 123,
		}
		cache.Set("bucket1", "key3", obj)
		got, exists := cache.Get("bucket1", "key3")
		if !exists {
			t.Fatal("expected key to exist")
		}
		gotMap, ok := got.(map[string]interface{})
		if !ok {
			t.Fatal("expected map[string]interface{}")
		}
		if gotMap["field1"] != "value1" {
			t.Errorf("field1 = %v, want value1", gotMap["field1"])
		}
	})

	t.Run("get non-existent key", func(t *testing.T) {
		_, exists := cache.Get("bucket1", "missing")
		if exists {
			t.Error("expected key not to exist")
		}
	})

	t.Run("get from non-existent bucket", func(t *testing.T) {
		_, exists := cache.Get("missing-bucket", "key1")
		if exists {
			t.Error("expected key not to exist in missing bucket")
		}
	})

	t.Run("delete existing key", func(t *testing.T) {
		cache.Set("bucket2", "key1", "value1")
		cache.Delete("bucket2", "key1")
		_, exists := cache.Get("bucket2", "key1")
		if exists {
			t.Error("expected key to be deleted")
		}
	})

	t.Run("delete non-existent key", func(t *testing.T) {
		// Should not panic
		cache.Delete("bucket2", "missing")
	})
}

// TestLocalKVCache_EnableDisable tests cache enable/disable functionality
func TestLocalKVCache_EnableDisable(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	t.Run("initially enabled", func(t *testing.T) {
		if !cache.IsEnabled() {
			t.Error("cache should be enabled by default")
		}
	})

	t.Run("set when enabled", func(t *testing.T) {
		cache.SetEnabled(true)
		cache.Set("bucket1", "key1", "value1")
		_, exists := cache.Get("bucket1", "key1")
		if !exists {
			t.Error("expected key to exist when cache enabled")
		}
	})

	t.Run("set when disabled does nothing", func(t *testing.T) {
		cache.SetEnabled(false)
		cache.Set("bucket1", "key2", "value2")
		// Should return cache miss
		_, exists := cache.Get("bucket1", "key2")
		if exists {
			t.Error("expected cache miss when disabled")
		}
	})

	t.Run("get when disabled returns false", func(t *testing.T) {
		cache.SetEnabled(true)
		cache.Set("bucket1", "key3", "value3")
		cache.SetEnabled(false)
		_, exists := cache.Get("bucket1", "key3")
		if exists {
			t.Error("expected cache miss when disabled")
		}
	})

	t.Run("re-enable and access previous data", func(t *testing.T) {
		cache.SetEnabled(true)
		// key1 should still exist from earlier
		_, exists := cache.Get("bucket1", "key1")
		if !exists {
			t.Error("expected previously cached data to still exist")
		}
	})
}

// TestLocalKVCache_MultipleBuckets tests operations across multiple buckets
func TestLocalKVCache_MultipleBuckets(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	buckets := []string{"bucket1", "bucket2", "bucket3"}
	for _, bucket := range buckets {
		cache.Set(bucket, "key1", bucket+"-value1")
		cache.Set(bucket, "key2", bucket+"-value2")
	}

	t.Run("get from correct bucket", func(t *testing.T) {
		for _, bucket := range buckets {
			got, exists := cache.Get(bucket, "key1")
			if !exists {
				t.Errorf("expected key1 to exist in %s", bucket)
			}
			want := bucket + "-value1"
			if got != want {
				t.Errorf("got = %v, want %v", got, want)
			}
		}
	})

	t.Run("delete from one bucket doesn't affect others", func(t *testing.T) {
		cache.Delete("bucket1", "key1")
		
		_, exists := cache.Get("bucket1", "key1")
		if exists {
			t.Error("expected key1 to be deleted from bucket1")
		}
		
		_, exists = cache.Get("bucket2", "key1")
		if !exists {
			t.Error("expected key1 to still exist in bucket2")
		}
	})
}

// TestLocalKVCache_GetStats tests statistics collection
func TestLocalKVCache_GetStats(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	t.Run("empty cache stats", func(t *testing.T) {
		stats := cache.GetStats()
		if stats["enabled"] != true {
			t.Error("expected enabled to be true")
		}
		if stats["total_keys"] != 0 {
			t.Errorf("total_keys = %v, want 0", stats["total_keys"])
		}
		if stats["bucket_count"] != 0 {
			t.Errorf("bucket_count = %v, want 0", stats["bucket_count"])
		}
	})

	t.Run("stats after adding data", func(t *testing.T) {
		cache.Set("bucket1", "key1", "value1")
		cache.Set("bucket1", "key2", "value2")
		cache.Set("bucket2", "key1", "value1")

		stats := cache.GetStats()
		if stats["total_keys"] != 3 {
			t.Errorf("total_keys = %v, want 3", stats["total_keys"])
		}
		if stats["bucket_count"] != 2 {
			t.Errorf("bucket_count = %v, want 2", stats["bucket_count"])
		}

		buckets, ok := stats["buckets"].(map[string]int)
		if !ok {
			t.Fatal("expected buckets to be map[string]int")
		}
		if buckets["bucket1"] != 2 {
			t.Errorf("bucket1 keys = %v, want 2", buckets["bucket1"])
		}
		if buckets["bucket2"] != 1 {
			t.Errorf("bucket2 keys = %v, want 1", buckets["bucket2"])
		}
	})

	t.Run("stats when disabled", func(t *testing.T) {
		cache.SetEnabled(false)
		stats := cache.GetStats()
		if stats["enabled"] != false {
			t.Error("expected enabled to be false")
		}
		// Total keys should still reflect what's in memory
		if stats["total_keys"] != 3 {
			t.Errorf("total_keys = %v, want 3", stats["total_keys"])
		}
	})
}

// TestLocalKVCache_GetAllKeys tests key enumeration
func TestLocalKVCache_GetAllKeys(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	cache.Set("bucket1", "key1", "value1")
	cache.Set("bucket1", "key2", "value2")
	cache.Set("bucket1", "key3", "value3")

	keys := cache.GetAllKeys("bucket1")
	if len(keys) != 3 {
		t.Errorf("got %d keys, want 3", len(keys))
	}

	// Verify all keys are present
	keyMap := make(map[string]bool)
	for _, key := range keys {
		keyMap[key] = true
	}
	for _, expectedKey := range []string{"key1", "key2", "key3"} {
		if !keyMap[expectedKey] {
			t.Errorf("expected key %s not found", expectedKey)
		}
	}

	// Empty bucket should return empty slice
	emptyKeys := cache.GetAllKeys("nonexistent")
	if len(emptyKeys) != 0 {
		t.Errorf("expected empty slice for nonexistent bucket, got %d keys", len(emptyKeys))
	}
}

// TestLocalKVCache_GetAllBuckets tests bucket enumeration
func TestLocalKVCache_GetAllBuckets(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	buckets := []string{"bucket1", "bucket2", "bucket3"}
	for _, bucket := range buckets {
		cache.Set(bucket, "key1", "value1")
	}

	allBuckets := cache.GetAllBuckets()
	if len(allBuckets) != 3 {
		t.Errorf("got %d buckets, want 3", len(allBuckets))
	}

	bucketMap := make(map[string]bool)
	for _, bucket := range allBuckets {
		bucketMap[bucket] = true
	}
	for _, expectedBucket := range buckets {
		if !bucketMap[expectedBucket] {
			t.Errorf("expected bucket %s not found", expectedBucket)
		}
	}
}

// TestLocalKVCache_Clear tests cache clearing
func TestLocalKVCache_Clear(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	// Add data
	cache.Set("bucket1", "key1", "value1")
	cache.Set("bucket2", "key2", "value2")

	// Verify data exists
	_, exists := cache.Get("bucket1", "key1")
	if !exists {
		t.Fatal("expected data to exist before clear")
	}

	// Clear cache
	cache.Clear()

	// Verify data is gone
	_, exists = cache.Get("bucket1", "key1")
	if exists {
		t.Error("expected data to be cleared")
	}

	stats := cache.GetStats()
	if stats["total_keys"] != 0 {
		t.Errorf("total_keys = %v, want 0 after clear", stats["total_keys"])
	}
}

// TestLocalKVCache_NilValues tests handling of nil values
func TestLocalKVCache_NilValues(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	t.Run("set and get nil value", func(t *testing.T) {
		cache.Set("bucket1", "key1", nil)
		got, exists := cache.Get("bucket1", "key1")
		if !exists {
			t.Fatal("expected key to exist")
		}
		if got != nil {
			t.Errorf("got = %v, want nil", got)
		}
	})

	t.Run("nil is different from non-existent", func(t *testing.T) {
		cache.Set("bucket1", "nil-key", nil)
		_, exists := cache.Get("bucket1", "nil-key")
		if !exists {
			t.Error("expected nil-key to exist")
		}
		
		_, exists = cache.Get("bucket1", "missing-key")
		if exists {
			t.Error("expected missing-key to not exist")
		}
	})
}

// TestLocalKVCache_ConcurrentAccess tests thread safety
func TestLocalKVCache_ConcurrentAccess(t *testing.T) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	var wg sync.WaitGroup
	concurrency := 100
	iterations := 100

	// Concurrent writes
	t.Run("concurrent writes", func(t *testing.T) {
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					key := "key-" + string(rune(id))
					value := id * 1000 + j
					cache.Set("bucket1", key, value)
				}
			}(i)
		}
		wg.Wait()

		// Verify some data exists
		_, exists := cache.Get("bucket1", "key-"+string(rune(0)))
		if !exists {
			t.Error("expected data from concurrent writes to exist")
		}
	})

	// Concurrent reads
	t.Run("concurrent reads", func(t *testing.T) {
		cache.Set("shared", "key", "value")
		
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					cache.Get("shared", "key")
				}
			}()
		}
		wg.Wait()
	})

	// Mixed operations
	t.Run("concurrent mixed operations", func(t *testing.T) {
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				bucket := "bucket-mixed"
				key := "key-" + string(rune(id))
				for j := 0; j < iterations; j++ {
					switch j % 3 {
					case 0:
						cache.Set(bucket, key, j)
					case 1:
						cache.Get(bucket, key)
					case 2:
						if j%10 == 0 {
							cache.Delete(bucket, key)
						}
					}
				}
			}(i)
		}
		wg.Wait()
	})
}

// BenchmarkLocalKVCache_Get benchmarks cache read performance
func BenchmarkLocalKVCache_Get(b *testing.B) {
	cache := NewLocalKVCache(logger.NewNopLogger())
	cache.Set("benchmark", "key", "value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get("benchmark", "key")
	}
}

// BenchmarkLocalKVCache_Set benchmarks cache write performance
func BenchmarkLocalKVCache_Set(b *testing.B) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Set("benchmark", "key", i)
	}
}

// BenchmarkLocalKVCache_GetComplex benchmarks complex object retrieval
func BenchmarkLocalKVCache_GetComplex(b *testing.B) {
	cache := NewLocalKVCache(logger.NewNopLogger())
	complexObj := map[string]interface{}{
		"field1": "value1",
		"field2": 12345,
		"field3": map[string]interface{}{
			"nested1": "nested-value",
			"nested2": []interface{}{1, 2, 3, 4, 5},
		},
	}
	cache.Set("benchmark", "complex", complexObj)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get("benchmark", "complex")
	}
}

// BenchmarkLocalKVCache_GetStats benchmarks statistics collection
func BenchmarkLocalKVCache_GetStats(b *testing.B) {
	cache := NewLocalKVCache(logger.NewNopLogger())
	
	// Add some data
	for i := 0; i < 100; i++ {
		bucket := "bucket-" + string(rune(i%10))
		key := "key-" + string(rune(i))
		cache.Set(bucket, key, i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.GetStats()
	}
}

// BenchmarkLocalKVCache_ConcurrentReads benchmarks parallel read performance
func BenchmarkLocalKVCache_ConcurrentReads(b *testing.B) {
	cache := NewLocalKVCache(logger.NewNopLogger())
	cache.Set("benchmark", "key", "value")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("benchmark", "key")
		}
	})
}

// BenchmarkLocalKVCache_ConcurrentWrites benchmarks parallel write performance
func BenchmarkLocalKVCache_ConcurrentWrites(b *testing.B) {
	cache := NewLocalKVCache(logger.NewNopLogger())

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			cache.Set("benchmark", "key", i)
			i++
		}
	})
}

// BenchmarkLocalKVCache_MixedOperations benchmarks realistic workload
func BenchmarkLocalKVCache_MixedOperations(b *testing.B) {
	cache := NewLocalKVCache(logger.NewNopLogger())
	
	// Pre-populate
	for i := 0; i < 100; i++ {
		cache.Set("benchmark", "key-"+string(rune(i)), i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := "key-" + string(rune(i%100))
		switch i % 10 {
		case 0, 1, 2, 3, 4, 5, 6, 7: // 80% reads
			cache.Get("benchmark", key)
		case 8, 9: // 20% writes
			cache.Set("benchmark", key, i)
		}
	}
}
