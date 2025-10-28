package rule

        import (
        	"context"
        	"errors"
        	"reflect"
        	"testing"
        	"time"

        	"github.com/nats-io/nats.go/jetstream"
        	"rule-router/internal/logger"
        )

        // --- Mocks for NATS JetStream KeyValue Interface (Updated for nats.go v1.42.0+) ---

        // mockKeyValueEntry implements the jetstream.KeyValueEntry interface for testing.
        type mockKeyValueEntry struct {
        	bucket string
        	key    string
        	value  []byte
        }

        func (m *mockKeyValueEntry) Bucket() string                  { return m.bucket }
        func (m *mockKeyValueEntry) Key() string                     { return m.key }
        func (m *mockKeyValueEntry) Value() []byte                   { return m.value }
        func (m *mockKeyValueEntry) Revision() uint64                { return 1 }
        func (m *mockKeyValueEntry) Created() time.Time              { return time.Now() }
        func (m *mockKeyValueEntry) Delta() uint64                   { return 0 }
        func (m *mockKeyValueEntry) Operation() jetstream.KeyValueOp { return jetstream.KeyValuePut }

        // mockKeyValueStore implements the jetstream.KeyValue interface for testing.
        // This version is compatible with the modern nats.go JetStream API.
        type mockKeyValueStore struct {
        	bucketName string
        	store      map[string][]byte
        	fail       bool // Flag to simulate infrastructure errors
        }

        func newMockKVStore(name string) *mockKeyValueStore {
        	return &mockKeyValueStore{
        		bucketName: name,
        		store:      make(map[string][]byte),
        	}
        }

        // --- Implemented Methods (Used by KVContext) ---

        func (m *mockKeyValueStore) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
        	if m.fail {
        		return nil, errors.New("simulated NATS error")
        	}
        	value, exists := m.store[key]
        	if !exists {
        		return nil, jetstream.ErrKeyNotFound
        	}
        	return &mockKeyValueEntry{bucket: m.bucketName, key: key, value: value}, nil
        }

        func (m *mockKeyValueStore) Put(ctx context.Context, key string, value []byte) (uint64, error) {
        	m.store[key] = value
        	return 1, nil
        }

        func (m *mockKeyValueStore) Bucket() string { return m.bucketName }

        // --- Stub Methods (To satisfy the jetstream.KeyValue interface) ---

        func (m *mockKeyValueStore) Create(ctx context.Context, key string, value []byte, opts ...jetstream.KVCreateOpt) (uint64, error) {
        	if _, exists := m.store[key]; exists {
        		return 0, jetstream.ErrKeyExists
        	}
        	return m.Put(ctx, key, value)
        }

        func (m *mockKeyValueStore) Update(ctx context.Context, key string, value []byte, last uint64) (uint64, error) {
        	return m.Put(ctx, key, value)
        }

        func (m *mockKeyValueStore) PutString(ctx context.Context, key string, value string) (uint64, error) {
        	return m.Put(ctx, key, []byte(value))
        }

        func (m *mockKeyValueStore) GetRevision(context.Context, string, uint64) (jetstream.KeyValueEntry, error) {
        	return nil, errors.New("not implemented")
        }

        func (m *mockKeyValueStore) Delete(context.Context, string, ...jetstream.KVDeleteOpt) error {
        	return errors.New("not implemented")
        }

        // **FIX START**: Corrected the option type for Purge.
        func (m *mockKeyValueStore) Purge(context.Context, string, ...jetstream.KVDeleteOpt) error {
        	return errors.New("not implemented")
        }
        // **FIX END**

        func (m *mockKeyValueStore) Watch(context.Context, string, ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
        	return nil, errors.New("not implemented")
        }

        func (m *mockKeyValueStore) WatchAll(context.Context, ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
        	return nil, errors.New("not implemented")
        }

        func (m *mockKeyValueStore) WatchFiltered(context.Context, []string, ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
        	return nil, errors.New("not implemented")
        }

        func (m *mockKeyValueStore) Keys(context.Context, ...jetstream.WatchOpt) ([]string, error) {
        	return nil, errors.New("not implemented")
        }

        func (m *mockKeyValueStore) ListKeys(context.Context, ...jetstream.WatchOpt) (jetstream.KeyLister, error) {
        	return nil, errors.New("not implemented")
        }

        func (m *mockKeyValueStore) ListKeysFiltered(context.Context, ...string) (jetstream.KeyLister, error) {
        	return nil, errors.New("not implemented")
        }

        // **FIX START**: Corrected the option type for History.
        func (m *mockKeyValueStore) History(context.Context, string, ...jetstream.WatchOpt) ([]jetstream.KeyValueEntry, error) {
        	return nil, errors.New("not implemented")
        }
        // **FIX END**

        func (m *mockKeyValueStore) Status(context.Context) (jetstream.KeyValueStatus, error) {
        	return nil, errors.New("not implemented")
        }

        // **FIX START**: Corrected the option type for PurgeDeletes.
        func (m *mockKeyValueStore) PurgeDeletes(context.Context, ...jetstream.KVPurgeOpt) error {
        	return errors.New("not implemented")
        }
        // **FIX END**


        // --- Test Suite ---

        // setupTestKVContext creates a KVContext with mock stores and a local cache.
        func setupTestKVContext(t *testing.T) (*KVContext, *LocalKVCache) {
        	t.Helper()

        	// Mock NATS KV Stores
        	store1 := newMockKVStore("device_config")
        	store1.Put(context.Background(), "sensor-123", []byte(`{"threshold": 30, "location": "room-a"}`))
        	store1.Put(context.Background(), "sensor.with.dots", []byte(`{"threshold": 40}`))

        	store2 := newMockKVStore("customer_data")
        	store2.Put(context.Background(), "cust-abc", []byte(`{"tier": "premium", "profile": {"name": "Acme Corp"}}`))

        	stores := map[string]jetstream.KeyValue{
        		"device_config": store1,
        		"customer_data": store2,
        	}

        	// Local Cache
        	cache := NewLocalKVCache(logger.NewNopLogger())

        	// Pre-populate cache for some tests
        	cache.Set("device_config", "sensor-123", map[string]interface{}{"threshold": 30.0, "location": "room-a"})
        	cache.Set("customer_data", "cust-abc", map[string]interface{}{"tier": "premium", "profile": map[string]interface{}{"name": "Acme Corp"}})

        	return NewKVContext(stores, logger.NewNopLogger(), cache), cache
        }

        func TestNewKVContext(t *testing.T) {
        	t.Run("panics with nil logger", func(t *testing.T) {
        		defer func() {
        			if r := recover(); r == nil {
        				t.Error("Expected NewKVContext to panic with a nil logger, but it did not")
        			}
        		}()
        		NewKVContext(nil, nil, nil)
        	})

        	t.Run("initializes successfully", func(t *testing.T) {
        		kvCtx, _ := setupTestKVContext(t)
        		if kvCtx == nil {
        			t.Fatal("KVContext should not be nil")
        		}
        		if len(kvCtx.stores) != 2 {
        			t.Errorf("Expected 2 KV stores, got %d", len(kvCtx.stores))
        		}
        		if !kvCtx.localCache.IsEnabled() {
        			t.Error("Local cache should be enabled")
        		}
        	})
        }

        func TestParseKVFieldWithPath(t *testing.T) {
        	kvCtx, _ := setupTestKVContext(t)

        	tests := []struct {
        		name       string
        		field      string
        		wantBucket string
        		wantKey    string
        		wantPath   []string
        		wantErr    bool
        	}{
        		{"valid simple", "@kv.device_config.sensor-123:threshold", "device_config", "sensor-123", []string{"threshold"}, false},
        		{"valid deep path", "@kv.customer_data.cust-abc:profile.name", "customer_data", "cust-abc", []string{"profile", "name"}, false},
        		{"valid array index", "@kv.customer_data.cust-abc:addresses.0.city", "customer_data", "cust-abc", []string{"addresses", "0", "city"}, false},
        		{"valid key with dots", "@kv.device_config.sensor.temp.001:location", "device_config", "sensor.temp.001", []string{"location"}, false},
        		{"valid with variables", "@kv.device_config.{device_id}:settings.{pref}", "device_config", "{device_id}", []string{"settings", "{pref}"}, false},
        		{"invalid no prefix", "kv.device_config.sensor-123:threshold", "", "", nil, true},
        		{"invalid no colon", "@kv.device_config.sensor-123.threshold", "", "", nil, true},
        		{"invalid multiple colons", "@kv.device_config.sensor-123:path:extra", "", "", nil, true},
        		{"invalid no path", "@kv.device_config.sensor-123:", "", "", nil, true},
        		{"invalid no key", "@kv.device_config.:threshold", "", "", nil, true},
        		{"invalid no bucket", "@kv..sensor-123:threshold", "", "", nil, true},
        		{"invalid bucket/key format", "@kv.device_config:threshold", "", "", nil, true},
        	}

        	for _, tt := range tests {
        		t.Run(tt.name, func(t *testing.T) {
        			bucket, key, path, err := kvCtx.parseKVFieldWithPath(tt.field)
        			if (err != nil) != tt.wantErr {
        				t.Fatalf("parseKVFieldWithPath() error = %v, wantErr %v", err, tt.wantErr)
        			}
        			if !tt.wantErr {
        				if bucket != tt.wantBucket {
        					t.Errorf("bucket = %q, want %q", bucket, tt.wantBucket)
        				}
        				if key != tt.wantKey {
        					t.Errorf("key = %q, want %q", key, tt.wantKey)
        				}
        				if !reflect.DeepEqual(path, tt.wantPath) {
        					t.Errorf("path = %v, want %v", path, tt.wantPath)
        				}
        			}
        		})
        	}
        }

        func TestGetField_Cache(t *testing.T) {
        	kvCtx, _ := setupTestKVContext(t)

        	t.Run("cache hit with path", func(t *testing.T) {
        		val, ok := kvCtx.GetField("@kv.device_config.sensor-123:threshold")
        		if !ok {
        			t.Fatal("Expected to find value in cache")
        		}
        		if val != 30.0 {
        			t.Errorf("got %v, want 30.0", val)
        		}
        	})

        	t.Run("cache hit with deep path", func(t *testing.T) {
        		val, ok := kvCtx.GetField("@kv.customer_data.cust-abc:profile.name")
        		if !ok {
        			t.Fatal("Expected to find value in cache")
        		}
        		if val != "Acme Corp" {
        			t.Errorf("got %v, want 'Acme Corp'", val)
        		}
        	})

        	t.Run("cache miss", func(t *testing.T) {
        		_, ok := kvCtx.GetField("@kv.device_config.non-existent-key:field")
        		if ok {
        			t.Error("Expected a cache miss, but found a value")
        		}
        	})

        	t.Run("cache hit with invalid path", func(t *testing.T) {
        		_, ok := kvCtx.GetField("@kv.device_config.sensor-123:invalid.path")
        		if ok {
        			t.Error("Expected lookup to fail for invalid path on cached item")
        		}
        	})

        	t.Run("cache disabled", func(t *testing.T) {
        		kvCtx.localCache.SetEnabled(false)
        		defer kvCtx.localCache.SetEnabled(true) // Restore for other tests

        		// This should now be a cache miss and fall back to NATS (which will succeed)
        		val, ok := kvCtx.GetField("@kv.device_config.sensor-123:threshold")
        		if !ok {
        			t.Fatal("Expected to find value via NATS fallback")
        		}
        		if val.(float64) != 30 {
        			t.Errorf("got %v, want 30", val)
        		}
        	})
        }

        func TestGetField_NATSFallback(t *testing.T) {
        	t.Run("successful nats lookup", func(t *testing.T) {
        		kvCtx, cache := setupTestKVContext(t)
        		cache.Clear() // Isolate this test

        		val, ok := kvCtx.GetField("@kv.device_config.sensor-123:location")
        		if !ok {
        			t.Fatal("Expected to find value via NATS")
        		}
        		if val != "room-a" {
        			t.Errorf("got %v, want 'room-a'", val)
        		}
        	})

        	t.Run("key not found in nats", func(t *testing.T) {
        		kvCtx, cache := setupTestKVContext(t)
        		cache.Clear() // Isolate this test

        		_, ok := kvCtx.GetField("@kv.device_config.not-a-sensor:field")
        		if ok {
        			t.Error("Expected lookup to fail for non-existent key")
        		}
        	})

        	t.Run("bucket not configured", func(t *testing.T) {
        		kvCtx, cache := setupTestKVContext(t)
        		cache.Clear() // Isolate this test

        		_, ok := kvCtx.GetField("@kv.non_existent_bucket.key:field")
        		if ok {
        			t.Error("Expected lookup to fail for non-existent bucket")
        		}
        	})

        	t.Run("nats infrastructure error", func(t *testing.T) {
        		kvCtx, cache := setupTestKVContext(t)
        		cache.Clear() // Isolate this test

        		// Get the mock store and set it to fail
        		mockStore := kvCtx.stores["device_config"].(*mockKeyValueStore)
        		mockStore.fail = true
        		defer func() { mockStore.fail = false }()

        		_, ok := kvCtx.GetField("@kv.device_config.sensor-123:location")
        		if ok {
        			t.Error("Expected lookup to fail on simulated NATS error")
        		}
        	})

        	t.Run("value is not valid json", func(t *testing.T) {
        		kvCtx, cache := setupTestKVContext(t)
        		cache.Clear() // Isolate this test

        		kvCtx.stores["device_config"].Put(context.Background(), "bad-json", []byte("not json"))
        		_, ok := kvCtx.GetField("@kv.device_config.bad-json:field")
        		if ok {
        			t.Error("Expected lookup to fail for non-JSON value")
        		}
        	})
        }

        func TestGetFieldWithContext_VariableResolution(t *testing.T) {
        	kvCtx, _ := setupTestKVContext(t)
        	msgData := map[string]interface{}{
        		"device_id": "sensor-123",
        		"customer":  "cust-abc",
        	}
        	timeCtx := NewSystemTimeProvider().GetCurrentContext()
        	subjectCtx := NewSubjectContext("events.device.sensor-123")

        	t.Run("resolve from message data", func(t *testing.T) {
        		val, ok := kvCtx.GetFieldWithContext("@kv.device_config.{device_id}:location", msgData, timeCtx, subjectCtx)
        		if !ok {
        			t.Fatal("Expected successful lookup with resolved variable")
        		}
        		if val != "room-a" {
        			t.Errorf("got %v, want 'room-a'", val)
        		}
        	})

        	t.Run("resolve from subject context", func(t *testing.T) {
        		val, ok := kvCtx.GetFieldWithContext("@kv.device_config.{@subject.2}:location", msgData, timeCtx, subjectCtx)
        		if !ok {
        			t.Fatal("Expected successful lookup with resolved subject token")
        		}
        		if val != "room-a" {
        			t.Errorf("got %v, want 'room-a'", val)
        		}
        	})

        	t.Run("unresolved variable returns false", func(t *testing.T) {
        		val, ok := kvCtx.GetFieldWithContext("@kv.device_config.{missing_var}:.location", msgData, timeCtx, subjectCtx)
        		if ok {
        			t.Errorf("Expected lookup to fail for unresolved variable, but got value: %v", val)
        		}
        		// It should return an empty string when it fails for template processing.
        		if val != "" {
        			t.Errorf("Expected empty string for unresolved variable, but got: %v", val)
        		}
        	})

        	t.Run("key with dots and variables", func(t *testing.T) {
        		msgDataWithDots := map[string]interface{}{"id": "sensor.with.dots"}
        		val, ok := kvCtx.GetFieldWithContext("@kv.device_config.{id}:threshold", msgDataWithDots, timeCtx, subjectCtx)
        		if !ok {
        			t.Fatal("Expected successful lookup for key with dots")
        		}
        		if val.(float64) != 40 {
        			t.Errorf("got %v, want 40", val)
        		}
        	})
        }
