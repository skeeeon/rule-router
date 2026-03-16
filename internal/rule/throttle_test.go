// file: internal/rule/throttle_test.go

package rule

import (
	"sync"
	"testing"
	"time"
)

func TestThrottleManager_FirstMessageAllowed(t *testing.T) {
	tm := NewThrottleManager()
	if !tm.Allow("key1", 5*time.Second) {
		t.Fatal("first message should be allowed")
	}
}

func TestThrottleManager_SecondMessageSuppressed(t *testing.T) {
	tm := NewThrottleManager()
	tm.Allow("key1", 5*time.Second)
	if tm.Allow("key1", 5*time.Second) {
		t.Fatal("second message within window should be suppressed")
	}
}

func TestThrottleManager_WindowExpiry(t *testing.T) {
	tm := NewThrottleManager()
	window := 50 * time.Millisecond

	tm.Allow("key1", window)
	time.Sleep(60 * time.Millisecond)

	if !tm.Allow("key1", window) {
		t.Fatal("message after window expiry should be allowed")
	}
}

func TestThrottleManager_DifferentKeysIndependent(t *testing.T) {
	tm := NewThrottleManager()
	window := 5 * time.Second

	tm.Allow("key1", window)
	if !tm.Allow("key2", window) {
		t.Fatal("different key should be allowed independently")
	}
}

func TestThrottleManager_ConcurrentAccess(t *testing.T) {
	tm := NewThrottleManager()
	window := 5 * time.Second

	var wg sync.WaitGroup
	allowed := make([]bool, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			allowed[idx] = tm.Allow("shared-key", window)
		}(i)
	}
	wg.Wait()

	allowCount := 0
	for _, a := range allowed {
		if a {
			allowCount++
		}
	}
	if allowCount != 1 {
		t.Fatalf("expected exactly 1 allowed, got %d", allowCount)
	}
}

func TestThrottleKey(t *testing.T) {
	key := ThrottleKey("t", 3, "sensors.temperature.room1")
	expected := "t:3:sensors.temperature.room1"
	if key != expected {
		t.Fatalf("expected %q, got %q", expected, key)
	}
}
