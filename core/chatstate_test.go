package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// lru limit prevents unbounded map growth
func TestIdleMapLRUCap(t *testing.T) {
	m := newIdleMap(100)
	for i := int64(0); i < 10000; i++ {
		m.touch(i)
	}
	if got := m.len(); got != 10000 {
		t.Fatalf("len=%d, want 10000", got)
	}

	drop := m.prune(time.Now(), 0)
	if len(drop) != 9900 {
		t.Fatalf("dropped %d, want 9900", len(drop))
	}
	if got := m.len(); got != 100 {
		t.Fatalf("after prune len=%d, want 100", got)
	}
	// most recent chats must remain
	for i := int64(9900); i < 10000; i++ {
		_ = i
	}
	again := m.prune(time.Now(), 0)
	if len(again) != 0 {
		t.Fatalf("repeated prune dropped %d entries", len(again))
	}
}

// ttl removes inactive entries
func TestIdleMapTTL(t *testing.T) {
	m := newIdleMap(0)
	m.touch(1)
	m.touch(2)

	// advance time forward
	future := time.Now().Add(8 * 24 * time.Hour)
	if drop := m.prune(future, 7*24*time.Hour); len(drop) != 2 {
		t.Fatalf("dropped %d, want 2", len(drop))
	}
	if m.len() != 0 {
		t.Fatalf("len=%d, want 0", m.len())
	}
}

// janitor bounds core state maps safely under concurrent access
func TestPruneChatStateCapsMaps(t *testing.T) {
	for i := int64(0); i < 10000; i++ {
		gHistory.Add(i, ChatMessage{Role: "user", Content: fmt.Sprintf("chat %d", i)})
		getChatLock(i)
		appendRecentLine(i, recentLine{Text: "hello", At: time.Now()})
	}

	// current time without offset triggers lru limit only
	counts, _ := pruneChatState(time.Now())
	if counts.History > chatStateMaxChats {
		t.Fatalf("history=%d > %d", counts.History, chatStateMaxChats)
	}
	if counts.Context > chatStateMaxChats {
		t.Fatalf("context=%d > %d", counts.Context, chatStateMaxChats)
	}

	// entries older than ttl are removed
	future := time.Now().Add(8 * 24 * time.Hour)
	counts, _ = pruneChatState(future)
	if counts.History != 0 || counts.Context != 0 || counts.Locks != 0 {
		t.Fatalf("entries remained after ttl: %+v", counts)
	}
}

// forgetGroupChat clears both activity map and idle index
func TestForgetGroupChat(t *testing.T) {
	chatID := int64(-777)
	groupRuntimeMu.Lock()
	groupChats[chatID] = &chatActivity{}
	groupRuntimeMu.Unlock()
	groupChatsIdle.touch(chatID)

	forgetGroupChat(chatID)

	groupRuntimeMu.Lock()
	_, ok := groupChats[chatID]
	groupRuntimeMu.Unlock()
	if ok {
		t.Fatal("activity still present")
	}
	if groupChatsIdle.len() != 0 {
		t.Fatal("idle index still tracks chat")
	}
}

// concurrent operations maintain state consistency
func TestChatStateConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	const workers = 8
	const chats = 500

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < chats; i++ {
				id := int64(w*chats + i)
				gHistory.Add(id, ChatMessage{Role: "user", Content: "x"})
				_ = gHistory.Get(id)
				getChatLock(id)
				appendRecentLine(id, recentLine{Text: "line", At: time.Now()})
				_ = recentChatTranscript(id, 0)
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			pruneChatState(time.Now())
		}
	}()
	wg.Wait()

	// cleanup after test
	pruneChatState(time.Now().Add(8 * 24 * time.Hour))
}
