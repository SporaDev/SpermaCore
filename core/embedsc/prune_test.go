package embedsc

import (
	"testing"
	"time"
)

// prune stores removes inactive stores and respects limits
func TestPruneStores(t *testing.T) {
	now := time.Now()

	storesMu.Lock()
	prev := stores
	stores = map[int64]*Store{
		1: {chatID: 1, loaded: true, lastUsed: now.Add(-48 * time.Hour)},
		2: {chatID: 2, loaded: true, lastUsed: now.Add(-time.Hour)},
		3: {chatID: 3, loaded: true, lastUsed: now},
	}
	storesMu.Unlock()
	defer func() {
		storesMu.Lock()
		stores = prev
		storesMu.Unlock()
	}()

	if removed := PruneStores(now.Add(-24*time.Hour), 0); removed != 1 {
		t.Fatalf("TTL removed %d, want 1", removed)
	}
	if got := StoreCount(); got != 2 {
		t.Fatalf("after TTL stores=%d, want 2", got)
	}

	// limit 1 retains newest store
	if removed := PruneStores(now.Add(-1000*time.Hour), 1); removed != 1 {
		t.Fatalf("LRU removed %d, want 1", removed)
	}
	storesMu.Lock()
	_, hasNewest := stores[3]
	n := len(stores)
	storesMu.Unlock()
	if n != 1 || !hasNewest {
		t.Fatalf("after LRU stores=%d (newest present=%v), want {3}", n, hasNewest)
	}
}
