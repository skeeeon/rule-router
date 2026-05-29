// file: internal/rule/throttle.go

package rule

import (
	"fmt"
	"sync"
	"time"
)

// sweepInterval bounds how often Allow walks the state map to evict expired
// entries. Eviction is opportunistic (piggybacked on Allow) rather than run on
// a background goroutine, since the Processor — and this ThrottleManager — is
// recreated on reload and a goroutine would need separate lifecycle management.
const sweepInterval = 5 * time.Minute

// ThrottleManager provides per-rule throttle state with fire-first semantics.
// The first message for a given key is allowed through immediately;
// subsequent messages within the window are suppressed.
// Thread-safe via a single mutex. State resets naturally on reload
// since Processor (and its ThrottleManager) is recreated.
//
// Each entry stores the moment its suppression window expires. High-cardinality
// keys (e.g. templated by order/device ID) would otherwise accumulate forever,
// so Allow periodically sweeps and drops expired entries.
type ThrottleManager struct {
	mu        sync.Mutex
	expiry    map[string]time.Time
	lastSweep time.Time
}

// NewThrottleManager creates a new ThrottleManager.
func NewThrottleManager() *ThrottleManager {
	return &ThrottleManager{
		expiry:    make(map[string]time.Time),
		lastSweep: time.Now(),
	}
}

// Allow checks whether a message should be allowed through.
// Returns true if this is the first occurrence or the window has elapsed.
// Returns false if we're still within the suppression window.
func (tm *ThrottleManager) Allow(key string, window time.Duration) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	now := time.Now()
	tm.sweepExpired(now)

	if exp, ok := tm.expiry[key]; ok && now.Before(exp) {
		return false
	}
	tm.expiry[key] = now.Add(window)
	return true
}

// sweepExpired drops entries whose suppression window has elapsed. It runs at
// most once per sweepInterval and assumes the caller holds tm.mu.
func (tm *ThrottleManager) sweepExpired(now time.Time) {
	if now.Sub(tm.lastSweep) < sweepInterval {
		return
	}
	for k, exp := range tm.expiry {
		if !now.Before(exp) {
			delete(tm.expiry, k)
		}
	}
	tm.lastSweep = now
}

// ThrottleKey builds the composite key for throttle lookups.
// Format: "{phase}:{ruleIndex}:{resolvedKey}"
func ThrottleKey(phase string, ruleIndex int, resolvedKey string) string {
	return fmt.Sprintf("%s:%d:%s", phase, ruleIndex, resolvedKey)
}
