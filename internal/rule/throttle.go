// file: internal/rule/throttle.go

package rule

import (
	"fmt"
	"sync"
	"time"
)

// ThrottleManager provides per-rule throttle state with fire-first semantics.
// The first message for a given key is allowed through immediately;
// subsequent messages within the window are suppressed.
// Thread-safe via a single mutex. State resets naturally on reload
// since Processor (and its ThrottleManager) is recreated.
type ThrottleManager struct {
	mu    sync.Mutex
	state map[string]time.Time
}

// NewThrottleManager creates a new ThrottleManager.
func NewThrottleManager() *ThrottleManager {
	return &ThrottleManager{
		state: make(map[string]time.Time),
	}
}

// Allow checks whether a message should be allowed through.
// Returns true if this is the first occurrence or the window has elapsed.
// Returns false if we're still within the suppression window.
func (tm *ThrottleManager) Allow(key string, window time.Duration) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	now := time.Now()
	if last, ok := tm.state[key]; ok && now.Sub(last) < window {
		return false
	}
	tm.state[key] = now
	return true
}

// ThrottleKey builds the composite key for throttle lookups.
// Format: "{phase}:{ruleIndex}:{resolvedKey}"
func ThrottleKey(phase string, ruleIndex int, resolvedKey string) string {
	return fmt.Sprintf("%s:%d:%s", phase, ruleIndex, resolvedKey)
}
